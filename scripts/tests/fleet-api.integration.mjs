import assert from 'node:assert/strict';
import { test } from 'node:test';
import { execFileSync } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { databaseFixture } from '../testing/database.mjs';
import { Database, databaseClient } from '../../apps/api/src/database.ts';
import { RedisPresence } from '../../apps/api/src/presence.ts';
import { PrismaStore } from '../../apps/api/src/prisma-store.ts';
import { createApp } from '../../apps/api/src/app.ts';
import { loadSeed } from '../../apps/api/src/seed.ts';
import { provision } from '../../apps/api/src/provision.ts';
import { digest } from '../../apps/api/src/store.ts';

test(
  'PostgreSQL and real Redis fleet policy across API instances',
  { timeout: 60_000 },
  async (t) => {
    const fixture = await databaseFixture(),
      client = databaseClient(fixture.env.DATABASE_URL),
      other = databaseClient(fixture.env.DATABASE_URL);
    const presence = new RedisPresence(fixture.env.REDIS_URL),
      otherPresence = new RedisPresence(fixture.env.REDIS_URL);
    const store = new PrismaStore(new Database(client), presence),
      second = new PrismaStore(new Database(other), otherPresence),
      app = createApp(store),
      otherApp = createApp(second);
    const report = {
      instanceId: '1'.repeat(32),
      status: 'HEALTHY',
      activeConnections: 0,
      activeTunnels: 0,
      retainedTunnels: 0,
      activeStreams: 0,
      maxConnections: 2,
      maxTunnels: 8,
      maxStreams: 32,
    };
    const call = async (
      path,
      method = 'GET',
      body,
      bearer = fixture.bearer,
      application = app,
    ) => {
      const response = await application.request('/api/v1' + path, {
        method,
        headers: {
          Authorization: 'Bearer ' + bearer,
          ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      });
      return { status: response.status, value: await response.json() };
    };
    const redis = (...args) =>
      execFileSync(
        'docker',
        ['exec', fixture.redisName, 'redis-cli', ...args],
        { timeout: 5000, stdio: 'pipe' },
      );
    const backupToken = 'b'.repeat(43);
    let ack;
    try {
      await t.test(
        'live reports replace seeded health; only scoped keys can report or operate a node',
        async () => {
          assert.equal(await store.ready(), false);
          assert.equal(
            (await call('/relays/rel_local')).value.data.relay.status,
            'OFFLINE',
          );
          assert.equal(
            (await call('/internal/relays/rel_local/register', 'POST', report))
              .status,
            401,
          );
          assert.equal(
            (
              await call(
                '/internal/relays/rel_foreign/register',
                'POST',
                report,
                fixture.relayBearer,
              )
            ).status,
            403,
          );
          const seed = loadSeed(fixture.env.API_SEED_FILE);
          seed.relays.push({
            id: 'rel_backup',
            name: 'Backup',
            region: 'local',
            hostname: 'localhost',
            port: 8082,
            protocol: 'tls',
            status: 'HEALTHY',
            lastSeenAt: null,
          });
          seed.relayKeys.push({
            relayId: 'rel_backup',
            tokenHash: digest(backupToken),
            expiresAt: new Date(Date.now() + 3600000).toISOString(),
          });
          await provision(client, seed);
          const registered = await call(
            '/internal/relays/rel_local/register',
            'POST',
            report,
            fixture.relayBearer,
          );
          assert.equal(registered.status, 200);
          ack = registered.value.data;
          assert.equal(
            (
              await call(
                '/internal/relays/rel_backup/register',
                'POST',
                { ...report, instanceId: '2'.repeat(32) },
                backupToken,
                otherApp,
              )
            ).status,
            200,
          );
          assert.equal(await second.ready(), true);
          const metadata = (
            await call(
              '/relays/rel_local',
              'GET',
              undefined,
              fixture.bearer,
              otherApp,
            )
          ).value.data.relay;
          assert.equal(metadata.status, 'HEALTHY');
          assert.equal(metadata.capacity.maxConnections, 2);
          assert.equal(
            (
              await call(
                '/internal/relays/rel_local/register',
                'POST',
                { ...report, hostname: 'attacker.example.test' },
                fixture.relayBearer,
              )
            ).status,
            400,
          );
        },
      );
      await t.test(
        'atomic reservation capacity spans API instances and rejection allocates nothing',
        async () => {
          const ids = [];
          for (let i = 0; i < 6; i++) {
            const created = await call('/tunnels', 'POST', {
              projectId: 'prj_local',
              name: 'Fleet ' + i,
              slug: 'fleet-' + i,
              type: 'PERSISTENT',
              protocol: 'http',
              localHost: '127.0.0.1',
              localPort: 3000,
            });
            assert.equal(created.status, 201);
            ids.push(created.value.data.tunnel.id);
          }
          const results = await Promise.all(
            ids.map((id, i) =>
              call(
                '/tunnels/' + id + '/connect',
                'POST',
                {},
                fixture.bearer,
                i % 2 ? app : otherApp,
              ),
            ),
          );
          assert.equal(results.filter((r) => r.status === 200).length, 4);
          assert.equal(
            results.filter(
              (r) =>
                r.status === 503 && r.value.error.code === 'RELAY_UNAVAILABLE',
            ).length,
            2,
          );
          const counts = new Map();
          results
            .filter((r) => r.status === 200)
            .forEach((r) =>
              counts.set(
                r.value.data.relay.id,
                (counts.get(r.value.data.relay.id) ?? 0) + 1,
              ),
            );
          assert.deepEqual([...counts.values()].sort(), [2, 2]);
          const owned = results.findIndex((r) => r.status === 200);
          const prior = results[owned].value.data;
          const renewed = await call(
            '/tunnels/' + ids[owned] + '/connect',
            'POST',
            {},
            fixture.bearer,
            otherApp,
          );
          assert.equal(renewed.status, 200);
          assert.equal(renewed.value.data.relay.id, prior.relay.id);
          assert.ok(
            BigInt(renewed.value.data.generation) > BigInt(prior.generation),
          );
          const rejected = results.findIndex((r) => r.status === 503);
          assert.equal(
            (await call('/tunnels/' + ids[rejected] + '/connect', 'POST', {}))
              .status,
            503,
          );
          for (let i = 0; i < results.length; i++)
            if (results[i].status === 503) {
              assert.equal(
                (
                  await client.tunnel.findUnique({ where: { id: ids[i] } })
                ).generation.toFixed(0),
                '0',
              );
              assert.equal(
                await client.tunnelCredential.count({
                  where: { tunnelId: ids[i] },
                }),
                0,
              );
            }
        },
      );
      await t.test(
        'Lua fences incarnations and duplicates; durable drain policy survives refresh and seed reruns',
        async () => {
          const body = { ...report, leaseId: ack.leaseId, sequence: 1 };
          const observed = await call(
            '/internal/relays/rel_local/report',
            'POST',
            body,
            fixture.relayBearer,
          );
          assert.equal(observed.status, 200);
          assert.deepEqual(
            (
              await call(
                '/internal/relays/rel_local/report',
                'POST',
                body,
                fixture.relayBearer,
                otherApp,
              )
            ).value.data,
            observed.value.data,
          );
          assert.equal(
            (
              await call(
                '/internal/relays/rel_local/report',
                'POST',
                { ...body, status: 'DEGRADED' },
                fixture.relayBearer,
              )
            ).value.error.code,
            'PRESENCE_STALE',
          );
          for (let i = 0; i < 2; i++)
            assert.equal(
              (
                await call(
                  '/internal/relays/rel_local/drain',
                  'POST',
                  undefined,
                  fixture.relayBearer,
                  i ? app : otherApp,
                )
              ).status,
              200,
            );
          await provision(client, loadSeed(fixture.env.API_SEED_FILE));
          assert.equal(
            (await client.relay.findUnique({ where: { id: 'rel_local' } }))
              .status,
            'DRAINING',
          );
          assert.equal(
            await client.auditLog.count({
              where: { action: 'relay.drain', resourceId: 'rel_local' },
            }),
            1,
          );
          assert.equal(
            (
              await call(
                '/internal/relays/rel_local/report',
                'POST',
                { ...body, sequence: 2 },
                fixture.relayBearer,
              )
            ).value.data.drainRequested,
            true,
          );
          assert.equal(
            (await call('/relays/rel_local')).value.data.relay.status,
            'DRAINING',
          );
          await call(
            '/internal/relays/rel_local/report',
            'POST',
            { ...body, sequence: 3, status: 'DRAINING' },
            fixture.relayBearer,
          );
          await call(
            '/internal/relays/rel_local/activate',
            'POST',
            undefined,
            fixture.relayBearer,
          );
          const replaced = await call(
            '/internal/relays/rel_local/register',
            'POST',
            { ...report, instanceId: '3'.repeat(32) },
            fixture.relayBearer,
          );
          assert.equal(replaced.status, 200);
          assert.notEqual(replaced.value.data.leaseId, ack.leaseId);
          assert.equal(
            (
              await call(
                '/internal/relays/rel_local/report',
                'POST',
                { ...body, sequence: 4 },
                fixture.relayBearer,
              )
            ).value.error.code,
            'PRESENCE_STALE',
          );
          ack = replaced.value.data;
        },
      );
      await t.test(
        'expired or corrupt Redis state fails safely; expiry allows re-registration',
        async () => {
          redis('PEXPIRE', 'portway:relay:rel_local:presence', '1');
          await delay(20);
          assert.equal(
            (await call('/relays/rel_local')).value.data.relay.status,
            'OFFLINE',
          );
          assert.equal(
            (
              await call(
                '/internal/relays/rel_local/report',
                'POST',
                {
                  ...report,
                  instanceId: '3'.repeat(32),
                  leaseId: ack.leaseId,
                  sequence: 1,
                },
                fixture.relayBearer,
              )
            ).value.error.code,
            'PRESENCE_EXPIRED',
          );
          const registered = await call(
            '/internal/relays/rel_local/register',
            'POST',
            report,
            fixture.relayBearer,
          );
          assert.equal(registered.status, 200);
          redis('SET', 'portway:relay:rel_local:presence', '{"corrupt":true}');
          const failed = await call('/relays/rel_local');
          assert.equal(failed.status, 503);
          assert.equal(failed.value.error.code, 'PRESENCE_UNAVAILABLE');
          assert.ok(!JSON.stringify(failed).includes(fixture.relayBearer));
          redis('DEL', 'portway:relay:rel_local:presence');
          assert.equal(
            (
              await call(
                '/internal/relays/rel_local/register',
                'POST',
                report,
                fixture.relayBearer,
              )
            ).status,
            200,
          );
        },
      );
    } finally {
      await Promise.all([
        presence.close(),
        otherPresence.close(),
        client.$disconnect(),
        other.$disconnect(),
      ]);
      await fixture.close();
    }
  },
);
