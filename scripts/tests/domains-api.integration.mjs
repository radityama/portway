import assert from 'node:assert/strict';
import { test } from 'node:test';
import { databaseFixture } from '../testing/database.mjs';
import { dnsFixture } from '../testing/dns.mjs';
import { DNSProof } from '../../apps/api/src/domains.ts';
import { Database, databaseClient } from '../../apps/api/src/database.ts';
import { PrismaStore } from '../../apps/api/src/prisma-store.ts';
import { RedisPresence } from '../../apps/api/src/presence.ts';
import { createApp } from '../../apps/api/src/app.ts';

test(
  'real PostgreSQL and DNS domain ownership, atomic reservation, durable restart and alias policy',
  { timeout: 60_000 },
  async (t) => {
    const fixture = await databaseFixture(),
      dns = await dnsFixture(),
      client = databaseClient(fixture.env.DATABASE_URL),
      other = databaseClient(fixture.env.DATABASE_URL);
    const presence = new RedisPresence(fixture.env.REDIS_URL),
      otherPresence = new RedisPresence(fixture.env.REDIS_URL);
    const store = new PrismaStore(new Database(client), presence, {
        dns: new DNSProof(dns.server),
      }),
      second = new PrismaStore(new Database(other), otherPresence, {
        dns: new DNSProof(dns.server),
      });
    const app = createApp(store),
      otherApp = createApp(second);
    const call = async (
      path,
      method = 'GET',
      data,
      bearer = fixture.bearer,
      application = app,
      headers = {},
    ) => {
      const r = await application.request('/api/v1' + path, {
        method,
        headers: {
          Authorization: 'Bearer ' + bearer,
          ...(data === undefined ? {} : { 'Content-Type': 'application/json' }),
          ...headers,
        },
        ...(data === undefined ? {} : { body: JSON.stringify(data) }),
      });
      return {
        status: r.status,
        data: r.status === 204 ? null : await r.json(),
      };
    };
    let created, proof, reportAck;
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
    try {
      await t.test(
        'concurrent cross-instance reservation returns one proof and stores only its hash',
        async () => {
          const input = {
            hostname: 'APP.example.test',
            tunnelId: 'tnl_local_dev',
          };
          const results = await Promise.all([
            call('/domains', 'POST', input),
            call('/domains', 'POST', input, fixture.bearer, otherApp),
          ]);
          assert.deepEqual(results.map((r) => r.status).sort(), [201, 409]);
          created = results.find((r) => r.status === 201).data.data;
          proof = created.verification.value;
          assert.equal(created.domain.hostname, 'app.example.test');
          assert.equal(await client.domain.count(), 1);
          const raw = await client.domain.findUnique({
            where: { id: created.domain.id },
          });
          assert.equal(raw.verificationHash.length, 64);
          assert.equal(JSON.stringify(raw).includes(proof), false);
          assert.equal(
            (await call('/domains/' + created.domain.id + '/activate', 'POST'))
              .status,
            409,
          );
          assert.equal(
            (await call('/domains/' + created.domain.id + '/verify', 'POST'))
              .status,
            409,
          );
        },
      );
      await t.test(
        'real TXT verification is idempotent across instances and survives a fresh backend',
        async () => {
          dns.records.set(created.verification.name, [proof]);
          const results = await Promise.all([
            call('/domains/' + created.domain.id + '/verify', 'POST'),
            call(
              '/domains/' + created.domain.id + '/verify',
              'POST',
              undefined,
              fixture.bearer,
              otherApp,
            ),
          ]);
          assert.ok(results.every((r) => r.status === 200));
          assert.ok(dns.queries >= 3);
          assert.equal(
            await client.auditLog.count({ where: { action: 'domain.verify' } }),
            1,
          );
          const fresh = new PrismaStore(new Database(other), otherPresence, {
            dns: new DNSProof(dns.server),
          });
          const principal = await fresh.authenticate(fixture.bearer);
          assert.equal(
            (await fresh.domain(principal, created.domain.id)).status,
            'VERIFIED',
          );
          assert.equal(
            (await call('/domains/' + created.domain.id + '/activate', 'POST'))
              .data.data.domain.status,
            'ACTIVE',
          );
          assert.equal(
            (await call('/domains/' + created.domain.id)).data.data.domain
              .verificationHash,
            undefined,
          );
          reportAck = (
            await call(
              '/internal/relays/rel_local/register',
              'POST',
              report,
              fixture.relayBearer,
            )
          ).data.data;
          assert.deepEqual(reportAck.routes, []);
          assert.equal(
            (await call('/tunnels/tnl_local_dev/connect', 'POST')).status,
            200,
          );
          const ack = (
            await call(
              '/internal/relays/rel_local/report',
              'POST',
              { ...report, leaseId: reportAck.leaseId, sequence: 1 },
              fixture.relayBearer,
            )
          ).data.data;
          assert.equal(ack.routes.length, 1);
          assert.equal(ack.routes[0].hostname, created.domain.hostname);
          const assignment = await client.tunnel.findUnique({
            where: { id: 'tnl_local_dev' },
          });
          assert.equal(
            ack.routes[0].generation,
            assignment.generation.toFixed(0),
          );
        },
      );
      await t.test(
        'proof rotation and disable remove snapshots; constraints reject partially bound proof',
        async () => {
          const rotated = await call(
            '/domains/' + created.domain.id + '/challenge',
            'POST',
            undefined,
            fixture.bearer,
            app,
            { 'Idempotency-Key': 'domain-rotate-001' },
          );
          assert.equal(rotated.status, 200);
          assert.equal(
            (
              await call(
                '/domains/' + created.domain.id + '/challenge',
                'POST',
                undefined,
                fixture.bearer,
                otherApp,
                { 'Idempotency-Key': 'domain-rotate-001' },
              )
            ).data.error.code,
            'CREDENTIAL_ALREADY_ISSUED',
          );
          assert.equal(
            (await call('/domains/' + created.domain.id + '/verify', 'POST'))
              .status,
            409,
          );
          assert.deepEqual(
            (
              await call(
                '/internal/relays/rel_local/report',
                'POST',
                { ...report, leaseId: reportAck.leaseId, sequence: 2 },
                fixture.relayBearer,
              )
            ).data.data.routes,
            [],
          );
          await assert.rejects(
            client.domain.update({
              where: { id: created.domain.id },
              data: { verificationHash: null },
            }),
          );
          dns.paused = true;
          try {
            const start = Date.now();
            const unavailable = await call(
              '/domains/' + created.domain.id + '/verify',
              'POST',
            );
            assert.equal(unavailable.status, 503);
            assert.equal(unavailable.data.error.code, 'DNS_UNAVAILABLE');
            assert.ok(Date.now() - start < 2500);
            assert.equal(
              (
                await client.domain.findUnique({
                  where: { id: created.domain.id },
                })
              ).status,
              'PENDING_VERIFICATION',
            );
            assert.equal(
              await client.auditLog.count({
                where: { action: 'domain.verify' },
              }),
              1,
            );
          } finally {
            dns.paused = false;
          }
          assert.equal(
            (
              await call(
                '/domains/' + created.domain.id,
                'DELETE',
                undefined,
                fixture.bearer,
                otherApp,
                { 'Idempotency-Key': 'domain-disable-001' },
              )
            ).status,
            204,
          );
          assert.equal(
            (
              await call(
                '/domains/' + created.domain.id,
                'DELETE',
                undefined,
                fixture.bearer,
                app,
                { 'Idempotency-Key': 'domain-disable-001' },
              )
            ).status,
            204,
          );
          assert.equal(
            (
              await client.domain.findUnique({
                where: { id: created.domain.id },
              })
            ).verificationHash,
            null,
          );
          assert.equal(
            (
              await call('/domains', 'POST', {
                hostname: created.domain.hostname,
                tunnelId: 'tnl_local_dev',
              })
            ).status,
            409,
          );
          assert.equal(
            await client.auditLog.count({
              where: { action: 'domain.disable' },
            }),
            1,
          );
        },
      );
    } finally {
      await presence.close();
      await otherPresence.close();
      await client.$disconnect();
      await other.$disconnect();
      await dns.close();
      await fixture.close();
    }
  },
);
