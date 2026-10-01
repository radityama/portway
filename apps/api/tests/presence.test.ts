import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  MemoryPresence,
  PRESENCE_TTL,
  choose,
  effective,
  parseReport,
} from '../src/presence.ts';
import type { Report } from '../src/presence.ts';
import type { Relay } from '../src/models.ts';
const report: Report = {
  instanceId: 'a'.repeat(32),
  status: 'HEALTHY',
  activeConnections: 0,
  activeTunnels: 0,
  retainedTunnels: 0,
  activeStreams: 0,
  maxConnections: 2,
  maxTunnels: 4,
  maxStreams: 2,
};
const relay = (id: string): Relay => ({
  id,
  name: id,
  region: 'local',
  hostname: 'localhost',
  port: 8081,
  protocol: 'tls',
  status: 'HEALTHY',
  lastSeenAt: null,
});
test('report leases fence expired/replaced instances and do not extend duplicate observations', () => {
  let now = 1000;
  const store = new MemoryPresence(() => now);
  const first = store.register('rel_a', report);
  now += 100;
  assert.deepEqual(store.register('rel_a', report), first);
  assert.throws(
    () => store.register('rel_a', { ...report, instanceId: 'b'.repeat(32) }),
    /superseded/,
  );
  const observed = store.report('rel_a', report, first.leaseId, 1);
  now += 100;
  assert.deepEqual(store.report('rel_a', report, first.leaseId, 1), observed);
  assert.throws(
    () =>
      store.report(
        'rel_a',
        { ...report, status: 'DEGRADED' },
        first.leaseId,
        1,
      ),
    /superseded/,
  );
  store.report('rel_a', { ...report, status: 'DRAINING' }, first.leaseId, 2);
  const next = store.register('rel_a', {
    ...report,
    instanceId: 'b'.repeat(32),
  });
  assert.notEqual(next.leaseId, first.leaseId);
  assert.throws(
    () => store.report('rel_a', report, first.leaseId, 3),
    /superseded/,
  );
  now += PRESENCE_TTL;
  assert.equal(store.get(['rel_a']).size, 0);
  assert.throws(
    () =>
      store.report(
        'rel_a',
        { ...report, instanceId: 'b'.repeat(32) },
        next.leaseId,
        1,
      ),
    /expired/,
  );
  assert.notEqual(store.register('rel_a', report).leaseId, next.leaseId);
});
test('selection checks fresh health and all capacities, keeps usable ownership and prefers alternatives', () => {
  const store = new MemoryPresence();
  const relays = ['rel_a', 'rel_b'].map(relay);
  store.register('rel_a', report);
  const b = store.register('rel_b', { ...report, instanceId: 'b'.repeat(32) });
  const select = (
    current: string | null = null,
    avoid?: string,
    reserved = new Map<string, number>(),
  ) =>
    choose(
      relays,
      store.get(relays.map((r) => r.id)),
      reserved,
      current,
      avoid,
    );
  assert.equal(select()?.id, 'rel_a');
  assert.equal(select('rel_b')?.id, 'rel_b');
  assert.equal(select('rel_b', 'rel_b')?.id, 'rel_a');
  store.report(
    'rel_b',
    { ...report, instanceId: 'b'.repeat(32), status: 'DEGRADED' },
    b.leaseId,
    1,
  );
  assert.equal(select('rel_b')?.id, 'rel_a');
  assert.equal(select(null, undefined, new Map([['rel_a', 2]])), undefined);
  assert.equal(
    effective(
      { ...relay('rel_a'), status: 'DRAINING' },
      store.get(['rel_a']).get('rel_a'),
    ).status,
    'DRAINING',
  );
  assert.equal(
    choose(relays, new Map(), new Map(), null, undefined),
    undefined,
  );
});
test('report schemas reject unsafe types, counter inconsistencies and endpoint mutation', () => {
  assert.deepEqual(parseReport({ ...report }), report);
  for (const changes of [
    { maxConnections: 0 },
    { maxConnections: 10001 },
    { activeConnections: 3 },
    { activeTunnels: 1 },
    { activeStreams: 1 },
    { retainedTunnels: 5 },
    { activeConnections: '1' },
    { status: 'OFFLINE' },
    { status: ['HEALTHY'] },
    { hostname: 'attacker.example.test' },
    { instanceId: 'not-an-instance' },
    { maxStreams: 1.5 },
  ])
    assert.throws(() => parseReport({ ...report, ...changes }));
});
