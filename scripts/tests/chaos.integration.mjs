import assert from 'node:assert/strict';
import { test } from 'node:test';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { root, stop, until } from '../testing/control.mjs';
import { platformFixture } from '../testing/platform.mjs';
import { traffic, workload, payloadDigest } from '../testing/traffic.mjs';
const execute = promisify(execFile);

test(
  'real process and network chaos: retransmission, outage isolation, agent crash and reconnect storms',
  { timeout: 180_000 },
  async (t) => {
    const fixture = await platformFixture({ network: true, maxStreams: 16 });
    const results = {
      profile: 'phase16-local-chaos',
      networkBackend: fixture.bridge.backend,
      scenarios: [],
    };
    let primary = fixture.primary;
    const request = (path, options = {}, agent = primary) =>
      traffic(fixture.url(agent) + path, fixture.ca, options);
    const healthy = async (id, agent = primary) => {
      const result = await request('/upload', { id }, agent);
      assert.equal(result.status, 200);
      assert.equal(JSON.parse(result.small).id, id);
      return result;
    };
    const generation = async (id = 'tnl_local_dev') =>
      (
        await fixture.client.tunnel.findUniqueOrThrow({ where: { id } })
      ).generation.toFixed(0);
    const failed = (result) =>
      assert.ok(
        result.status === 'rejected' || result.value.status >= 500,
        'interrupted request unexpectedly succeeded',
      );
    try {
      await t.test(
        'packet loss retransmits intact bytes; delay spikes and a complete loss burst recover',
        async () => {
          const priorGeneration = await generation(),
            priorReady = fixture.ready(primary).length;
          try {
            await fixture.bridge.impair({ delayMS: 5, lossPercent: 0.5 });
            const size = 256 * 1024,
              expected = payloadDigest(size);
            const summary = await workload(16, 4, async (index) => {
              const id = 'loss-' + index;
              const result = await request('/upload', {
                method: 'POST',
                size,
                chunked: index % 2 === 0,
                id,
              });
              assert.equal(result.status, 200);
              assert.deepEqual(JSON.parse(result.small), {
                id,
                length: size,
                digest: expected,
              });
              assert.equal(fixture.hits.get(id), 1);
              return result;
            });
            const loss = await fixture.bridge.stats();
            // Random loss can legitimately drop zero packets in this bounded
            // sample. The complete-loss burst below requires observed drops,
            // making retransmission coverage independent of that random draw.
            results.scenarios.push({
              name: 'packet-loss',
              ...summary,
              ...loss,
            });
            await fixture.bridge.impair({ delayMS: 100 });
            const delayed = await healthy('latency-spike');
            assert.ok(
              delayed.durationMS >= 70,
              'configured latency was not observed',
            );
            results.scenarios.push({
              name: 'latency-spike',
              delayMS: 100,
              observedMS: delayed.durationMS,
            });
            await fixture.bridge.impair({ lossPercent: 100 });
            const blocked = request('/upload', { id: 'loss-burst' });
            const joined = Promise.allSettled([blocked]);
            await delay(300);
            const burst = await fixture.bridge.stats();
            await fixture.bridge.clear();
            const [result] = await joined;
            assert.equal(result.status, 'fulfilled');
            assert.equal(result.value.status, 200);
            assert.ok(burst.droppedPackets > 0);
            assert.equal(fixture.hits.get('loss-burst'), 1);
            results.scenarios.push({
              name: 'complete-loss-burst',
              durationMS: 300,
              ...burst,
              recoveredMS: result.value.durationMS,
            });
          } finally {
            await fixture.bridge.clear().catch(() => {});
          }
          await healthy('network-restored');
          assert.equal(fixture.ready(primary).length, priorReady);
          assert.equal(await generation(), priorGeneration);
        },
      );

      await t.test(
        'API outage leaves concurrent admitted traffic usable',
        async () => {
          const prior = await generation();
          await stop(fixture.api);
          try {
            await assert.rejects(
              fixture.call('/tunnels/tnl_local_dev/connect', 'POST', {}),
            );
            const summary = await workload(64, 8, async (index) => {
              const id = 'api-outage-' + index;
              const result = await request('/upload', {
                id,
                method: 'POST',
                size: 64 * 1024,
              });
              assert.equal(result.status, 200);
              assert.equal(
                JSON.parse(result.small).digest,
                payloadDigest(64 * 1024),
              );
              assert.equal(fixture.hits.get(id), 1);
              return result;
            });
            results.scenarios.push({ name: 'api-outage', ...summary });
            assert.equal(await generation(), prior);
          } finally {
            await fixture.startAPI();
          }
          assert.equal(primary.closed(), false);
          await healthy('api-restored');
        },
      );

      await t.test(
        'Redis outage blocks new assignments while admitted streams continue',
        async () => {
          const prior = await generation();
          const credentials = await fixture.client.tunnelCredential.count();
          await execute('docker', ['pause', fixture.database.redisName], {
            timeout: 5000,
          });
          try {
            const rejected = await fixture.call(
              '/tunnels/tnl_local_dev/connect',
              'POST',
              {},
            );
            assert.equal(rejected.status, 503);
            assert.equal(rejected.value.error.code, 'PRESENCE_UNAVAILABLE');
            const summary = await workload(64, 8, async (index) => {
              const result = await healthy('redis-outage-' + index);
              assert.equal(fixture.hits.get('redis-outage-' + index), 1);
              return result;
            });
            results.scenarios.push({
              name: 'redis-outage',
              rejectedStatus: rejected.status,
              ...summary,
            });
            assert.equal(await generation(), prior);
            assert.equal(
              await fixture.client.tunnelCredential.count(),
              credentials,
            );
          } finally {
            await execute('docker', ['unpause', fixture.database.redisName], {
              timeout: 5000,
            });
          }
          await until(
            async () =>
              (await fixture.call('/relays/rel_local')).value.data.relay
                .status === 'HEALTHY',
          );
          await healthy('redis-restored');
        },
      );

      await t.test(
        'agent crash cancels a received mutation and restart does not replay it',
        async () => {
          const url = fixture.url(primary),
            prior = await generation();
          const interrupted = Promise.allSettled([
            request('/hold', { method: 'POST', id: 'agent-mutation' }),
          ]);
          await until(() => fixture.holds.has('agent-mutation'));
          primary.child.kill('SIGKILL');
          await primary.completion;
          failed((await interrupted)[0]);
          await until(
            async () =>
              (await fixture.metrics()).get('portway_relay_streams_active') ===
                0 && !fixture.holds.has('agent-mutation'),
          );
          const offline = await request('/upload', { id: 'offline-agent' });
          assert.equal(offline.status, 503);
          assert.equal(fixture.hits.has('offline-agent'), false);
          primary = await fixture.startAgent('tnl_local_dev');
          assert.equal(fixture.url(primary), url);
          assert.ok(BigInt(await generation()) > BigInt(prior));
          await healthy('agent-restored');
          assert.equal(fixture.hits.get('agent-mutation'), 1);
          results.scenarios.push({
            name: 'agent-crash',
            mutationHits: 1,
            offlineStatus: offline.status,
            localCancellations: fixture.canceled,
          });
        },
      );

      await t.test(
        'eight agents recover together across two relay crashes with increasing generations and bounded jitter',
        async () => {
          const ids = ['tnl_local_dev'];
          for (let index = 1; index < 8; index++) {
            const result = await fixture.call('/tunnels', 'POST', {
              projectId: 'prj_local',
              name: 'Storm ' + index,
              slug: 'storm-' + index,
              type: 'PERSISTENT',
              protocol: 'http',
              localHost: '127.0.0.1',
              localPort: 3000,
            });
            assert.equal(result.status, 201);
            ids.push(result.value.data.tunnel.id);
          }
          const others = await Promise.allSettled(
            ids.slice(1).map((id) => fixture.startAgent(id)),
          );
          for (const result of others) assert.equal(result.status, 'fulfilled');
          const agents = [primary, ...others.map((result) => result.value)];
          const hosts = agents.map(
            (agent) => new URL(fixture.url(agent)).hostname,
          );
          let activeRelay = fixture.relay;
          let standby = await fixture.startStandby();
          for (let cycle = 0; cycle < 2; cycle++) {
            const beforeReady = agents.map(
              (agent) => fixture.ready(agent).length,
            );
            const beforeGeneration = await Promise.all(ids.map(generation));
            const beforeEvents = agents.map(
              (agent) => fixture.events(agent).length,
            );
            const pending = Promise.allSettled(
              agents.map((agent, index) =>
                request(
                  '/hold',
                  { method: 'POST', id: `storm-${cycle}-${index}` },
                  agent,
                ),
              ),
            );
            await until(() => fixture.holds.size === 8);
            const crashedAt = performance.now();
            activeRelay.child.kill('SIGKILL');
            await activeRelay.completion;
            for (const result of await pending) failed(result);
            await until(
              () =>
                agents.every(
                  (agent, index) =>
                    fixture.ready(agent).length > beforeReady[index],
                ),
              10_000,
            );
            const recoveryMS = performance.now() - crashedAt;
            assert.ok(
              recoveryMS < 10_000,
              'normal-condition reconnect exceeded the PRD recovery target',
            );
            await until(() => fixture.holds.size === 0);
            const jitter = [];
            for (let index = 0; index < agents.length; index++) {
              const agent = agents[index];
              assert.equal(agent.closed(), false);
              assert.equal(new URL(fixture.url(agent)).hostname, hosts[index]);
              assert.ok(
                BigInt(await generation(ids[index])) >
                  BigInt(beforeGeneration[index]),
              );
              await healthy(`storm-restored-${cycle}-${index}`, agent);
              assert.equal(fixture.hits.get(`storm-${cycle}-${index}`), 1);
              const assignments = fixture
                .events(agent)
                .filter((event) => event.event === 'relay_assigned')
                .map((event) => BigInt(event.generation));
              assert.ok(
                assignments.every(
                  (value, at) => at === 0 || value > assignments[at - 1],
                ),
              );
              const retries = fixture
                .events(agent)
                .slice(beforeEvents[index])
                .filter((event) => event.event === 'reconnect_scheduled');
              assert.ok(retries.length >= 1 && retries.length <= 4);
              for (const retry of retries) {
                const base =
                  retry.attempt <= 5 ? 1000 * 2 ** (retry.attempt - 1) : 30_000;
                assert.ok(retry.delay_ms >= base / 2 && retry.delay_ms <= base);
                jitter.push(retry.delay_ms);
              }
            }
            const metrics = await fixture.metrics(standby);
            assert.equal(metrics.get('portway_relay_connections_active'), 8);
            await until(
              async () =>
                (await fixture.metrics(standby)).get(
                  'portway_relay_streams_active',
                ) === 0,
            );
            results.scenarios.push({
              name: 'reconnect-storm',
              cycle,
              agents: 8,
              recoveryMS,
              jitterMS: jitter,
              finalStreams: 0,
              mutationHits: 8,
            });
            activeRelay = standby;
            if (cycle === 0) {
              // Incarnation fencing prevents immediate replacement of the
              // crashed ID until its 15-second presence lease expires. Keep
              // serving on the standby while that independent fence expires.
              await assert.rejects(
                fixture.startRelay(),
                /relay_reporting_rejected/,
              );
              await until(async () => {
                // Respect the user API quota while waiting for a lease TTL.
                await delay(500);
                const response = await fixture.call('/relays/rel_local');
                assert.equal(response.status, 200);
                return response.value.data.relay.status === 'OFFLINE';
              }, 20_000);
              standby = await fixture.startRelay();
              await healthy('standby-restored-without-interruption');
            }
          }
        },
      );

      assert.equal(
        results.scenarios.length,
        8,
        'chaos profile did not complete',
      );
      for (const process of fixture.processes)
        for (const secret of fixture.secrets)
          assert.equal(
            process.output().includes(secret),
            false,
            'credential appeared in process output',
          );
      const directory = join(root, '.tmp/load');
      mkdirSync(directory, { recursive: true });
      writeFileSync(
        join(directory, 'phase16-chaos.json'),
        JSON.stringify(results, null, 2) + '\n',
      );
      t.diagnostic(JSON.stringify(results));
    } finally {
      await fixture.close();
    }
  },
);
