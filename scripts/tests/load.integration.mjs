import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { root, until } from '../testing/control.mjs';
import { platformFixture } from '../testing/platform.mjs';
import { traffic, workload, payloadDigest } from '../testing/traffic.mjs';

const prefix = 'portway_relay_';
async function drained(fixture) {
  await until(async () => {
    const metrics = await fixture.metrics();
    return (
      ['streams_active', 'requests_active', 'public_connections_active'].every(
        (name) => metrics.get(prefix + name) === 0,
      ) && fixture.active === 0
    );
  });
}

test(
  'bounded real-process load: concurrency, streamed body boundaries and reusable capacity',
  { timeout: 120_000 },
  async (t) => {
    const fixture = await platformFixture({ maxStreams: 32 });
    const results = {
      profile: 'phase16-local-smoke',
      runtime: { node: process.version },
      scenarios: [],
    };
    try {
      await drained(fixture);
      const before = await fixture.metrics();
      const generation = (
        await fixture.client.tunnel.findUnique({
          where: { id: 'tnl_local_dev' },
        })
      ).generation.toFixed(0);
      await t.test(
        '256 mixed requests across 12 workers preserve bytes and request identity',
        async () => {
          const sizes = [0, 16 * 1024, 64 * 1024, 256 * 1024];
          const digests = new Map(
            sizes.map((size) => [size, payloadDigest(size)]),
          );
          const summary = await workload(256, 12, async (index) => {
            const size = sizes[index % sizes.length],
              id = 'mixed-' + index;
            if (index % 3 === 0) {
              const result = await traffic(
                fixture.url() + '/bytes?size=' + size,
                fixture.ca,
                { id },
              );
              assert.equal(result.status, 200);
              assert.equal(result.length, size);
              assert.equal(result.digest, digests.get(size));
              return result;
            }
            const result = await traffic(
              fixture.url() + '/upload',
              fixture.ca,
              { method: 'POST', size, chunked: index % 2 === 0, id },
            );
            assert.equal(result.status, 200);
            assert.deepEqual(JSON.parse(result.small), {
              id,
              length: size,
              digest: digests.get(size),
            });
            return result;
          });
          results.scenarios.push({ name: 'mixed', ...summary });
          for (let index = 0; index < 256; index++)
            assert.equal(fixture.hits.get('mixed-' + index), 1);
          await drained(fixture);
        },
      );

      await t.test(
        '16 MiB known/chunked uploads and a 64 MiB response stream without whole-body buffering',
        async () => {
          for (const chunked of [false, true]) {
            const size = 16 * 1024 * 1024,
              id = 'boundary-upload-' + chunked;
            const result = await traffic(
              fixture.url() + '/upload',
              fixture.ca,
              { method: 'POST', size, chunked, id },
            );
            assert.equal(result.status, 200);
            assert.deepEqual(JSON.parse(result.small), {
              id,
              length: size,
              digest: payloadDigest(size),
            });
            results.scenarios.push({
              name: id,
              durationMS: result.durationMS,
              bytes: size,
            });
          }
          const size = 64 * 1024 * 1024;
          const result = await traffic(
            fixture.url() + '/bytes?size=' + size,
            fixture.ca,
            { id: 'boundary-response' },
          );
          assert.equal(result.status, 200);
          assert.equal(result.length, size);
          assert.equal(result.digest, payloadDigest(size));
          assert.equal(result.small, '');
          results.scenarios.push({
            name: 'boundary-response',
            durationMS: result.durationMS,
            bytes: size,
          });
          await drained(fixture);
        },
      );

      await t.test(
        '32 retained streams reject excess work and release every admission slot',
        async () => {
          const pending = Array.from({ length: 32 }, (_, index) =>
            traffic(fixture.url() + '/hold', fixture.ca, {
              id: 'held-' + index,
            }),
          );
          // Observe all outcomes immediately, then join them after releasing holds.
          const settled = Promise.allSettled(pending);
          try {
            await until(() => fixture.holds.size === 32);
            const peak = await fixture.metrics();
            assert.equal(peak.get(prefix + 'streams_active'), 32);
            const rejected = await traffic(
              fixture.url() + '/upload',
              fixture.ca,
              { id: 'excess' },
            );
            assert.equal(rejected.status, 503);
            assert.equal(fixture.hits.has('excess'), false);
            results.capacity = {
              admitted: 32,
              rejectedStatus: rejected.status,
              heapBytesAtCapacity: peak.get(prefix + 'go_heap_bytes'),
            };
          } finally {
            for (let index = 0; index < 32; index++)
              fixture.release('held-' + index);
          }
          for (const result of await settled) {
            assert.equal(result.status, 'fulfilled');
            assert.equal(result.value.status, 200);
            assert.equal(result.value.small, 'released');
          }
          await drained(fixture);
          const result = await traffic(fixture.url() + '/upload', fixture.ca, {
            id: 'capacity-recovered',
          });
          assert.equal(result.status, 200);
        },
      );

      assert.equal(
        results.scenarios.length,
        4,
        'load profile did not complete',
      );
      assert.equal(results.capacity?.admitted, 32);
      await drained(fixture);
      const after = await fixture.metrics();
      assert.ok(fixture.peakActive <= 32);
      assert.equal(fixture.ready(fixture.primary).length, 1);
      assert.equal(
        (
          await fixture.client.tunnel.findUnique({
            where: { id: 'tnl_local_dev' },
          })
        ).generation.toFixed(0),
        generation,
      );
      // Generous resource bounds catch retained workers/unbounded payload storage
      // without turning a local correctness profile into a hardware-dependent SLA.
      assert.ok(after.get(prefix + 'go_heap_bytes') < 128 * 1024 * 1024);
      assert.ok(
        after.get(prefix + 'go_goroutines') <=
          before.get(prefix + 'go_goroutines') + 16,
      );
      results.resources = {
        before: {
          heapBytes: before.get(prefix + 'go_heap_bytes'),
          goroutines: before.get(prefix + 'go_goroutines'),
        },
        after: {
          heapBytes: after.get(prefix + 'go_heap_bytes'),
          goroutines: after.get(prefix + 'go_goroutines'),
        },
        peakUpstreamRequests: fixture.peakActive,
        finalActiveStreams: after.get(prefix + 'streams_active'),
        bytesIn: after.get(prefix + 'bytes_in_total'),
        bytesOut: after.get(prefix + 'bytes_out_total'),
      };
      const directory = join(root, '.tmp/load');
      mkdirSync(directory, { recursive: true });
      writeFileSync(
        join(directory, 'phase16-load.json'),
        JSON.stringify(results, null, 2) + '\n',
      );
      t.diagnostic(JSON.stringify(results));
    } finally {
      await fixture.close();
    }
  },
);
