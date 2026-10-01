import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createApp } from '../src/app.ts';
import { ControlStore, digest } from '../src/store.ts';
import { parseReport } from '../src/presence.ts';
import { parseObject } from '../src/validation.ts';
import { parseObservations, type Observation } from '../src/observations.ts';
import type { ProcessLog } from '../src/metrics.ts';

const owner = 'a'.repeat(43),
  viewer = 'b'.repeat(43),
  foreign = 'c'.repeat(43),
  operator = 'r'.repeat(43);
const report = {
  instanceId: 'a'.repeat(32),
  status: 'HEALTHY' as const,
  activeConnections: 0,
  activeTunnels: 0,
  retainedTunnels: 0,
  activeStreams: 0,
  maxConnections: 128,
  maxTunnels: 1024,
  maxStreams: 32,
};
const observation = (tunnelId = 'tnl_a', generation = '1'): Observation => ({
  tunnelId,
  generation,
  requests: '1',
  errors: '0',
  bytesIn: '5',
  bytesOut: '10',
  activeRequests: 0,
  latencyBuckets: ['0', '0', '1', '1', '1', '1', '1', '1', '1', '1', '1', '1'],
  latencySumSeconds: 0.02,
  logs: [
    {
      id: 'b'.repeat(32),
      timestamp: new Date().toISOString(),
      method: 'GET',
      status: 200,
      durationMs: 20,
      bytesIn: '5',
      bytesOut: '10',
      outcome: 'complete',
    },
  ],
});
function fixture() {
  let now = Date.now();
  const at = new Date(now).toISOString();
  const store = new ControlStore({
    now: () => now,
    seed: {
      users: ['a', 'b', 'c'].map((v) => ({
        id: 'usr_' + v,
        email: v + '@test.example',
        createdAt: at,
        updatedAt: at,
      })),
      organizations: ['a', 'c'].map((v) => ({
        id: 'org_' + v,
        name: v,
        slug: v,
      })),
      memberships: [
        { userId: 'usr_a', organizationId: 'org_a', role: 'OWNER' },
        { userId: 'usr_b', organizationId: 'org_a', role: 'VIEWER' },
        { userId: 'usr_c', organizationId: 'org_c', role: 'OWNER' },
      ],
      apiKeys: [owner, viewer, foreign].map((token, i) => ({
        id: 'key_' + i,
        userId: 'usr_' + ['a', 'b', 'c'][i],
        organizationId: i === 2 ? 'org_c' : 'org_a',
        tokenHash: digest(token),
        expiresAt: new Date(now + 7200000).toISOString(),
      })),
      projects: [
        {
          id: 'prj_a',
          organizationId: 'org_a',
          name: 'A',
          slug: 'a',
          createdAt: at,
          updatedAt: at,
        },
      ],
      relays: [
        {
          id: 'rel_a',
          name: 'Relay',
          region: 'local',
          hostname: 'localhost',
          port: 8081,
          protocol: 'tls',
          status: 'HEALTHY',
          lastSeenAt: null,
        },
      ],
      relayKeys: [
        {
          relayId: 'rel_a',
          tokenHash: digest(operator),
          expiresAt: new Date(now + 7200000).toISOString(),
        },
      ],
    },
  });
  const logs: ProcessLog[] = [];
  const app = createApp(store, (e) => logs.push(e));
  const principal = store.authenticate(owner);
  store.presence.register('rel_a', report);
  const tunnel = store.createTunnel(principal, {
    projectId: 'prj_a',
    name: 'A',
    type: 'PERSISTENT',
    protocol: 'http',
    localHost: '127.0.0.1',
    localPort: 3000,
  }).tunnel;
  const assignment = store.connect(principal, tunnel.id, {});
  const call = async (path: string, bearer = owner) => {
    const r = await app.request('/api/v1' + path, {
      headers: { Authorization: 'Bearer ' + bearer },
    });
    return { status: r.status, value: await r.json() };
  };
  const publish = (o = observation(tunnel.id, assignment.generation)) => {
    const prior = store.presence.get(['rel_a']).get('rel_a');
    return store.relayReport(
      operator,
      'rel_a',
      {
        ...report,
        observations: [o],
        ...(prior
          ? { leaseId: prior.leaseId, sequence: prior.sequence + 1 }
          : {}),
      },
      !!prior,
    );
  };
  return {
    store,
    app,
    call,
    tunnel,
    assignment,
    publish,
    logs,
    advance: (ms: number) => {
      now += ms;
    },
    principal,
  };
}
test('bounded reports validate nested metadata, lossless counters and histogram consistency', () => {
  const good = observation();
  assert.deepEqual(parseObservations([good]), [good]);
  const max = '18446744073709551615';
  const large = {
    ...good,
    requests: max,
    errors: max,
    bytesIn: max,
    latencyBuckets: Array(12).fill(max),
  };
  assert.equal(parseObservations([large])[0]?.requests, max);
  for (const change of [
    { path: '/secret' },
    { requests: 1 },
    { bytesIn: '01' },
    { bytesOut: '18446744073709551616' },
    { generation: '0' },
    { errors: '2' },
    { activeRequests: 10001 },
    { latencySumSeconds: NaN },
    { latencyBuckets: ['1', '0', ...Array(10).fill('1')] },
    { logs: Array(5).fill(good.logs[0]) },
    { logs: [{ ...good.logs[0], headers: { Authorization: 'secret' } }] },
    { logs: [{ ...good.logs[0], method: 'SECRET' }] },
    {
      logs: [
        {
          ...good.logs[0],
          timestamp: new Date(Date.now() + 60000).toISOString(),
        },
      ],
    },
  ])
    assert.throws(() => parseObservations([{ ...good, ...change }]));
  assert.throws(() => parseObservations([good, good]));
  assert.throws(() => parseObservations(Array(33).fill(good)));
  assert.throws(() =>
    parseObject('{"observations":[{"requests":"1","requ\\u0065sts":"2"}]}'),
  );
  assert.deepEqual(parseReport({ ...report }), report);
});
test('scoped observations distinguish absent, stale, superseded, revoked and expired state', async () => {
  const f = fixture();
  const path = '/tunnels/' + f.tunnel.id;
  assert.equal((await f.call(path + '/metrics')).value.data.available, false);
  f.publish();
  const metrics = await f.call(path + '/metrics', viewer);
  assert.equal(metrics.status, 200);
  assert.equal(metrics.value.data.metrics.requests, '1');
  assert.equal(metrics.value.data.available, true);
  assert.equal('logs' in metrics.value.data.metrics, false);
  assert.equal(
    (await f.call(path + '/logs?limit=1', viewer)).value.data.logs.length,
    1,
  );
  for (const suffix of ['/metrics', '/logs'])
    assert.equal((await f.call(path + suffix, foreign)).status, 404);
  for (const query of [
    'limit=5',
    'limit=0',
    'limit=01',
    'limit=1&limit=1',
    'cursor=abc',
    'path=secret',
  ])
    assert.equal((await f.call(path + '/logs?' + query)).status, 400);
  assert.equal((await f.call(path + '/metrics?limit=1')).status, 400);
  f.publish({
    ...observation(f.tunnel.id, f.assignment.generation),
    logs: [
      {
        ...observation().logs[0]!,
        timestamp: new Date(Date.now() - 3601000).toISOString(),
      },
    ],
  });
  assert.deepEqual((await f.call(path + '/logs')).value.data.logs, []);
  f.store.connect(f.principal, f.tunnel.id, {
    minimumGeneration: (BigInt(f.assignment.generation) + 1n).toString(),
  });
  assert.equal((await f.call(path + '/metrics')).value.data.available, false);
  f.publish(
    observation(
      f.tunnel.id,
      f.store.tunnel(f.principal, f.tunnel.id).generation,
    ),
  );
  f.advance(15000);
  assert.equal((await f.call(path + '/logs')).value.data.available, false);
  f.publish(
    observation(
      f.tunnel.id,
      f.store.tunnel(f.principal, f.tunnel.id).generation,
    ),
  );
  f.store.revoke(f.principal, f.tunnel.id);
  assert.equal((await f.call(path + '/metrics')).value.data.available, false);
});
test('operator metrics require a relay key; structured logs and labels omit attacker input', async () => {
  const f = fixture();
  for (const bearer of ['', owner, viewer, foreign]) {
    const r = await f.app.request('/metrics', {
      headers: { Authorization: 'Bearer ' + bearer },
    });
    assert.equal(r.status, 401);
  }
  await f.call('/tunnels/' + f.tunnel.id + '/metrics');
  for (let i = 0; i < 20; i++)
    await f.app.request('/SECRET-' + i + '?token=secret', {
      method: i % 2 ? 'GET' : 'SECRET',
      headers: { Authorization: 'Bearer ' + owner, Cookie: 'secret' },
    });
  const r = await f.app.request('/metrics', {
    headers: { Authorization: 'Bearer ' + operator },
  });
  assert.equal(r.status, 200);
  assert.match(r.headers.get('content-type')!, /text\/plain/);
  const text = await r.text();
  assert.match(text, /portway_api_request_duration_seconds_bucket/);
  assert.match(text, /route="other"/);
  const output = text + JSON.stringify(f.logs);
  for (const secret of [
    'SECRET-',
    'token=secret',
    'Cookie',
    owner,
    operator,
    f.tunnel.id,
  ])
    assert.equal(output.includes(secret), false);
  assert.equal(
    f.logs.some((e) => e.method === 'OTHER'),
    true,
  );
  assert.equal(r.headers.get('cache-control'), 'no-store');
});
