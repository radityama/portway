import assert from 'node:assert/strict';
import { test } from 'node:test';
import { randomBytes } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { createServer } from 'node:http';
import { execFileSync } from 'node:child_process';
import { databaseFixture } from '../testing/database.mjs';
import {
  root,
  freePort,
  start,
  stop,
  until,
  publicGet,
} from '../testing/control.mjs';
import { databaseClient } from '../../apps/api/src/database.ts';
import { provision } from '../../apps/api/src/provision.ts';
import { loadSeed } from '../../apps/api/src/seed.ts';
import { digest } from '../../apps/api/src/store.ts';

test(
  'two real relays: Redis outage isolation, operator drain, fresh-report transport failover and no request replay',
  { timeout: 90_000 },
  async (t) => {
    const fixture = await databaseFixture();
    const processes = [];
    const client = databaseClient(fixture.env.DATABASE_URL);
    let upstream;
    try {
      const ports = await Promise.all(
        Array.from({ length: 5 }, () => freePort()),
      );
      assert.equal(new Set(ports).size, 5);
      const [apiPort, relayA, publicA, relayB, publicB] = ports;
      await client.relay.update({
        where: { id: 'rel_local' },
        data: { port: relayA },
      });
      const backupToken = randomBytes(32).toString('base64url');
      const backupFile = join(fixture.privateDir, 'relay-api-token-backup');
      writeFileSync(backupFile, backupToken + '\n', { mode: 0o600 });
      const seed = loadSeed(fixture.env.API_SEED_FILE);
      seed.relays.push({
        id: 'rel_z_backup',
        name: 'Backup',
        region: 'local',
        hostname: 'localhost',
        port: relayB,
        protocol: 'tls',
        status: 'HEALTHY',
        lastSeenAt: null,
      });
      seed.relayKeys.push({
        relayId: 'rel_z_backup',
        tokenHash: digest(backupToken),
        expiresAt: new Date(Date.now() + 3600000).toISOString(),
      });
      await provision(client, seed);
      execFileSync(
        'go',
        ['run', './cmd/dev-init', '-dir', fixture.privateDir],
        { cwd: root, timeout: 30_000, stdio: 'pipe' },
      );
      const base = `http://127.0.0.1:${apiPort}/api/v1`;
      const env = {
        ...fixture.env,
        API_PORT: String(apiPort),
        API_CREDENTIAL_TTL_SECONDS: '60',
        RELAY_API_URL: base,
        RELAY_API_TOKEN_FILE: join(fixture.privateDir, 'relay-api-token'),
        RELAY_ID: 'rel_local',
        RELAY_API_REPORT_INTERVAL: '1s',
        RELAY_TLS_CERT_FILE: join(fixture.privateDir, 'relay-cert.pem'),
        RELAY_TLS_KEY_FILE: join(fixture.privateDir, 'relay-key.pem'),
        PUBLIC_TLS_CERT_FILE: join(fixture.privateDir, 'public-cert.pem'),
        PUBLIC_TLS_KEY_FILE: join(fixture.privateDir, 'public-key.pem'),
        PORTWAY_JSON: '1',
        PORTWAY_API_URL: base,
        PORTWAY_API_TOKEN_FILE: join(fixture.privateDir, 'api-token'),
        PORTWAY_RELAY_CA_FILE: join(fixture.privateDir, 'ca.pem'),
        PORTWAY_STATE_DIR: join(fixture.directory, 'agent-state'),
      };
      delete env.PORTWAY_GENERATION;
      const call = async (
        path,
        method = 'GET',
        body,
        bearer = fixture.bearer,
      ) => {
        const response = await fetch(base + path, {
          method,
          headers: {
            Authorization: 'Bearer ' + bearer,
            ...(body === undefined
              ? {}
              : { 'Content-Type': 'application/json' }),
          },
          ...(body === undefined ? {} : { body: JSON.stringify(body) }),
          signal: AbortSignal.timeout(5000),
        });
        return { status: response.status, value: await response.json() };
      };
      const api = start(process.execPath, ['apps/api/dist/index.js'], env);
      processes.push(api);
      await until(async () => {
        try {
          return (
            (await fetch(base.replace('/api/v1', '/health'))).status === 200
          );
        } catch {
          return false;
        }
      });
      assert.equal(
        (await fetch(base.replace('/api/v1', '/ready'))).status,
        503,
      );
      t.diagnostic('API policy waits for reports');
      const startA = () => {
        const process = start(join(root, 'bin/portway-relay'), [], {
          ...env,
          RELAY_PORT: String(relayA),
          PUBLIC_PORT: String(publicA),
        });
        processes.push(process);
        return process;
      };
      let nodeA = startA();
      const nodeB = start(join(root, 'bin/portway-relay'), [], {
        ...env,
        RELAY_PORT: String(relayB),
        PUBLIC_PORT: String(publicB),
        RELAY_ID: 'rel_z_backup',
        RELAY_API_TOKEN_FILE: backupFile,
      });
      processes.push(nodeB);
      await until(
        () =>
          nodeA.output().includes('relay_presence_registered') &&
          nodeB.output().includes('relay_presence_registered'),
      );
      t.diagnostic('both reporters registered');
      let hits = 0,
        mutations = 0,
        mutationStarted;
      const mutated = new Promise((r) => {
        mutationStarted = r;
      });
      upstream = createServer((req, res) => {
        hits++;
        if (req.url === '/events') {
          res.writeHead(200, { 'Content-Type': 'text/event-stream' });
          res.write('data: first\n\n');
          const timer = setTimeout(() => res.end('data: last\n\n'), 1400);
          res.on('close', () => clearTimeout(timer));
        } else if (req.url === '/mutate') {
          mutations++;
          mutationStarted();
          const timer = setTimeout(() => res.end('mutation-complete'), 5000);
          res.on('close', () => clearTimeout(timer));
        } else res.end('local-response');
      });
      await new Promise((r) => upstream.listen(0, '127.0.0.1', r));
      const cli = start(
        join(root, 'bin/portway'),
        [String(upstream.address().port)],
        { ...env, RELAY_PORT: String(relayA), PUBLIC_PORT: String(publicA) },
      );
      processes.push(cli);
      const events = () =>
        cli
          .output()
          .trim()
          .split('\n')
          .filter(Boolean)
          .map((line) => JSON.parse(line));
      const ready = () => events().filter((e) => e.event === 'ready');
      await until(() => ready().length === 1);
      t.diagnostic('agent registered at primary');
      const firstURL = ready()[0].public_url;
      assert.equal(new URL(firstURL).port, String(publicA));
      const hostname = new URL(firstURL).hostname;
      const ca = readFileSync(join(fixture.privateDir, 'public-ca.pem'));
      assert.deepEqual(await publicGet(firstURL + '/', ca), {
        status: 200,
        body: 'local-response',
      });
      const before = (
        await client.tunnel.findUnique({ where: { id: 'tnl_local_dev' } })
      ).generation.toFixed(0);
      t.diagnostic('pausing Redis');
      execFileSync('docker', ['pause', fixture.redisName], {
        timeout: 5000,
        stdio: 'pipe',
      });
      try {
        assert.deepEqual(await publicGet(firstURL + '/', ca), {
          status: 200,
          body: 'local-response',
        });
        assert.deepEqual(await publicGet(firstURL + '/events', ca), {
          status: 200,
          body: 'data: first\n\ndata: last\n\n',
        });
        const failed = await call('/tunnels/tnl_local_dev/connect', 'POST');
        assert.equal(failed.status, 503);
        assert.equal(failed.value.error.code, 'PRESENCE_UNAVAILABLE');
        assert.equal(
          (
            await client.tunnel.findUnique({ where: { id: 'tnl_local_dev' } })
          ).generation.toFixed(0),
          before,
        );
      } finally {
        execFileSync('docker', ['unpause', fixture.redisName], {
          timeout: 5000,
          stdio: 'pipe',
        });
      }
      t.diagnostic('Redis unpaused');
      await until(
        async () =>
          (await call('/relays/rel_local')).value.data.relay.status ===
          'HEALTHY',
      );
      assert.equal(cli.closed(), false);
      t.diagnostic('reports recovered');
      let began;
      const beginning = new Promise((r) => {
        began = r;
      });
      const drainingResponse = publicGet(firstURL + '/events', ca, () =>
        began(),
      );
      await beginning;
      assert.equal(
        (
          await call(
            '/internal/relays/rel_local/drain',
            'POST',
            undefined,
            fixture.relayBearer,
          )
        ).status,
        200,
      );
      assert.deepEqual(await drainingResponse, {
        status: 200,
        body: 'data: first\n\ndata: last\n\n',
      });
      await until(() => nodeA.closed() && ready().length >= 2, 15_000);
      const backupURL = ready().at(-1).public_url;
      assert.equal(new URL(backupURL).hostname, hostname);
      assert.equal(new URL(backupURL).port, String(publicB));
      assert.deepEqual(await publicGet(backupURL + '/', ca), {
        status: 200,
        body: 'local-response',
      });
      assert.equal(
        (await client.relay.findUnique({ where: { id: 'rel_local' } })).status,
        'DRAINING',
      );
      assert.equal(
        (
          await call(
            '/internal/relays/rel_local/activate',
            'POST',
            undefined,
            fixture.relayBearer,
          )
        ).status,
        200,
      );
      nodeA = startA();
      await until(() => nodeA.output().includes('relay_presence_registered'));
      assert.equal(
        (await call('/relays/rel_z_backup')).value.data.relay.status,
        'HEALTHY',
      );
      const interrupted = publicGet(backupURL + '/mutate', ca).then(
        () => false,
        () => true,
      );
      await mutated;
      nodeB.child.kill('SIGKILL');
      await nodeB.completion;
      assert.equal(await interrupted, true);
      // The failed node's report is still fresh. Avoiding its assigned ID must
      // select the alternative without waiting for the 15-second report TTL.
      const crashedAt = Date.now();
      await until(() => ready().length >= 3, 10_000);
      assert.ok(Date.now() - crashedAt < 10_000);
      assert.equal(ready().at(-1).public_url, firstURL);
      assert.deepEqual(await publicGet(firstURL + '/', ca), {
        status: 200,
        body: 'local-response',
      });
      assert.equal(mutations, 1);
      assert.equal(hits, 7);
      const generations = events()
        .filter((e) => e.event === 'relay_assigned')
        .map((e) => BigInt(e.generation));
      assert.ok(generations.every((g, i) => i === 0 || g > generations[i - 1]));
      for (const process of processes)
        for (const token of [fixture.bearer, fixture.relayBearer, backupToken])
          assert.ok(!process.output().includes(token));
    } catch (error) {
      for (const process of processes)
        t.diagnostic(process.output().slice(-2000));
      throw error;
    } finally {
      for (const process of processes.reverse()) await stop(process);
      if (upstream) await new Promise((r) => upstream.close(r));
      await client.$disconnect();
      await fixture.close();
    }
  },
);
