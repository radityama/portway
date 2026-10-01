import { RedisPresence } from '../../apps/api/src/presence.ts';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { databaseFixture, root } from '../testing/database.mjs';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
const execute = promisify(execFile);
import { databaseClient, Database } from '../../apps/api/src/database.ts';
import { PrismaStore } from '../../apps/api/src/prisma-store.ts';
import { createApp } from '../../apps/api/src/app.ts';
import { provision } from '../../apps/api/src/provision.ts';
import { loadSeed } from '../../apps/api/src/seed.ts';
import { digest } from '../../apps/api/src/store.ts';

test(
  'PostgreSQL migration, durability, cross-instance atomicity, rollback, indexes and failure isolation',
  { timeout: 90_000 },
  async (t) => {
    const fixture = await databaseFixture();
    const client = databaseClient(fixture.env.DATABASE_URL);
    const database = new Database(client);
    const presence = new RedisPresence(fixture.env.REDIS_URL);
    const store = new PrismaStore(database, presence);
    const otherClient = databaseClient(fixture.env.DATABASE_URL);
    const otherDatabase = new Database(otherClient);
    const otherStore = new PrismaStore(otherDatabase, presence);
    const app = createApp(store),
      otherApp = createApp(otherStore);
    const call = async (
      path,
      method = 'GET',
      body,
      bearer = fixture.bearer,
      headers = {},
      application = app,
    ) => {
      const response = await application.request('/api/v1' + path, {
        method,
        headers: {
          Authorization: 'Bearer ' + bearer,
          ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
          ...headers,
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      });
      return {
        status: response.status,
        value: response.status === 204 ? null : await response.json(),
      };
    };
    try {
      await t.test(
        'migrations are deployed; hash-only seed reruns preserve rows and policy',
        async () => {
          assert.equal(
            (
              await client.$queryRaw`SELECT count(*)::int AS count FROM "_prisma_migrations" WHERE finished_at IS NOT NULL`
            )[0].count,
            3,
          );
          const seed = loadSeed(fixture.env.API_SEED_FILE);
          await provision(client, seed);
          assert.equal(await client.user.count(), 1);
          assert.equal(await client.apiKey.count(), 2);
          assert.equal(await client.tunnel.count(), 1);
          assert.equal(await store.ready(), false);
          await store.relayReport(
            fixture.relayBearer,
            'rel_local',
            {
              instanceId: 'a'.repeat(32),
              status: 'HEALTHY',
              activeConnections: 0,
              activeTunnels: 0,
              retainedTunnels: 0,
              activeStreams: 0,
              maxConnections: 128,
              maxTunnels: 1024,
              maxStreams: 32,
            },
            false,
          );
          assert.equal(await store.ready(), true);
          const failed = structuredClone(seed);
          failed.users.push({
            id: 'usr_added',
            email: 'added@example.test',
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
          });
          failed.apiKeys[0].tokenHash = 'a'.repeat(64);
          await assert.rejects(provision(client, failed));
          assert.equal(await client.user.count(), 1);
          assert.equal(await client.apiKey.count(), 2);
        },
      );
      await t.test(
        'concurrent instances replay the same project once; request conflicts never partially allocate',
        async () => {
          const request = { name: 'Durable project', slug: 'durable' };
          const headers = { 'Idempotency-Key': 'durable-project-1' };
          const responses = await Promise.all(
            Array.from({ length: 8 }, (_, i) =>
              call(
                '/projects',
                'POST',
                request,
                fixture.bearer,
                headers,
                i % 2 ? app : otherApp,
              ),
            ),
          );
          assert.ok(responses.every((r) => r.status === 201));
          const id = responses[0].value.data.project.id;
          assert.ok(responses.every((r) => r.value.data.project.id === id));
          assert.equal(await client.project.count(), 2);
          assert.equal(
            await client.auditLog.count({
              where: { action: 'project.create' },
            }),
            1,
          );
          assert.equal(await client.idempotencyRecord.count(), 1);
          assert.equal(
            (
              await call(
                '/projects',
                'POST',
                { name: 'Different', slug: 'different' },
                fixture.bearer,
                headers,
              )
            ).value.error.code,
            'IDEMPOTENCY_CONFLICT',
          );
          assert.equal(await client.project.count(), 2);
          assert.equal(
            (await call('/projects', 'POST', request)).value.error.code,
            'PROJECT_CONFLICT',
          );
        },
      );
      await t.test(
        'durable session auth, exact uint64 credentials and serial generation allocation',
        async () => {
          const login = await call(
            '/auth/login',
            'POST',
            { token: fixture.bearer },
            fixture.bearer,
            { 'Idempotency-Key': 'login-durable-1' },
          );
          const session = login.value.data.session.accessToken;
          assert.equal(
            (await call('/me', 'GET', undefined, session, {}, otherApp)).status,
            200,
          );
          const results = await Promise.all(
            Array.from({ length: 6 }, (_, i) =>
              call(
                '/tunnels/tnl_local_dev/connect',
                'POST',
                { minimumGeneration: '9007199254740993' },
                session,
                {},
                i % 2 ? app : otherApp,
              ),
            ),
          );
          assert.ok(results.every((r) => r.status === 200));
          assert.deepEqual(
            results
              .map((r) => BigInt(r.value.data.generation))
              .sort((a, b) => (a < b ? -1 : 1)),
            Array.from({ length: 6 }, (_, i) => 9007199254740993n + BigInt(i)),
          );
          const newest = results.find(
            (r) => r.value.data.generation === '9007199254740998',
          );
          const issued = newest.value.data;
          const old = results.find((r) => r !== newest).value.data;
          assert.equal(
            (
              await call(
                '/internal/credentials/verify',
                'POST',
                {
                  relayId: 'rel_local',
                  tokenHash: digest(old.credential.token),
                },
                fixture.relayBearer,
                {},
                otherApp,
              )
            ).value.error.code,
            'AUTH_REVOKED',
          );
          assert.equal(
            (
              await call(
                '/internal/credentials/verify',
                'POST',
                {
                  relayId: 'rel_local',
                  tokenHash: digest(issued.credential.token),
                },
                fixture.relayBearer,
                {},
                otherApp,
              )
            ).status,
            200,
          );
          assert.equal(
            (
              await call(
                '/auth/login',
                'POST',
                { token: fixture.bearer },
                fixture.bearer,
                { 'Idempotency-Key': 'login-durable-1' },
                otherApp,
              )
            ).value.error.code,
            'CREDENTIAL_ALREADY_ISSUED',
          );
          assert.equal(
            (
              await call(
                '/auth/logout',
                'POST',
                undefined,
                session,
                {},
                otherApp,
              )
            ).status,
            204,
          );
          assert.equal(
            (await call('/me', 'GET', undefined, session)).value.error.code,
            'AUTH_REVOKED',
          );
          assert.equal(
            (
              await call(
                '/internal/credentials/verify',
                'POST',
                {
                  relayId: 'rel_local',
                  tokenHash: digest(issued.credential.token),
                },
                fixture.relayBearer,
              )
            ).value.error.code,
            'AUTH_REVOKED',
          );
          const stored = JSON.stringify({
            credentials: await client.tunnelCredential.findMany(),
            sessions: await client.apiSession.findMany(),
            idempotency: await client.idempotencyRecord.findMany(),
            audit: await client.auditLog.findMany(),
          });
          assert.ok(
            !stored.includes(session) &&
              !stored.includes(issued.credential.token) &&
              !stored.includes(fixture.bearer),
          );
        },
      );
      await t.test(
        'rollback joins resource, credential, generation, audit and replay state atomically',
        async () => {
          const p = await store.authenticate(fixture.bearer);
          const before = {
            tunnels: await client.tunnel.findMany(),
            credentials: await client.tunnelCredential.count(),
            audit: await client.auditLog.count(),
            idempotency: await client.idempotencyRecord.count(),
          };
          await assert.rejects(
            store.mutation(
              p,
              'POST',
              '/api/v1/tunnels/tnl_local_dev/connect',
              '{}',
              'rollback-test-1',
              async () => {
                await store.connect(p, 'tnl_local_dev', {});
                throw new Error('injected post-write failure');
              },
            ),
          );
          assert.deepEqual(await client.tunnel.findMany(), before.tunnels);
          assert.equal(
            await client.tunnelCredential.count(),
            before.credentials,
          );
          assert.equal(await client.auditLog.count(), before.audit);
          assert.equal(
            await client.idempotencyRecord.count(),
            before.idempotency,
          );
        },
      );
      await t.test(
        'scoped reads, roles and keyset cursors enforce policy through database queries',
        async () => {
          const seed = loadSeed(fixture.env.API_SEED_FILE);
          const foreignToken = 'f'.repeat(43),
            viewerToken = 'v'.repeat(43);
          const now = new Date().toISOString(),
            expiresAt = new Date(Date.now() + 3600_000).toISOString();
          seed.users.push(
            {
              id: 'usr_foreign',
              email: 'foreign@example.test',
              createdAt: now,
              updatedAt: now,
            },
            {
              id: 'usr_viewer',
              email: 'viewer@example.test',
              createdAt: now,
              updatedAt: now,
            },
          );
          seed.organizations.push({
            id: 'org_foreign',
            name: 'Foreign',
            slug: 'foreign',
          });
          seed.memberships.push(
            {
              userId: 'usr_foreign',
              organizationId: 'org_foreign',
              role: 'OWNER',
            },
            {
              userId: 'usr_viewer',
              organizationId: 'org_local',
              role: 'VIEWER',
            },
          );
          seed.apiKeys.push(
            {
              id: 'key_foreign',
              userId: 'usr_foreign',
              organizationId: 'org_foreign',
              tokenHash: digest(foreignToken),
              expiresAt,
            },
            {
              id: 'key_viewer',
              userId: 'usr_viewer',
              organizationId: 'org_local',
              tokenHash: digest(viewerToken),
              expiresAt,
            },
          );
          await provision(client, seed);
          for (const [path, method] of [
            ['/projects/prj_local', 'GET'],
            ['/tunnels/tnl_local_dev', 'GET'],
            ['/tunnels/tnl_local_dev/revoke', 'POST'],
          ])
            assert.equal(
              (await call(path, method, undefined, foreignToken)).status,
              404,
            );
          assert.equal(await store.ready(), true);
          assert.equal(
            (
              await call(
                '/projects',
                'POST',
                { name: 'Forbidden', slug: 'forbidden' },
                viewerToken,
              )
            ).status,
            403,
          );
          assert.equal(
            (
              await call(
                '/tunnels/tnl_local_dev/connect',
                'POST',
                {},
                viewerToken,
              )
            ).status,
            403,
          );
          for (let i = 0; i < 3; i++)
            await call('/projects', 'POST', {
              name: 'Page ' + i,
              slug: 'page-' + i,
            });
          const first = await call('/projects?limit=2');
          const second = await call(
            '/projects?limit=2&cursor=' + first.value.meta.nextCursor,
          );
          assert.equal(second.status, 200);
          assert.equal(
            new Set(
              [...first.value.data.projects, ...second.value.data.projects].map(
                (p) => p.id,
              ),
            ).size,
            4,
          );
          assert.equal(
            (
              await call(
                '/projects?cursor=' + first.value.meta.nextCursor,
                'GET',
                undefined,
                viewerToken,
              )
            ).status,
            400,
          );
        },
      );
      await t.test(
        'database CHECK/FK constraints reject invalid data; expected indexed query paths exist',
        async () => {
          for (const value of ['-1', '18446744073709551616'])
            await assert.rejects(
              client.tunnel.update({
                where: { id: 'tnl_local_dev' },
                data: { generation: value },
              }),
            );
          await assert.rejects(
            client.membership.update({
              where: {
                organizationId_userId: {
                  organizationId: 'org_local',
                  userId: 'usr_local',
                },
              },
              data: { role: 'SUPERUSER' },
            }),
          );
          await assert.rejects(
            client.relay.update({
              where: { id: 'rel_local' },
              data: { port: 0 },
            }),
          );
          await assert.rejects(
            client.apiKey.update({
              where: { tokenHash: digest(fixture.bearer) },
              data: { relayId: 'rel_local' },
            }),
          );
          await assert.rejects(
            client.tunnelCredential.create({
              data: {
                id: 'cred_invalid',
                tunnelId: 'tnl_local_dev',
                tokenHash: 'c'.repeat(64),
                scope: 'connect',
                relayId: 'rel_local',
                generation: '1',
              },
            }),
          );
          await assert.rejects(
            client.project.create({
              data: {
                id: 'prj_orphan',
                organizationId: 'org_missing',
                name: 'Orphan',
                slug: 'orphan',
              },
            }),
          );
          const indexes =
            await client.$queryRaw`SELECT indexname,indexdef FROM pg_indexes WHERE schemaname=current_schema()`;
          for (const name of [
            'ApiKey_tokenHash_key',
            'ApiSession_tokenHash_key',
            'TunnelCredential_tokenHash_key',
            'Project_organizationId_slug_key',
            'Tunnel_projectId_slug_key',
            'TunnelCredential_tunnelId_expiresAt_idx',
            'Project_organizationId_id_idx',
            'Tunnel_projectId_id_idx',
          ])
            assert.ok(
              indexes.some((i) => i.indexname === name),
              name,
            );
          const plan = await client.$transaction(async (tx) => {
            await tx.$executeRaw`SET LOCAL enable_seqscan = off`;
            return tx.$queryRaw`EXPLAIN SELECT id FROM "ApiKey" WHERE "tokenHash" = ${digest(fixture.bearer)}`;
          });
          assert.ok(JSON.stringify(plan).includes('ApiKey_tokenHash_key'));
        },
      );
      await t.test(
        'concurrent lease admission is bounded across instances without consuming rejected generations',
        async () => {
          const created = await call('/tunnels', 'POST', {
            projectId: 'prj_local',
            name: 'Bounded',
            slug: 'bounded',
            type: 'PERSISTENT',
            protocol: 'http',
            localHost: '127.0.0.1',
            localPort: 3000,
          });
          assert.equal(created.status, 201);
          const id = created.value.data.tunnel.id;
          const results = await Promise.all(
            Array.from({ length: 20 }, (_, i) =>
              call(
                '/tunnels/' + id + '/connect',
                'POST',
                {},
                fixture.bearer,
                {},
                i % 2 ? app : otherApp,
              ),
            ),
          );
          assert.equal(results.filter((r) => r.status === 200).length, 16);
          assert.equal(
            results.filter(
              (r) =>
                r.status === 503 && r.value.error.code === 'CAPACITY_REACHED',
            ).length,
            4,
          );
          assert.equal(
            (
              await client.tunnel.findUnique({ where: { id } })
            ).generation.toFixed(0),
            '16',
          );
          assert.equal(
            await client.tunnelCredential.count({ where: { tunnelId: id } }),
            16,
          );
          assert.equal(
            await client.auditLog.count({
              where: { action: 'tunnel.connect', resourceId: id },
            }),
            16,
          );
          assert.equal(
            (await call('/tunnels/' + id + '/revoke', 'POST')).status,
            200,
          );
        },
      );
      await t.test(
        'unsigned generation ceiling, persistent revocation and seed reruns survive a fresh backend',
        async () => {
          assert.equal(
            (
              await call('/tunnels/tnl_local_dev/connect', 'POST', {
                minimumGeneration: '18446744073709551615',
              })
            ).status,
            200,
          );
          assert.equal(
            (await call('/tunnels/tnl_local_dev/connect', 'POST')).value.error
              .code,
            'GENERATION_EXHAUSTED',
          );
          await call('/tunnels/tnl_local_dev/revoke', 'POST');
          const key = await client.apiKey.findUnique({
            where: { tokenHash: digest(fixture.bearer) },
          });
          await client.apiKey.update({
            where: { id: key.id },
            data: { revokedAt: new Date() },
          });
          await provision(client, loadSeed(fixture.env.API_SEED_FILE));
          assert.equal(
            (await client.tunnel.findUnique({ where: { id: 'tnl_local_dev' } }))
              .status,
            'REVOKED',
          );
          assert.equal(
            (
              await client.tunnel.findUnique({ where: { id: 'tnl_local_dev' } })
            ).generation.toFixed(0),
            '18446744073709551615',
          );
          const fresh = createApp(
            new PrismaStore(new Database(otherClient), presence),
          );
          const response = await call(
            '/me',
            'GET',
            undefined,
            fixture.bearer,
            {},
            fresh,
          );
          assert.equal(response.value.error.code, 'AUTH_REVOKED');
        },
      );
      await t.test(
        'bounded lock timeout rolls back without secret leakage or partial allocation',
        async () => {
          // Use the foreign key after the local owner was revoked in the preceding test.
          const bearer = 'f'.repeat(43);
          let release, locked;
          const held = new Promise((r) => {
            locked = r;
          });
          const unblock = new Promise((r) => {
            release = r;
          });
          const holding = otherClient.$transaction(
            async (tx) => {
              await tx.$queryRaw`SELECT pg_advisory_xact_lock(1347375700, 10)::text`;
              locked();
              await unblock;
            },
            { timeout: 5000 },
          );
          await held;
          try {
            const start = Date.now();
            const response = await call(
              '/projects',
              'POST',
              { name: 'Locked', slug: 'locked' },
              bearer,
            );
            assert.equal(response.status, 503);
            assert.equal(response.value.error.code, 'STORAGE_UNAVAILABLE');
            assert.ok(Date.now() - start < 2500);
            assert.equal(
              await client.project.count({ where: { slug: 'locked' } }),
              0,
            );
            assert.ok(!JSON.stringify(response).includes(bearer));
          } finally {
            release();
            await holding;
          }
        },
      );
    } finally {
      await presence.close();
      await Promise.all([database.close(), otherDatabase.close()]);
      await fixture.close();
    }
  },
);

test(
  'additive migration preserves baseline records and exact signed-64-bit generations',
  { timeout: 60_000 },
  async () => {
    const fixture = await databaseFixture({
      async beforeDeploy({ name, env }) {
        const baseline = readFileSync(
          join(root, 'prisma/migrations/20261001000000_initial/migration.sql'),
          'utf8',
        );
        const legacy = `
      INSERT INTO "Organization" (id,name,slug,"updatedAt") VALUES ('org_legacy','Legacy','legacy',now());
      INSERT INTO "Project" (id,"organizationId",name,slug,"updatedAt") VALUES ('prj_legacy','org_legacy','Legacy','legacy',now());
      INSERT INTO "Tunnel" (id,"projectId",name,slug,generation,"updatedAt") VALUES ('tnl_legacy','prj_legacy','Legacy','legacy',9223372036854775807,now());
      INSERT INTO "Domain" (id,"tunnelId",hostname,status,"verifiedAt","updatedAt") VALUES ('dom_legacy','tnl_legacy','legacy.example.test','VERIFIED',now(),now());
      INSERT INTO "TunnelCredential" (id,"tunnelId","tokenHash",scope) VALUES ('cred_legacy','tnl_legacy','${'a'.repeat(64)}','connect');
      INSERT INTO "AuditLog" (id,"organizationId",action,"resourceType","resourceId") VALUES ('aud_legacy','org_legacy','tunnel.create','tunnel','tnl_legacy');`;
        await execute(
          'docker',
          [
            'exec',
            name,
            'psql',
            '-U',
            'portway',
            '-d',
            'portway',
            '-v',
            'ON_ERROR_STOP=1',
            '-c',
            baseline + legacy,
          ],
          { timeout: 10_000, maxBuffer: 128 * 1024 },
        );
        const sql = async (text) =>
          execute(
            'docker',
            [
              'exec',
              name,
              'psql',
              '-U',
              'portway',
              '-d',
              'portway',
              '-v',
              'ON_ERROR_STOP=1',
              '-At',
              '-c',
              text,
            ],
            { timeout: 10_000, maxBuffer: 128 * 1024 },
          );
        await sql(`UPDATE "Tunnel" SET generation=-1 WHERE id='tnl_legacy'`);
        const upgrade = readFileSync(
          join(
            root,
            'prisma/migrations/20261001001000_durable_control/migration.sql',
          ),
          'utf8',
        );
        await assert.rejects(sql(upgrade));
        assert.equal(
          (
            await sql(
              `SELECT data_type FROM information_schema.columns WHERE table_name='Tunnel' AND column_name='generation'`,
            )
          ).stdout.trim(),
          'bigint',
        );
        assert.equal(
          (
            await sql(`SELECT to_regclass('"ApiSession"') IS NULL`)
          ).stdout.trim(),
          't',
        );
        await sql(
          `UPDATE "Tunnel" SET generation=9223372036854775807 WHERE id='tnl_legacy'`,
        );
        await execute(
          'pnpm',
          [
            '--filter',
            '@portway/api',
            'exec',
            'prisma',
            'migrate',
            'resolve',
            '--config',
            'prisma.config.ts',
            '--applied',
            '20261001000000_initial',
          ],
          { cwd: root, env, timeout: 20_000, maxBuffer: 128 * 1024 },
        );
      },
    });
    const client = databaseClient(fixture.env.DATABASE_URL);
    const presence = new RedisPresence(fixture.env.REDIS_URL);
    try {
      const tunnel = await client.tunnel.findUnique({
        where: { id: 'tnl_legacy' },
      });
      assert.equal(tunnel.generation.toFixed(0), '9223372036854775807');
      assert.equal(tunnel.localPort, null);
      assert.equal(tunnel.publicHostname, null);
      const domain = await client.domain.findUnique({
        where: { id: 'dom_legacy' },
      });
      assert.equal(domain.hostname, 'legacy.example.test');
      assert.equal(domain.tunnelId, 'tnl_legacy');
      assert.equal(domain.status, 'DISABLED');
      assert.equal(domain.verifiedAt, null);
      assert.equal(domain.verificationHash, null);
      assert.equal(domain.verificationExpiresAt, null);
      const credential = await client.tunnelCredential.findUnique({
        where: { id: 'cred_legacy' },
      });
      assert.equal(credential.apiKeyId, null);
      assert.equal(credential.relayId, null);
      assert.equal(
        await client.auditLog.count({ where: { id: 'aud_legacy' } }),
        1,
      );
      const app = createApp(new PrismaStore(new Database(client), presence));
      const verified = await app.request(
        '/api/v1/internal/credentials/verify',
        {
          method: 'POST',
          headers: {
            Authorization: 'Bearer ' + fixture.relayBearer,
            'Content-Type': 'application/json',
          },
          body: JSON.stringify({
            relayId: 'rel_local',
            tokenHash: 'a'.repeat(64),
          }),
        },
      );
      assert.equal(verified.status, 401);
      assert.equal((await verified.json()).error.code, 'AUTH_INVALID');
    } finally {
      await presence.close();
      await client.$disconnect();
      await fixture.close();
    }
  },
);
