import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { request } from 'node:http';
import { test } from 'node:test';
import { databaseFixture } from '../testing/database.mjs';
import { root, freePort, start, stop, until } from '../testing/control.mjs';
import { databaseClient } from '../../apps/api/src/database.ts';
import { digest } from '../../apps/api/src/store.ts';

test(
  'production API enforces tenant, role, operator and credential policy boundaries',
  { timeout: 60_000 },
  async (t) => {
    const fixture = await databaseFixture();
    const client = databaseClient(fixture.env.DATABASE_URL);
    const foreign = randomBytes(32).toString('base64url');
    const viewer = randomBytes(32).toString('base64url');
    const secrets = [fixture.bearer, fixture.relayBearer, foreign, viewer];
    let api;
    try {
      await client.organization.create({
        data: { id: 'org_foreign', name: 'Foreign', slug: 'foreign' },
      });
      for (const [name, organizationId, role, token] of [
        ['foreign', 'org_foreign', 'OWNER', foreign],
        ['viewer', 'org_local', 'VIEWER', viewer],
      ]) {
        await client.user.create({
          data: { id: 'usr_' + name, email: name + '@example.test' },
        });
        await client.membership.create({
          data: {
            id: 'mem_' + name,
            userId: 'usr_' + name,
            organizationId,
            role,
          },
        });
        await client.apiKey.create({
          data: {
            id: 'key_' + name,
            name,
            userId: 'usr_' + name,
            organizationId,
            tokenHash: digest(token),
            expiresAt: new Date(Date.now() + 3600_000),
          },
        });
      }
      const port = await freePort();
      api = start(process.execPath, [root + 'apps/api/dist/index.js'], {
        ...fixture.env,
        API_PORT: String(port),
      });
      const origin = `http://127.0.0.1:${port}`;
      await until(async () => {
        try {
          return (
            (
              await fetch(origin + '/health', {
                signal: AbortSignal.timeout(1000),
              })
            ).status === 200
          );
        } catch {
          return false;
        }
      });
      const call = async (
        path,
        method = 'GET',
        body,
        bearer = fixture.bearer,
        headers = {},
      ) => {
        const response = await fetch(origin + path, {
          method,
          headers: {
            Authorization: 'Bearer ' + bearer,
            ...(body === undefined
              ? {}
              : { 'Content-Type': 'application/json' }),
            ...headers,
          },
          ...(body === undefined
            ? {}
            : { body: typeof body === 'string' ? body : JSON.stringify(body) }),
          signal: AbortSignal.timeout(5000),
        });
        return {
          status: response.status,
          value: response.status === 204 ? null : await response.json(),
        };
      };
      const user = (path, ...args) => call('/api/v1' + path, ...args);
      const report = {
        instanceId: 'a'.repeat(32),
        status: 'HEALTHY',
        activeConnections: 0,
        activeTunnels: 0,
        retainedTunnels: 0,
        activeStreams: 0,
        maxConnections: 128,
        maxTunnels: 1024,
        maxStreams: 32,
      };
      assert.equal(
        (
          await user(
            '/internal/relays/rel_local/register',
            'POST',
            report,
            fixture.relayBearer,
          )
        ).status,
        200,
      );
      const created = await user('/domains', 'POST', {
        hostname: 'security.example.test',
        tunnelId: 'tnl_local_dev',
      });
      assert.equal(created.status, 201);
      const domainId = created.value.data.domain.id;
      secrets.push(created.value.data.verification.value);

      await t.test(
        'foreign resources and observations remain undisclosed',
        async () => {
          const before = {
            credentials: await client.tunnelCredential.count(),
            audit: await client.auditLog.count(),
            tunnel: await client.tunnel.findUnique({
              where: { id: 'tnl_local_dev' },
            }),
            domain: await client.domain.findUnique({ where: { id: domainId } }),
          };
          for (const [path, method] of [
            ['/projects/prj_local', 'GET'],
            ['/projects/prj_local', 'DELETE'],
            ['/tunnels/tnl_local_dev', 'GET'],
            ['/tunnels/tnl_local_dev', 'DELETE'],
            ['/tunnels/tnl_local_dev/connect', 'POST'],
            ['/tunnels/tnl_local_dev/revoke', 'POST'],
            ['/tunnels/tnl_local_dev/metrics', 'GET'],
            ['/tunnels/tnl_local_dev/logs', 'GET'],
            ['/domains/' + domainId, 'GET'],
            ['/domains/' + domainId, 'DELETE'],
            ['/domains/' + domainId + '/challenge', 'POST'],
            ['/domains/' + domainId + '/verify', 'POST'],
            ['/domains/' + domainId + '/activate', 'POST'],
          ]) {
            const result = await user(path, method, undefined, foreign);
            assert.equal(result.status, 404, method + ' ' + path);
            assert.equal(result.value.data, null);
          }
          for (const collection of ['projects', 'tunnels', 'domains']) {
            const result = await user(
              '/' + collection,
              'GET',
              undefined,
              foreign,
            );
            assert.equal(result.status, 200);
            assert.deepEqual(result.value.data[collection], []);
          }
          assert.equal(
            await client.tunnelCredential.count(),
            before.credentials,
          );
          assert.equal(await client.auditLog.count(), before.audit);
          assert.deepEqual(
            await client.tunnel.findUnique({ where: { id: 'tnl_local_dev' } }),
            before.tunnel,
          );
          assert.deepEqual(
            await client.domain.findUnique({ where: { id: domainId } }),
            before.domain,
          );
        },
      );

      await t.test(
        'viewers read but cannot mutate; user and operator keys stay separate',
        async () => {
          for (const path of [
            '/tunnels/tnl_local_dev',
            '/tunnels/tnl_local_dev/metrics',
            '/tunnels/tnl_local_dev/logs',
            '/domains/' + domainId,
          ])
            assert.equal(
              (await user(path, 'GET', undefined, viewer)).status,
              200,
            );
          for (const [path, method] of [
            ['/tunnels/tnl_local_dev/connect', 'POST'],
            ['/tunnels/tnl_local_dev/revoke', 'POST'],
            ['/tunnels/tnl_local_dev', 'DELETE'],
            ['/domains/' + domainId + '/challenge', 'POST'],
            ['/domains/' + domainId + '/verify', 'POST'],
            ['/domains/' + domainId + '/activate', 'POST'],
            ['/domains/' + domainId, 'DELETE'],
          ])
            assert.equal(
              (await user(path, method, undefined, viewer)).status,
              403,
            );
          assert.equal((await call('/metrics')).status, 401);
          assert.equal(
            (await user('/me', 'GET', undefined, fixture.relayBearer)).status,
            401,
          );
          assert.equal(
            (
              await user('/internal/credentials/verify', 'POST', {
                relayId: 'rel_local',
                tokenHash: '0'.repeat(64),
              })
            ).status,
            401,
          );
          assert.equal(
            (
              await user(
                '/internal/relays/rel_foreign/drain',
                'POST',
                {},
                fixture.relayBearer,
              )
            ).status,
            403,
          );
        },
      );

      const issue = async (bearer = fixture.bearer) => {
        const result = await user(
          '/tunnels/tnl_local_dev/connect',
          'POST',
          {},
          bearer,
        );
        assert.equal(result.status, 200);
        secrets.push(result.value.data.credential.token);
        return result.value.data.credential;
      };
      const verify = (credential) =>
        user(
          '/internal/credentials/verify',
          'POST',
          {
            relayId: 'rel_local',
            tokenHash: digest(credential.token),
          },
          fixture.relayBearer,
        );
      await t.test(
        'strict bodies and slow uploads fail without allocating state',
        async () => {
          const before = await client.project.count();
          for (const [body, status] of [
            ['{"name":"first","name":"second","slug":"ambiguous"}', 400],
            ['{"name":"first","na\\u006de":"second","slug":"ambiguous"}', 400],
            ['{"name":"x","slug":"x","__proto__":{"admin":true}}', 400],
            ['{"name":"body-secret","slug":"secret"} {}', 400],
            ['x'.repeat(64 * 1024 + 1), 413],
          ])
            assert.equal(
              (await user('/projects', 'POST', body)).status,
              status,
            );
          const slowStatus = await new Promise((resolve, reject) => {
            const upload = request(
              origin + '/api/v1/projects',
              {
                method: 'POST',
                headers: {
                  Authorization: 'Bearer ' + fixture.bearer,
                  'Content-Type': 'application/json',
                  'Transfer-Encoding': 'chunked',
                },
              },
              (response) => {
                response.resume();
                response.on('end', () => {
                  upload.destroy();
                  resolve(response.statusCode);
                });
                response.on('error', reject);
              },
            );
            upload.setTimeout(7000, () =>
              upload.destroy(new Error('Slow upload test deadline')),
            );
            upload.on('error', reject);
            upload.write('{');
          });
          assert.equal(slowStatus, 408);
          assert.equal(await client.project.count(), before);
          assert.equal((await user('/me')).status, 200);
          secrets.push('body-secret');
        },
      );

      await t.test(
        'relay AUTH rejects expired, revoked and unsupported membership policy',
        async () => {
          const credential = await issue();
          assert.equal((await verify(credential)).status, 200);
          const membership = await client.membership.findFirstOrThrow({
            where: { userId: 'usr_local', organizationId: 'org_local' },
          });
          // Production CHECK constraints already prevent unsupported roles. Drop
          // this one only in the owned disposable database to model legacy drift
          // and verify that AUTH also fails closed independently of that defense.
          await assert.rejects(
            client.membership.update({
              where: { id: membership.id },
              data: { role: 'SUPERUSER' },
            }),
          );
          await client.$executeRaw`ALTER TABLE "Membership" DROP CONSTRAINT "Membership_role_valid"`;
          try {
            for (const role of ['VIEWER', 'SUPERUSER', '']) {
              await client.membership.update({
                where: { id: membership.id },
                data: { role },
              });
              const result = await verify(credential);
              assert.equal(result.status, 401, 'role=' + role);
              assert.equal(result.value.error.code, 'AUTH_REVOKED');
            }
          } finally {
            await client.membership.update({
              where: { id: membership.id },
              data: { role: membership.role },
            });
            await client.$executeRaw`ALTER TABLE "Membership" ADD CONSTRAINT "Membership_role_valid" CHECK (role IN ('OWNER','ADMIN','MEMBER','VIEWER'))`;
          }
          await client.tunnelCredential.update({
            where: { id: credential.id },
            data: {
              issuedAt: new Date(Date.now() - 2000),
              expiresAt: new Date(Date.now() - 1000),
            },
          });
          assert.equal(
            (await verify(credential)).value.error.code,
            'AUTH_EXPIRED',
          );
          await client.tunnelCredential.update({
            where: { id: credential.id },
            data: { revokedAt: new Date() },
          });
          assert.equal(
            (await verify(credential)).value.error.code,
            'AUTH_REVOKED',
          );
        },
      );

      await t.test(
        'parent logout blocks sessions, their issued leases and idempotent replays',
        async () => {
          const login = await user('/auth/login', 'POST', {
            token: fixture.bearer,
          });
          assert.equal(login.status, 200);
          const session = login.value.data.session.accessToken;
          secrets.push(session);
          const credential = await issue(session);
          assert.equal((await verify(credential)).status, 200);
          const input = { name: 'Replay', slug: 'security-replay' };
          const headers = { 'Idempotency-Key': 'security-project-replay' };
          assert.equal(
            (await user('/projects', 'POST', input, session, headers)).status,
            201,
          );
          assert.equal((await user('/auth/logout', 'POST')).status, 204);
          assert.equal(
            (await user('/me', 'GET', undefined, session)).value.error.code,
            'AUTH_REVOKED',
          );
          assert.equal(
            (await verify(credential)).value.error.code,
            'AUTH_REVOKED',
          );
          assert.equal(
            (await user('/projects', 'POST', input, session, headers)).status,
            401,
          );
          assert.equal(
            await client.project.count({ where: { slug: input.slug } }),
            1,
          );
        },
      );

      await stop(api);
      for (const secret of secrets)
        assert.equal(
          api.output().includes(secret),
          false,
          'secret in API process output',
        );
    } finally {
      await stop(api);
      await client.$disconnect();
      await fixture.close();
    }
  },
);
