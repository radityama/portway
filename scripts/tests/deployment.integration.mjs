import assert from 'node:assert/strict';
import { test } from 'node:test';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdir, writeFile, stat } from 'node:fs/promises';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { platformFixture } from '../testing/platform.mjs';
import { traffic, workload, payloadDigest } from '../testing/traffic.mjs';
import { root, start, stop, until, freePort } from '../testing/control.mjs';

const execute = promisify(execFile);
const rawSeconds = process.env.PORTWAY_SOAK_SECONDS ?? '10';
assert.match(rawSeconds, /^(?:[1-9]\d?|[12]\d\d|300)$/);
const seconds = Number(rawSeconds);
const prefix = 'portway_relay_';

test(
  'isolated deployment acceptance: restart, backup restore and sustained forwarding',
  { timeout: (seconds + 120) * 1000 },
  async (t) => {
    const fixture = await platformFixture({
      apiOverrides: { API_CREDENTIAL_TTL_SECONDS: '900' },
    });
    const results = {
      profile: 'phase19-isolated-acceptance',
      soakSeconds: seconds,
    };
    const get = async (base, path) => {
      const response = await fetch(base + path, {
        headers: { Authorization: 'Bearer ' + fixture.database.bearer },
        signal: AbortSignal.timeout(5000),
      });
      assert.equal(response.status, 200);
      return response.json();
    };
    const probe = async (id) => {
      const result = await traffic(
        fixture.url() + '/bytes?size=16384',
        fixture.ca,
        { id },
      );
      assert.equal(result.status, 200);
      assert.equal(result.length, 16384);
      assert.equal(result.digest, payloadDigest(16384));
      return result;
    };
    const drained = () =>
      until(async () => {
        const metrics = await fixture.metrics();
        return (
          [
            'streams_active',
            'requests_active',
            'public_connections_active',
          ].every((key) => metrics.get(prefix + key) === 0) &&
          fixture.active === 0
        );
      });
    const generation = async () =>
      (
        await fixture.client.tunnel.findUniqueOrThrow({
          where: { id: 'tnl_local_dev' },
        })
      ).generation.toString();
    try {
      const initialGeneration = await generation();
      await t.test(
        'migration rerun and API restart preserve policy and admitted forwarding',
        async () => {
          await stop(fixture.api);
          await probe('api-outage');
          await execute('pnpm', ['db:deploy'], {
            cwd: root,
            env: fixture.database.env,
            timeout: 30_000,
            maxBuffer: 65536,
          });
          await fixture.startAPI();
          assert.equal(
            (await get(fixture.base, '/me')).data.user.id,
            'usr_local',
          );
          assert.equal(await generation(), initialGeneration);
          assert.equal(fixture.ready(fixture.primary).length, 1);
          await probe('api-restarted');
          results.restart = 'passed';
        },
      );

      await t.test(
        'custom-format backup restores a consistent point in time into a separate database',
        async () => {
          const created = await fixture.call('/projects', 'POST', {
            name: 'Restore acceptance',
            slug: 'restore-acceptance',
          });
          assert.equal(created.status, 201);
          const project = created.value.data.project;
          const expected = {
            projects: await fixture.client.project.count(),
            audit: await fixture.client.auditLog.count(),
            keys: await fixture.client.apiKey.count(),
            generation: await generation(),
          };
          const dump = await execute(
            'docker',
            [
              'exec',
              fixture.database.name,
              'pg_dump',
              '-U',
              'portway',
              '-d',
              'portway',
              '--format=custom',
              '--no-owner',
              '--no-privileges',
            ],
            { encoding: 'buffer', timeout: 30_000, maxBuffer: 8 * 1024 * 1024 },
          );
          assert.ok(
            dump.stdout.length > 0 && dump.stdout.length < 8 * 1024 * 1024,
          );
          const backup = join(
            fixture.database.directory,
            'acceptance-backup.dump',
          );
          await writeFile(backup, dump.stdout, { mode: 0o600 });
          assert.equal((await stat(backup)).mode & 0o777, 0o600);
          const docker = (args) =>
            execute('docker', args, { timeout: 30_000, maxBuffer: 65536 });
          await docker([
            'exec',
            fixture.database.name,
            'psql',
            '-U',
            'portway',
            '-d',
            'postgres',
            '-v',
            'ON_ERROR_STOP=1',
            '-c',
            'CREATE DATABASE portway_restore',
          ]);
          await docker([
            'cp',
            backup,
            fixture.database.name + ':/tmp/acceptance-backup.dump',
          ]);
          await docker([
            'exec',
            fixture.database.name,
            'pg_restore',
            '-U',
            'portway',
            '-d',
            'portway_restore',
            '--exit-on-error',
            '--no-owner',
            '--no-privileges',
            '/tmp/acceptance-backup.dump',
          ]);
          const restoreURL = new URL(fixture.database.env.DATABASE_URL);
          restoreURL.pathname = '/portway_restore';
          const restorePort = await freePort();
          const env = {
            ...fixture.database.env,
            DATABASE_URL: restoreURL.toString(),
            API_PORT: String(restorePort),
          };
          await execute('pnpm', ['db:deploy'], {
            cwd: root,
            env,
            timeout: 30_000,
            maxBuffer: 65536,
          });
          assert.equal(
            (await fixture.call('/projects/' + project.id, 'DELETE')).status,
            204,
          );
          const restored = start(
            process.execPath,
            ['apps/api/dist/index.js'],
            env,
          );
          fixture.processes.push(restored);
          const base = `http://127.0.0.1:${restorePort}/api/v1`;
          try {
            await until(async () => {
              assert.equal(
                restored.closed(),
                false,
                'Restored API stopped before readiness',
              );
              try {
                return (
                  (
                    await fetch(`http://127.0.0.1:${restorePort}/ready`, {
                      signal: AbortSignal.timeout(1000),
                    })
                  ).status === 200
                );
              } catch {
                return false;
              }
            });
            const projects = await get(base, '/projects');
            assert.ok(
              projects.data.projects.some(
                (item) => item.id === project.id && item.name === project.name,
              ),
            );
            assert.equal(
              await fixture.client.project.findUnique({
                where: { id: project.id },
              }),
              null,
            );
            const counts = await docker([
              'exec',
              fixture.database.name,
              'psql',
              '-U',
              'portway',
              '-d',
              'portway_restore',
              '-tA',
              '-v',
              'ON_ERROR_STOP=1',
              '-c',
              "SELECT json_build_object('projects', (SELECT count(*) FROM \"Project\"), 'audit', (SELECT count(*) FROM \"AuditLog\"), 'keys', (SELECT count(*) FROM \"ApiKey\"), 'generation', (SELECT generation::text FROM \"Tunnel\" WHERE id = 'tnl_local_dev'))",
            ]);
            assert.deepEqual(JSON.parse(counts.stdout), expected);
            assert.equal((await get(base, '/me')).data.user.id, 'usr_local');
            await probe('backup-restored');
            results.restore = {
              status: 'passed',
              archiveBytes: dump.stdout.length,
            };
          } finally {
            await stop(restored);
          }
        },
      );

      await t.test(
        'bounded sustained traffic preserves bytes and releases stream resources',
        async () => {
          await drained();
          const before = await fixture.metrics();
          const begin = Date.now();
          let requests = 0;
          while (Date.now() - begin < seconds * 1000) {
            await workload(4, 4, (index) => probe('soak-' + index));
            requests += 4;
            await drained();
            await delay(100);
          }
          const after = await fixture.metrics();
          assert.ok(after.get(prefix + 'go_heap_bytes') < 128 * 1024 * 1024);
          assert.ok(
            after.get(prefix + 'go_goroutines') <=
              before.get(prefix + 'go_goroutines') + 16,
          );
          assert.equal(await generation(), initialGeneration);
          assert.equal(fixture.ready(fixture.primary).length, 1);
          results.soak = {
            status: 'passed',
            elapsedMS: Date.now() - begin,
            requests,
            heapBytes: after.get(prefix + 'go_heap_bytes'),
            goroutines: after.get(prefix + 'go_goroutines'),
            activeStreams: after.get(prefix + 'streams_active'),
          };
        },
      );
      for (const process of fixture.processes)
        for (const secret of fixture.secrets)
          assert.equal(
            process.output().includes(secret),
            false,
            'Credential in fixture output',
          );
      const directory = join(root, '.tmp/acceptance');
      await mkdir(directory, { recursive: true });
      await writeFile(
        join(directory, 'phase19-deployment.json'),
        JSON.stringify(results, null, 2) + '\n',
        { mode: 0o600 },
      );
      t.diagnostic(JSON.stringify(results));
    } finally {
      await fixture.close();
    }
  },
);
