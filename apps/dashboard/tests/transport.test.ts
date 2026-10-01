import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createServer } from 'node:http';
import type { ServerResponse } from 'node:http';
import { setTimeout as delay } from 'node:timers/promises';
import { randomBytes } from 'node:crypto';
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  allowedRoute,
  callAPI,
  clearCookie,
  configuration,
  cookieToken,
  ControlError,
  originCheck,
  readJSON,
  sessionCookie,
} from '../lib/transport.ts';

test('fixed configuration rejects remote HTTP, userinfo, paths, query and unsafe deadlines', () => {
  const env = { DASHBOARD_SESSION_KEY_FILE: '/private/key' };
  assert.equal(configuration(env).api, 'http://127.0.0.1:8080/api/v1');
  for (const input of [
    { DASHBOARD_API_URL: 'http://remote.test/api/v1' },
    { DASHBOARD_API_URL: 'https://secret@api.test/api/v1' },
    { DASHBOARD_API_URL: 'https://api.test/other' },
    { DASHBOARD_API_URL: 'https://api.test/api/v1?secret=value' },
    { DASHBOARD_ORIGIN: 'http://remote.test' },
    { DASHBOARD_ORIGIN: 'https://ui.test/subpath' },
    { DASHBOARD_API_TIMEOUT_MS: '10001' },
    { DASHBOARD_SESSION_KEY_FILE: '' },
  ])
    assert.throws(() => configuration({ ...env, ...input }), ControlError);
});
test('encrypted host-only cookies bind session, origin and expiry; raw parent keys and edits fail', () => {
  const directory = mkdtempSync(join(tmpdir(), 'portway-cookie-'));
  try {
    const key = join(directory, 'key');
    writeFileSync(key, randomBytes(32).toString('base64url'), { mode: 0o600 });
    const config = configuration({
      DASHBOARD_SESSION_KEY_FILE: key,
      DASHBOARD_ORIGIN: 'https://dashboard.test',
    });
    const token = 's'.repeat(43),
      cookie = sessionCookie(
        config,
        token,
        new Date(Date.now() + 60000).toISOString(),
      );
    assert.ok(!cookie.includes(token));
    assert.match(cookie, /^__Host-portway_session=/);
    for (const attribute of ['HttpOnly', 'SameSite=Lax', 'Secure', 'Path=/'])
      assert.ok(cookie.includes(attribute));
    assert.ok(!cookie.includes('Domain='));
    const request = (value: string) =>
      new Request(config.origin, { headers: { Cookie: value } });
    assert.equal(cookieToken(request(cookie), config), token);
    assert.equal(
      cookieToken(request(config.cookie + '=' + token), config),
      null,
    );
    const first = cookie.split(';')[0];
    assert.equal(
      cookieToken(request(first.slice(0, -8) + 'A'.repeat(8)), config),
      null,
    );
    assert.equal(
      cookieToken(request(cookie), { ...config, origin: 'https://other.test' }),
      null,
    );
    assert.equal(cookieToken(request(first + '; ' + first), config), null);
    assert.match(clearCookie(config), /Max-Age=0/);
    assert.throws(
      () =>
        sessionCookie(config, token, new Date(Date.now() - 1000).toISOString()),
      ControlError,
    );
    const publicKey = join(directory, 'public');
    writeFileSync(publicKey, randomBytes(32).toString('base64url'), {
      mode: 0o600,
    });
    chmodSync(publicKey, 0o644);
    if (process.platform !== 'win32')
      assert.throws(
        () =>
          sessionCookie(
            { ...config, keyFile: publicKey },
            token,
            new Date(Date.now() + 60000).toISOString(),
          ),
        ControlError,
      );
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
test('strict origin and metadata allowlist exclude cross-site, internal and credential routes', () => {
  const config = configuration({ DASHBOARD_SESSION_KEY_FILE: '/private/key' });
  const invalidHeaders: Record<string, string>[] = [
    {},
    { Origin: 'https://attacker.test', 'Content-Type': 'application/json' },
    { Origin: config.origin, 'Content-Type': 'text/plain' },
  ];
  for (const headers of invalidHeaders)
    assert.throws(
      () => originCheck(new Request(config.origin, { headers }), config),
      ControlError,
    );
  originCheck(
    new Request(config.origin, {
      headers: { Origin: config.origin, 'Content-Type': 'application/json' },
    }),
    config,
  );
  for (const [path, method] of [
    ['/internal/relays/rel_a/drain', 'POST'],
    ['/tunnels/tnl_a/connect', 'POST'],
    ['/auth/login', 'POST'],
    ['/../me', 'GET'],
    ['//remote.test', 'GET'],
    ['/tunnels/%2fsecret', 'GET'],
  ])
    assert.equal(allowedRoute(path, method, new URLSearchParams()), false);
  assert.ok(
    allowedRoute('/domains/dom_a/verify', 'POST', new URLSearchParams()),
  );
  assert.ok(
    allowedRoute(
      '/tunnels',
      'GET',
      new URLSearchParams('status=CREATED&cursor=abc'),
    ),
  );
  assert.equal(
    allowedRoute('/me', 'GET', new URLSearchParams('cursor=abc')),
    false,
  );
  assert.equal(
    allowedRoute(
      '/tunnels',
      'GET',
      new URLSearchParams('status=CREATED&status=REVOKED'),
    ),
    false,
  );
});
test('scoped observation routes permit only bounded recent-log limits', () => {
  assert.equal(
    allowedRoute('/tunnels/tnl_a/logs', 'GET', new URLSearchParams('limit=4')),
    true,
  );
  for (const query of ['limit=5', 'limit=04', 'cursor=one', 'limit=1&limit=2'])
    assert.equal(
      allowedRoute('/tunnels/tnl_a/logs', 'GET', new URLSearchParams(query)),
      false,
    );
  assert.equal(
    allowedRoute(
      '/tunnels/tnl_a/metrics',
      'GET',
      new URLSearchParams('limit=1'),
    ),
    false,
  );
  assert.equal(allowedRoute('/metrics', 'GET', new URLSearchParams()), false);
});

test('body parsing bounds actual streamed bytes and rejects arrays and invalid UTF-8', async () => {
  await assert.rejects(
    readJSON(
      new Request('http://local', { method: 'POST', body: 'x'.repeat(65) }),
      64,
    ),
    (e: unknown) => e instanceof ControlError && e.status === 413,
  );
  await assert.rejects(readJSON(new Response('[]'), 64), ControlError);
  await assert.rejects(
    readJSON(new Response(new Uint8Array([0xff, 0xff])), 64),
    ControlError,
  );
});
test('bounded API client rejects redirects, oversized replies and slow peers without forwarding secrets', async () => {
  let seen = 0,
    redirected = 0;
  const server = createServer((request, response) => {
    seen++;
    if (request.url === '/api/v1/redirect') {
      response.writeHead(302, { Location: '/other' });
      response.end();
    } else if (request.url === '/other') {
      redirected++;
      response.end();
    } else if (request.url === '/api/v1/large') {
      response.setHeader('Content-Type', 'application/json');
      response.end('x'.repeat(256 * 1024 + 1));
    } else if (request.url === '/api/v1/slow') {
      /* Request remains owned until test cleanup. */
    } else {
      response.setHeader('Content-Type', 'application/json');
      response.end(
        JSON.stringify({ data: { ok: true }, error: null, meta: {} }),
      );
    }
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  assert.ok(address && typeof address !== 'string');
  const config = configuration({
    DASHBOARD_API_URL: `http://127.0.0.1:${address.port}/api/v1`,
    DASHBOARD_API_TIMEOUT_MS: '100',
    DASHBOARD_SESSION_KEY_FILE: '/private/key',
  });
  try {
    assert.equal(
      (await callAPI<{ ok: boolean }>(config, '/me', 't'.repeat(43))).envelope!
        .data.ok,
      true,
    );
    for (const path of ['/redirect', '/large', '/slow']) {
      const start = Date.now();
      await assert.rejects(callAPI(config, path, 't'.repeat(43)), ControlError);
      assert.ok(Date.now() - start < 1500);
    }
    assert.equal(seen, 4);
    assert.equal(redirected, 0);
  } finally {
    server.closeAllConnections();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('32 outstanding API calls bound admission and cancellation returns capacity', async () => {
  const held: ServerResponse[] = [];
  const server = createServer((_, response) => {
    held.push(response);
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  assert.ok(address && typeof address !== 'string');
  const config = configuration({
    DASHBOARD_API_URL: `http://127.0.0.1:${address.port}/api/v1`,
    DASHBOARD_API_TIMEOUT_MS: '3000',
    DASHBOARD_SESSION_KEY_FILE: '/private/key',
  });
  const controllers = Array.from({ length: 32 }, () => new AbortController());
  const calls = controllers.map((controller) =>
    callAPI(config, '/me', 't'.repeat(43), { signal: controller.signal }).catch(
      (error) => error,
    ),
  );
  try {
    const deadline = Date.now() + 2000;
    while (held.length !== 32 && Date.now() < deadline) await delay(10);
    assert.equal(Number(held.length), 32);
    await assert.rejects(
      callAPI(config, '/me', 't'.repeat(43)),
      (error: unknown) =>
        error instanceof ControlError && error.code === 'CAPACITY_REACHED',
    );
    controllers.forEach((controller) => controller.abort());
    assert.ok(
      (await Promise.all(calls)).every(
        (error) => error instanceof ControlError,
      ),
    );
    const next = callAPI(config, '/me', 't'.repeat(43));
    const nextDeadline = Date.now() + 2000;
    while (held.length !== 33 && Date.now() < nextDeadline) await delay(10);
    assert.equal(held.length, 33);
    held[32].setHeader('Content-Type', 'application/json');
    held[32].end(JSON.stringify({ data: { ok: true }, error: null, meta: {} }));
    assert.equal((await next).status, 200);
  } finally {
    controllers.forEach((controller) => controller.abort());
    await Promise.all(calls);
    server.closeAllConnections();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
