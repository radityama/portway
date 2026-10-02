import assert from 'node:assert/strict';
import { test } from 'node:test';
import { readFileSync, existsSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { platformFixture } from '../testing/platform.mjs';
import { dnsFixture } from '../testing/dns.mjs';
import { root, start, stop, until, publicGet } from '../testing/control.mjs';

test(
  'CLI: managed sessions, scoped commands, discovery, owned stop and outage cleanup',
  { timeout: 120_000 },
  async (t) => {
    const dns = await dnsFixture();
    let f;
    try {
      f = await platformFixture({
        apiOverrides: { API_DNS_SERVER: dns.server },
      });
      const profile = join(f.database.directory, 'cli-profile');
      const env = { ...process.env };
      for (const key of Object.keys(env))
        if (key.startsWith('PORTWAY_')) delete env[key];
      Object.assign(env, {
        PORTWAY_CONFIG_DIR: profile,
        PORTWAY_JSON: '1',
        PORTWAY_SHUTDOWN_TIMEOUT: '2s',
      });
      const cli = (args, overrides = {}) => {
        const process = start(join(root, 'bin/portway'), args, {
          ...env,
          ...overrides,
        });
        f.processes.push(process);
        return process;
      };
      const command = async (args, code = 0, overrides = {}) => {
        const process = cli(args, overrides);
        await until(() => process.closed(), 8000);
        const [actual] = await process.completion;
        assert.equal(actual, code, process.output());
        const events = process
          .output()
          .trim()
          .split('\n')
          .map((line) => JSON.parse(line));
        return events.at(-1);
      };
      const login = () =>
        command([
          'login',
          '--token-file',
          join(f.database.privateDir, 'api-token'),
          '--api-url',
          f.base,
          '--relay-ca-file',
          join(f.database.privateDir, 'ca.pem'),
        ]);
      let session;
      await t.test(
        'login keeps private session and refuses endpoint overrides',
        async () => {
          const result = await login();
          assert.equal(result.event, 'logged_in');
          session = readFileSync(join(profile, 'session.token'), 'utf8').trim();
          f.secrets.push(session);
          for (const name of ['config.json', 'session.json', 'session.token'])
            assert.equal(statSync(join(profile, name)).mode & 0o777, 0o600);
          const rejected = await command(
            ['list', '--api-url', 'http://127.0.0.1:1/api/v1'],
            1,
          );
          assert.match(rejected.error, /belongs to another API/);
          assert.equal(
            (await command(['config'])).data.session.api_url,
            f.base,
          );
          assert.equal(
            (await command(['list', '--project', 'prj_local', '--limit', '1']))
              .data.items.length,
            1,
          );
        },
      );
      await t.test('doctor checks are read-only', async () => {
        const generation = (
          await f.client.tunnel.findUniqueOrThrow({
            where: { id: 'tnl_local_dev' },
          })
        ).generation.toString();
        const credentials = await f.client.tunnelCredential.count();
        const result = await command(['doctor', String(f.localPort)]);
        assert.equal(result.data.ok, true);
        assert.ok(
          result.data.checks.some(
            (check) => check.check === 'relay_tls' && check.ok,
          ),
        );
        assert.equal(await f.client.tunnelCredential.count(), credentials);
        assert.equal(
          (
            await f.client.tunnel.findUniqueOrThrow({
              where: { id: 'tnl_local_dev' },
            })
          ).generation.toString(),
          generation,
        );
      });
      await t.test(
        'no-port start creates ephemeral tunnel, forwards HTTPS and cleans up after stop',
        async () => {
          await command(['config', 'set', 'local_port', String(f.localPort)]);
          const process = cli(['start']);
          await until(() => {
            assert.equal(process.closed(), false, process.output());
            return f.ready(process).length === 1;
          });
          const registered = f
            .events(process)
            .find((event) => event.event === 'tunnel_registered');
          const id = registered.tunnel_id;
          const state = await command(['status', id]);
          assert.equal(state.data.local.state, 'ready');
          assert.equal(state.data.tunnel.type, 'EPHEMERAL');
          assert.equal(
            (await publicGet(f.url(process) + '/', f.ca)).status,
            200,
          );
          assert.equal((await command(['stop', id])).event, 'stop_requested');
          await until(() => process.closed());
          assert.equal((await process.completion)[0], 0);
          assert.equal(
            (await f.client.tunnel.findUniqueOrThrow({ where: { id } })).status,
            'REVOKED',
          );
          assert.equal((await command(['status', id])).data.local, null);
        },
      );
      let persistent;
      await t.test(
        'persistent management, bounded logs and domain DNS proof',
        async () => {
          persistent = (
            await command([
              'create',
              '--name',
              'CLI integration',
              '--project',
              'prj_local',
              '--port',
              String(f.localPort),
              '--use',
            ])
          ).data.id;
          assert.equal(
            (await command(['config'])).data.settings.tunnel_id,
            persistent,
          );
          const created = (await command(['domain', 'add', 'cli.example.test']))
            .data;
          const rotated = (
            await command(['domain', 'challenge', created.domain.id])
          ).data;
          dns.records.set(rotated.verification.name, [
            rotated.verification.value,
          ]);
          assert.equal(
            (await command(['domain', 'verify', created.domain.id])).data.domain
              .status,
            'VERIFIED',
          );
          assert.equal(
            (await command(['domain', 'activate', created.domain.id])).data
              .domain.status,
            'ACTIVE',
          );
          await command(['domain', 'remove', created.domain.id]);
          const process = cli(['start']);
          await until(() => {
            assert.equal(process.closed(), false, process.output());
            return f.ready(process).length === 1;
          });
          assert.equal(
            (await publicGet(f.url(process) + '/', f.ca)).status,
            200,
          );
          await command(['logs', '--limit', '4']);
          await command(['logs', '--limit', '5'], 2);
          await command(['stop']);
          await until(() => process.closed());
          assert.equal(
            (
              await f.client.tunnel.findUniqueOrThrow({
                where: { id: persistent },
              })
            ).status === 'REVOKED',
            false,
          );
        },
      );
      await t.test(
        'logout revokes only managed session; source API key still works',
        async () => {
          const result = await command(['logout']);
          assert.equal(result.data.remote_revocation_confirmed, true);
          assert.equal(existsSync(join(profile, 'session.token')), false);
          const revoked = await fetch(f.base + '/me', {
            headers: { Authorization: 'Bearer ' + session },
            signal: AbortSignal.timeout(3000),
          });
          assert.equal(revoked.status, 401);
          assert.equal((await f.call('/me')).status, 200);
          await login();
          f.secrets.push(
            readFileSync(join(profile, 'session.token'), 'utf8').trim(),
          );
          await command(['delete', persistent]);
        },
      );
      await t.test(
        'local stop and logout remain available during API outage',
        async () => {
          await command(['config', 'set', 'tunnel_id', '']);
          const process = cli(['start']);
          await until(() => {
            assert.equal(process.closed(), false, process.output());
            return f.ready(process).length === 1;
          });
          const id = f
            .events(process)
            .find((event) => event.event === 'tunnel_registered').tunnel_id;
          await stop(f.api);
          assert.equal(
            (await publicGet(f.url(process) + '/', f.ca)).status,
            200,
          );
          await command(['stop', id]);
          await until(() => process.closed(), 8000);
          assert.equal((await process.completion)[0], 0);
          assert.ok(
            f
              .events(process)
              .some((event) => event.event === 'ephemeral_cleanup_unconfirmed'),
          );
          const logout = await command(['logout'], 1);
          assert.match(
            logout.error,
            /local session cleared; remote revocation unconfirmed/,
          );
          assert.equal(existsSync(join(profile, 'session.token')), false);
          await f.startAPI();
          assert.equal((await f.call('/tunnels/' + id, 'DELETE')).status, 204);
        },
      );
      for (const process of f.processes)
        for (const secret of f.secrets)
          assert.equal(
            process.output().includes(secret),
            false,
            'credential appeared in process output',
          );
    } finally {
      await f?.close();
      await dns.close();
    }
  },
);
