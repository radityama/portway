import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  mkdtempSync,
  writeFileSync,
  chmodSync,
  symlinkSync,
  rmSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createApp } from '../src/app.ts';
import { ControlStore, digest } from '../src/store.ts';
import { loadSeed } from '../src/seed.ts';
import type { Seed } from '../src/models.ts';

const owner = 'a'.repeat(43),
  foreign = 'b'.repeat(43),
  viewer = 'c'.repeat(43),
  member = 'd'.repeat(43),
  relayToken = 'r'.repeat(43);
function fixture(options: ConstructorParameters<typeof ControlStore>[0] = {}) {
  let now = Date.now();
  const time = new Date(now).toISOString(),
    expires = new Date(now + 7200_000).toISOString();
  const seed: Seed = {
    users: ['a', 'b', 'c', 'd'].map((v) => ({
      id: 'usr_' + v,
      email: v + '@example.test',
      createdAt: time,
      updatedAt: time,
    })),
    organizations: ['a', 'b'].map((v) => ({
      id: 'org_' + v,
      name: v,
      slug: v,
    })),
    memberships: [
      { userId: 'usr_a', organizationId: 'org_a', role: 'OWNER' },
      { userId: 'usr_b', organizationId: 'org_b', role: 'OWNER' },
      { userId: 'usr_c', organizationId: 'org_a', role: 'VIEWER' },
      { userId: 'usr_d', organizationId: 'org_a', role: 'MEMBER' },
    ],
    apiKeys: [owner, foreign, viewer, member].map((token, i) => ({
      id: 'key_' + i,
      userId: 'usr_' + ['a', 'b', 'c', 'd'][i],
      organizationId: i === 1 ? 'org_b' : 'org_a',
      tokenHash: digest(token),
      expiresAt: expires,
    })),
    projects: ['a', 'b'].map((v) => ({
      id: 'prj_' + v,
      organizationId: 'org_' + v,
      name: v,
      slug: v,
      createdAt: time,
      updatedAt: time,
    })),
    relays: [
      {
        id: 'rel_a',
        name: 'a',
        region: 'local',
        hostname: 'localhost',
        port: 8081,
        protocol: 'tls',
        status: 'HEALTHY',
        lastSeenAt: null,
      },
    ],
    relayKeys: [
      { relayId: 'rel_a', tokenHash: digest(relayToken), expiresAt: expires },
    ],
  };
  const store = new ControlStore({ seed, now: () => now, ...options });
  const app = createApp(store);
  const call = async (
    path: string,
    method = 'GET',
    data?: unknown,
    bearer = owner,
    headers: Record<string, string> = {},
  ) => {
    const r = await app.request('/api/v1' + path, {
      method,
      headers: {
        Authorization: 'Bearer ' + bearer,
        ...(data === undefined ? {} : { 'Content-Type': 'application/json' }),
        ...headers,
      },
      ...(data === undefined
        ? {}
        : { body: typeof data === 'string' ? data : JSON.stringify(data) }),
    });
    const value = r.status === 204 ? null : await r.json();
    return { r, value };
  };
  const create = async () =>
    (
      await call('/tunnels', 'POST', {
        projectId: 'prj_a',
        name: 'Service',
        type: 'EPHEMERAL',
        protocol: 'http',
        localHost: '127.0.0.1',
        localPort: 3000,
      })
    ).value.data.tunnel;
  return {
    store,
    app,
    call,
    create,
    advance: (ms: number) => {
      now += ms;
    },
  };
}
test('API authenticates keys and sessions; logout invalidates dependent credentials without retaining secrets', async () => {
  const { store, call, create } = fixture();
  assert.equal((await call('/me')).value.data.role, 'OWNER');
  assert.equal(
    (await call('/me', 'GET', undefined, 'x'.repeat(43))).r.status,
    401,
  );
  const login = await call('/auth/login', 'POST', { token: owner }, owner, {
    'Idempotency-Key': 'login-key-1',
  });
  const session = login.value.data.session.accessToken;
  assert.equal(login.r.headers.get('cache-control'), 'no-store');
  assert.equal(
    (
      await call('/auth/login', 'POST', { token: owner }, owner, {
        'Idempotency-Key': 'login-key-1',
      })
    ).value.error.code,
    'CREDENTIAL_ALREADY_ISSUED',
  );
  assert.equal((await call('/me', 'GET', undefined, session)).r.status, 200);
  assert.equal(
    (await call('/auth/login', 'POST', { token: session })).r.status,
    401,
  );
  const tunnel = await create();
  const lease = (
    await call(`/tunnels/${tunnel.id}/connect`, 'POST', {}, session)
  ).value.data;
  assert.equal(
    store.verify(relayToken, 'rel_a', digest(lease.credential.token))
      .generation,
    lease.generation,
  );
  assert.equal(
    (await call('/auth/logout', 'POST', undefined, session)).r.status,
    204,
  );
  assert.equal(
    (await call('/me', 'GET', undefined, session)).value.error.code,
    'AUTH_REVOKED',
  );
  assert.throws(
    () => store.verify(relayToken, 'rel_a', digest(lease.credential.token)),
    /revoked/,
  );
  assert.equal((await call('/me')).r.status, 200);
  const another = (await call('/auth/login', 'POST', { token: owner })).value
    .data.session.accessToken;
  assert.equal((await call('/auth/logout', 'POST')).r.status, 204);
  assert.equal((await call('/me', 'GET', undefined, another)).r.status, 401);
  const stored = JSON.stringify({
    keys: [...store.keys],
    sessions: [...store.sessions],
    credentials: [...store.credentials],
    idempotency: [...store.idempotency],
    audit: store.audit,
  });
  for (const secret of [owner, session, another, lease.credential.token])
    assert.ok(!stored.includes(secret));
});
test('all project and tunnel operations enforce organization and role; revoked tunnels stay terminal', async () => {
  const { call, create } = fixture();
  const tunnel = await create();
  for (const [path, method, data] of [
    [`/projects/prj_a`, 'GET', undefined],
    [`/projects/prj_a`, 'DELETE', undefined],
    [`/tunnels/${tunnel.id}`, 'GET', undefined],
    [`/tunnels/${tunnel.id}/connect`, 'POST', {}],
    [`/tunnels/${tunnel.id}/revoke`, 'POST', undefined],
    [`/tunnels/${tunnel.id}/logs`, 'GET', undefined],
  ] as const)
    assert.equal((await call(path, method, data, foreign)).r.status, 404);
  assert.equal(
    (
      await call(
        '/projects',
        'POST',
        { name: 'Private', slug: 'private' },
        viewer,
      )
    ).r.status,
    403,
  );
  assert.equal(
    (await call(`/tunnels/${tunnel.id}/connect`, 'POST', {}, viewer)).r.status,
    403,
  );
  assert.equal(
    (await call('/projects/prj_a', 'DELETE', undefined, member)).r.status,
    403,
  );
  assert.equal(
    (await call('/projects/prj_a', 'DELETE')).value.error.code,
    'PROJECT_NOT_EMPTY',
  );
  assert.equal(
    (await call(`/tunnels/${tunnel.id}/revoke`, 'POST')).r.status,
    200,
  );
  assert.equal(
    (await call(`/tunnels/${tunnel.id}/connect`, 'POST')).value.error.code,
    'TUNNEL_REVOKED',
  );
  assert.equal(
    (await call(`/tunnels/${tunnel.id}`)).value.data.tunnel.status,
    'REVOKED',
  );
});
test('mutations replay metadata safely, reject body conflicts, and never replay issued secrets', async () => {
  const { call, store, create } = fixture();
  const headers = { 'Idempotency-Key': 'create_project_1' };
  const results = await Promise.all(
    Array.from({ length: 8 }, () =>
      call('/projects', 'POST', { name: 'One', slug: 'one' }, owner, headers),
    ),
  );
  assert.ok(
    results.every(
      (r) =>
        r.r.status === 201 &&
        r.value.data.project.id === results[0].value.data.project.id,
    ),
  );
  assert.equal(store.projects.size, 3);
  assert.equal(
    (
      await call(
        '/projects',
        'POST',
        { name: 'Two', slug: 'two' },
        owner,
        headers,
      )
    ).value.error.code,
    'IDEMPOTENCY_CONFLICT',
  );
  const created = results[0].value.data.project;
  await call('/projects/' + created.id, 'DELETE');
  assert.equal(
    (
      await call(
        '/projects',
        'POST',
        { name: 'One', slug: 'one' },
        owner,
        headers,
      )
    ).r.status,
    404,
  );
  const t = await create(),
    key = { 'Idempotency-Key': 'connect-key-1' };
  const a = (await call(`/tunnels/${t.id}/connect`, 'POST', {}, owner, key))
    .value.data;
  assert.equal(
    (await call(`/tunnels/${t.id}/connect`, 'POST', {}, owner, key)).value.error
      .code,
    'CREDENTIAL_ALREADY_ISSUED',
  );
  assert.equal(store.credentials.size, 1);
  assert.equal(store.tunnels.get(t.id)?.generation, a.generation);
  assert.equal(
    (
      await call(
        `/tunnels/${t.id}/connect`,
        'POST',
        { minimumGeneration: '2' },
        owner,
        key,
      )
    ).value.error.code,
    'IDEMPOTENCY_CONFLICT',
  );
});
test('generation allocation preserves uint64, respects minimums and capacity, and is atomic under concurrent connects', async () => {
  const { store, call, create } = fixture();
  const t = await create();
  const results = await Promise.all(
    Array.from({ length: 8 }, () =>
      call(`/tunnels/${t.id}/connect`, 'POST', {
        minimumGeneration: '9007199254740993',
      }),
    ),
  );
  assert.deepEqual(
    results.map((r) => r.value.data.generation),
    Array.from({ length: 8 }, (_, i) =>
      (9007199254740993n + BigInt(i)).toString(),
    ),
  );
  for (let i = 0; i < 8; i++)
    assert.equal(
      (await call(`/tunnels/${t.id}/connect`, 'POST')).r.status,
      200,
    );
  const generation = store.tunnels.get(t.id)!.generation;
  assert.equal((await call(`/tunnels/${t.id}/connect`, 'POST')).r.status, 503);
  assert.equal(store.tunnels.get(t.id)!.generation, generation);
  for (const minimum of [0, 1, '0', '01', '+1', '18446744073709551616', null])
    assert.equal(
      (
        await call(`/tunnels/${t.id}/connect`, 'POST', {
          minimumGeneration: minimum,
        })
      ).r.status,
      400,
    );
  const f = fixture();
  const x = await f.create();
  assert.equal(
    (
      await f.call(`/tunnels/${x.id}/connect`, 'POST', {
        minimumGeneration: '18446744073709551615',
      })
    ).r.status,
    200,
  );
  assert.equal(
    (await f.call(`/tunnels/${x.id}/connect`, 'POST')).value.error.code,
    'GENERATION_EXHAUSTED',
  );
});
test('relay verification enforces hash, relay scope, parent membership, expiration, and revocation', async () => {
  const f = fixture({ credentialTTL: 1000 });
  const t = await f.create();
  const a = (await f.call(`/tunnels/${t.id}/connect`, 'POST')).value.data;
  const payload = { relayId: 'rel_a', tokenHash: digest(a.credential.token) };
  assert.equal(
    (await f.call('/internal/credentials/verify', 'POST', payload, relayToken))
      .value.data.tunnelId,
    t.id,
  );
  assert.equal(
    (await f.call('/internal/credentials/verify', 'POST', payload, owner)).r
      .status,
    401,
  );
  assert.equal(
    (
      await f.call(
        '/internal/credentials/verify',
        'POST',
        { ...payload, relayId: 'rel_other' },
        relayToken,
      )
    ).r.status,
    403,
  );
  assert.equal(
    (
      await f.call(
        '/internal/credentials/verify',
        'POST',
        { ...payload, tokenHash: a.credential.token },
        relayToken,
      )
    ).r.status,
    400,
  );
  f.store.memberships[0]!.role = 'VIEWER';
  assert.throws(
    () => f.store.verify(relayToken, 'rel_a', payload.tokenHash),
    /revoked/,
  );
  f.store.memberships[0]!.role = 'OWNER';
  f.advance(1000);
  assert.throws(
    () => f.store.verify(relayToken, 'rel_a', payload.tokenHash),
    /expired/,
  );
  f.store.sweep();
  assert.equal(f.store.credentials.size, 0);
  const lease = (await f.call(`/tunnels/${t.id}/connect`, 'POST')).value.data;
  await f.call(`/tunnels/${t.id}/revoke`, 'POST');
  assert.throws(
    () => f.store.verify(relayToken, 'rel_a', digest(lease.credential.token)),
    /revoked/,
  );
});
test('pagination cursors bind caller and filters, reject tampering, and preserve complete ordering', async () => {
  const { call } = fixture();
  for (let i = 0; i < 4; i++)
    await call('/projects', 'POST', { name: 'P' + i, slug: 'p' + i });
  const first = await call('/projects?limit=2');
  const cursor = first.value.meta.nextCursor;
  assert.equal(first.value.data.projects.length, 2);
  assert.ok(cursor);
  const second = await call('/projects?limit=2&cursor=' + cursor);
  assert.equal(second.r.status, 200);
  assert.equal(
    (await call('/projects?limit=2&cursor=' + cursor, 'GET', undefined, member))
      .r.status,
    400,
  );
  assert.equal((await call('/tunnels?cursor=' + cursor)).r.status, 400);
  assert.equal((await call('/projects?cursor=' + cursor + '0')).r.status, 400);
  const all = [...first.value.data.projects, ...second.value.data.projects];
  const third = await call(
    '/projects?limit=2&cursor=' + second.value.meta.nextCursor,
  );
  all.push(...third.value.data.projects);
  assert.equal(new Set(all.map((v) => v.id)).size, 5);
  assert.equal(third.value.meta.nextCursor, null);
  for (const query of [
    '?limit=0',
    '?limit=101',
    '?limit=01',
    '?limit=2&limit=3',
    '?unknown=x',
  ])
    assert.equal((await call('/projects' + query)).r.status, 400);
});
test('untrusted JSON rejects duplicates, unknown fields, incorrect types, smuggled bodies, and oversize input', async () => {
  const { call, app, store } = fixture();
  for (const raw of [
    '{"name":"A","name":"B","slug":"a"}',
    '{"name":"A","na\\u006de":"B","slug":"a"}',
    '{"name":"A","slug":"a","extra":1}',
    '{"name":true,"slug":"a"}',
    '{"name":"A","slug":"a"}{}',
    '[]',
    'null',
    '{"name":"A","slug":"a","__proto__":{}}',
  ])
    assert.equal((await call('/projects', 'POST', raw)).r.status, 400);
  assert.equal(store.projects.size, 2);
  assert.equal(
    (await call('/projects', 'POST', 'x'.repeat(65537))).r.status,
    413,
  );
  assert.equal(
    (
      await call('/projects', 'POST', { name: 'A', slug: 'a' }, owner, {
        'Content-Type': 'text/plain',
      })
    ).r.status,
    400,
  );
  assert.equal(
    (
      await app.request('/api/v1/projects', {
        method: 'POST',
        headers: {
          Authorization: 'Bearer ' + owner,
          'Content-Type': 'application/json',
        },
        body: new Uint8Array([0xff]),
      })
    ).status,
    400,
  );
  assert.equal(
    (await call('/projects/prj_a', 'DELETE', { unknown: 1 })).r.status,
    400,
  );
  for (const host of [
    'example.test',
    'localhost',
    '169.254.169.254',
    '0.0.0.0',
  ])
    assert.equal(
      (
        await call('/tunnels', 'POST', {
          projectId: 'prj_a',
          name: 'X',
          type: 'EPHEMERAL',
          protocol: 'http',
          localHost: host,
          localPort: 3000,
        })
      ).r.status,
      400,
    );
});
test('bounded resource admission fails without allocating generations or partial resources; expired entries free capacity', async () => {
  const f = fixture({
    limits: { credentials: 1, sessions: 1, idempotency: 1, audit: 1 },
    credentialTTL: 1000,
  });
  const t = await f.create();
  await f.call(`/tunnels/${t.id}/connect`, 'POST');
  const gen = f.store.tunnels.get(t.id)!.generation;
  assert.equal(
    (await f.call(`/tunnels/${t.id}/connect`, 'POST')).r.status,
    503,
  );
  assert.equal(f.store.tunnels.get(t.id)!.generation, gen);
  f.advance(1000);
  assert.equal(
    (await f.call(`/tunnels/${t.id}/connect`, 'POST')).r.status,
    200,
  );
  assert.equal(f.store.credentials.size, 1);
  assert.equal(f.store.audit.length, 1);
  await f.call('/auth/login', 'POST', { token: owner });
  assert.equal(
    (await f.call('/auth/login', 'POST', { token: owner })).r.status,
    503,
  );
  await f.call('/projects', 'POST', { name: 'First', slug: 'first' }, owner, {
    'Idempotency-Key': 'capacity-key-1',
  });
  const before = f.store.projects.size;
  assert.equal(
    (
      await f.call(
        '/projects',
        'POST',
        { name: 'Second', slug: 'second' },
        owner,
        { 'Idempotency-Key': 'capacity-key-2' },
      )
    ).r.status,
    503,
  );
  assert.equal(f.store.projects.size, before);
  f.store.relays.get('rel_a')!.status = 'DRAINING';
  assert.equal(
    (await f.call(`/tunnels/${t.id}/connect`, 'POST')).value.error.code,
    'RELAY_UNAVAILABLE',
  );
});
test('per-IP admission rate is bounded and resets after its window', async () => {
  const f = fixture();
  for (let i = 0; i < 120; i++)
    assert.equal((await f.app.request('/health')).status, 200);
  assert.equal((await f.app.request('/health')).status, 429);
  f.advance(60_000);
  assert.equal((await f.app.request('/health')).status, 200);
});
test('private seed loader rejects public permissions, symlinks, excessive bytes and invalid JSON without leaking contents', () => {
  const dir = mkdtempSync(join(tmpdir(), 'portway-seed-'));
  try {
    const file = join(dir, 'seed');
    writeFileSync(file, '{"users":[]}', { mode: 0o600 });
    assert.deepEqual(loadSeed(file), { users: [] });
    if (process.platform !== 'win32') {
      chmodSync(file, 0o644);
      assert.throws(
        () => loadSeed(file),
        /Invalid private API seed configuration/,
      );
      chmodSync(file, 0o600);
      symlinkSync(file, join(dir, 'link'));
      assert.throws(() => loadSeed(join(dir, 'link')));
    }
    writeFileSync(file, 'private-secret');
    assert.throws(
      () => loadSeed(file),
      (e) => e instanceof Error && !e.message.includes('private-secret'),
    );
    writeFileSync(file, ' '.repeat(2 * 1024 * 1024 + 1));
    assert.throws(() => loadSeed(file));
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test(
  'slow bodies time out, global concurrency is capped, and cancellation releases admission',
  { timeout: 12_000 },
  async () => {
    const { app } = fixture();
    const { serve } = await import('@hono/node-server');
    const server = serve({ fetch: app.fetch, hostname: '127.0.0.1', port: 0 });
    const keeper = setInterval(() => {}, 1000);
    const pending: Promise<Response>[] = [];
    const controllers: ReadableStreamDefaultController<Uint8Array>[] = [];
    try {
      for (let i = 0; i < 128; i++) {
        const stream = new ReadableStream<Uint8Array>({
          start(c) {
            controllers.push(c);
          },
        });
        const request = new Request('http://localhost/api/v1/projects', {
          method: 'POST',
          headers: {
            Authorization: 'Bearer ' + [owner, foreign, viewer, member][i % 4],
            'Content-Type': 'application/json',
          },
          body: stream,
          duplex: 'half',
        } as RequestInit);
        pending.push(
          app.fetch(request, {
            incoming: { socket: { remoteAddress: 'ip-' + i } },
          }),
        );
      }
      await new Promise((resolve) => setTimeout(resolve, 20));
      assert.equal((await app.request('/health')).status, 503);
      for (const c of controllers) c.close();
      assert.ok((await Promise.all(pending)).every((r) => r.status === 400));
      assert.equal((await app.request('/health')).status, 200);
      const stream = new ReadableStream<Uint8Array>();
      const r = await app.request('/api/v1/projects', {
        method: 'POST',
        headers: {
          Authorization: 'Bearer ' + owner,
          'Content-Type': 'application/json',
        },
        body: stream,
        duplex: 'half',
      } as RequestInit);
      assert.equal(r.status, 408);
    } finally {
      clearInterval(keeper);
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  },
);
