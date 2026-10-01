import { Metrics, type ProcessLog } from './metrics.ts';
import { Hono } from 'hono';
import type { Context } from 'hono';
import type { ContentfulStatusCode } from 'hono/utils/http-status';
import { ControlStore } from './store.ts';
import type { ControlBackend } from './backend.ts';
import type { Principal } from './models.ts';
import {
  ApiFailure,
  fields,
  generation,
  hash,
  id,
  invalid,
  parseObject,
  token,
} from './validation.ts';

const MAX_BODY = 64 * 1024;
function bearer(header: string | undefined): string {
  if (!header || !/^Bearer [A-Za-z0-9_-]{32,512}$/.test(header))
    throw new ApiFailure(401, 'AUTH_INVALID', 'Authentication failed');
  return header.slice(7);
}
async function body(
  request: Request,
  optional = false,
): Promise<{ value: Record<string, unknown>; raw: string }> {
  if (
    request.headers.has('content-length') &&
    (!/^\d+$/.test(request.headers.get('content-length')!) ||
      Number(request.headers.get('content-length')) > MAX_BODY)
  )
    throw new ApiFailure(
      413,
      'PAYLOAD_TOO_LARGE',
      'Request body exceeds limit',
    );
  if (!request.body) {
    if (optional) return { value: {}, raw: '' };
    return invalid();
  }
  const jsonType =
    (request.headers.get('content-type') ?? '')
      .split(';')[0]
      ?.trim()
      .toLowerCase() === 'application/json';
  if (!optional && !jsonType) invalid();
  const reader = request.body.getReader();
  const pieces: Uint8Array[] = [];
  let size = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => {
      reject(new ApiFailure(408, 'REQUEST_TIMEOUT', 'Request timed out'));
      void reader.cancel().catch(() => {});
    }, 5000);
    timer.unref?.();
  });
  try {
    while (true) {
      const chunk = await Promise.race([reader.read(), timeout]);
      if (chunk.done) break;
      size += chunk.value.byteLength;
      if (size > MAX_BODY) {
        void reader.cancel().catch(() => {});
        throw new ApiFailure(
          413,
          'PAYLOAD_TOO_LARGE',
          'Request body exceeds limit',
        );
      }
      pieces.push(chunk.value);
    }
    let raw: string;
    try {
      raw = new TextDecoder('utf-8', { fatal: true }).decode(
        Buffer.concat(pieces, size),
      );
    } catch {
      return invalid();
    }
    if (raw === '' && optional) return { value: {}, raw };
    if (!jsonType) invalid();
    return { value: parseObject(raw), raw };
  } finally {
    clearTimeout(timer);
    reader.releaseLock();
  }
}
function query(url: string, names: string[]) {
  const values = new URL(url).searchParams;
  for (const key of values.keys())
    if (!names.includes(key) || values.getAll(key).length !== 1) invalid();
  return values;
}
function pagination(values: URLSearchParams) {
  const raw = values.get('limit') ?? '50';
  if (!/^[1-9][0-9]{0,2}$/.test(raw) || Number(raw) > 100) invalid();
  return { limit: Number(raw), cursor: values.get('cursor') ?? undefined };
}
export function createApp(
  store: ControlBackend = new ControlStore(),
  log?: (event: ProcessLog) => void,
) {
  const metrics = new Metrics(log);
  const app = new Hono<{ Variables: { principal: Principal } }>();
  app.use('*', async (c, next) => {
    const finish = metrics.begin(c.req.method, c.req.path);
    try {
      await next();
    } finally {
      finish(c.res.status);
    }
  });
  let active = 0;
  app.use('*', async (c, next) => {
    c.header('Cache-Control', 'no-store');
    c.header('X-Content-Type-Options', 'nosniff');
    if (active >= 128)
      throw new ApiFailure(
        503,
        'CAPACITY_REACHED',
        'API request capacity reached',
      );
    const declared = c.req.header('Content-Length');
    if (
      declared !== undefined &&
      (!/^\d+$/.test(declared) || Number(declared) > MAX_BODY)
    )
      throw new ApiFailure(
        413,
        'PAYLOAD_TOO_LARGE',
        'Request body exceeds limit',
      );
    active++;
    try {
      const env = c.env as
        { incoming?: { socket?: { remoteAddress?: string } } } | undefined;
      const relayReport = c.req.path.startsWith('/api/v1/internal/relays/');
      store.rate(
        (relayReport ? 'relay-ip:' : 'ip:') +
          (env?.incoming?.socket?.remoteAddress ?? 'local'),
        relayReport ? 4096 : 120,
      );
      await next();
    } finally {
      active--;
    }
  });
  app.use('/api/v1/*', async (c, next) => {
    if (
      (c.req.path === '/api/v1/auth/login' && c.req.method === 'POST') ||
      (c.req.path.startsWith('/api/v1/internal/relays/') &&
        c.req.method === 'POST') ||
      (c.req.path === '/api/v1/internal/credentials/verify' &&
        c.req.method === 'POST')
    ) {
      await next();
      return;
    }
    const p = await store.authenticate(bearer(c.req.header('Authorization')));
    store.rate('key:' + p.keyHash);
    c.set('principal', p);
    await next();
  });
  app.get('/metrics', async (c) => {
    query(c.req.url, []);
    await store.relayAccess(bearer(c.req.header('Authorization')));
    return c.text(metrics.text(), 200, {
      'Content-Type': 'text/plain; version=0.0.4; charset=utf-8',
    });
  });
  app.get('/health', (c) =>
    c.json({ data: { status: 'ok' }, error: null, meta: {} }),
  );
  app.get('/ready', async (c) =>
    (await store.ready())
      ? c.json({
          data: { status: 'ready', storage: store.storage },
          error: null,
          meta: {},
        })
      : c.json(
          {
            data: null,
            error: {
              code: 'NOT_READY',
              message: 'Control plane is not configured',
            },
            meta: {},
          },
          503,
        ),
  );
  app.post('/api/v1/auth/login', async (c) => {
    query(c.req.url, []);
    const b = await body(c.req.raw);
    fields(b.value, ['token']);
    const rawToken = token(b.value.token);
    const p = await store.authenticate(rawToken);
    const result = await store.mutation(
      p,
      'POST',
      c.req.path,
      b.raw,
      c.req.header('Idempotency-Key'),
      async () => ({ status: 200, data: await store.login(rawToken) }),
      true,
    );
    return c.json({ data: result.data, error: null, meta: {} });
  });
  app.post('/api/v1/auth/logout', async (c) => {
    query(c.req.url, []);
    const b = await body(c.req.raw, true);
    fields(b.value, []);
    await store.logout(c.get('principal'));
    return c.body(null, 204);
  });
  app.get('/api/v1/me', async (c) => {
    query(c.req.url, []);
    return c.json({
      data: await store.profile(c.get('principal')),
      error: null,
      meta: {},
    });
  });
  app.get('/api/v1/projects', async (c) => {
    const q = query(c.req.url, ['cursor', 'limit']);
    const p = pagination(q);
    const page = await store.listProjects(
      c.get('principal'),
      p.cursor,
      p.limit,
    );
    return c.json({
      data: { projects: page.items },
      error: null,
      meta: { nextCursor: page.nextCursor },
    });
  });
  app.post('/api/v1/projects', async (c) => {
    query(c.req.url, []);
    const b = await body(c.req.raw);
    const p = c.get('principal');
    await store.mutate(p);
    fields(b.value, ['name', 'slug']);
    const result = await store.mutation(
      p,
      'POST',
      c.req.path,
      b.raw,
      c.req.header('Idempotency-Key'),
      async () => ({
        status: 201,
        data: await store.createProject(p, b.value),
      }),
    );
    return c.json(
      { data: result.data, error: null, meta: {} },
      result.status as ContentfulStatusCode,
    );
  });
  app.get('/api/v1/projects/:id', async (c) => {
    query(c.req.url, []);
    return c.json({
      data: {
        project: await store.project(c.get('principal'), id(c.req.param('id'))),
      },
      error: null,
      meta: {},
    });
  });
  app.delete('/api/v1/projects/:id', async (c) => {
    query(c.req.url, []);
    const p = c.get('principal'),
      projectId = id(c.req.param('id'));
    await store.mutate(p, true);
    const b = await body(c.req.raw, true);
    fields(b.value, []);
    await store.mutation(
      p,
      'DELETE',
      c.req.path,
      b.raw,
      c.req.header('Idempotency-Key'),
      async () => {
        await store.deleteProject(p, projectId);
        return { status: 204, data: null };
      },
      false,
      projectId,
    );
    return c.body(null, 204);
  });
  app.get('/api/v1/tunnels', async (c) => {
    const q = query(c.req.url, [
        'cursor',
        'limit',
        'projectId',
        'status',
        'relayId',
      ]),
      page = pagination(q),
      projectId = q.get('projectId'),
      relayId = q.get('relayId'),
      status = q.get('status');
    if (projectId !== null) id(projectId);
    if (relayId !== null) id(relayId);
    if (
      status !== null &&
      ![
        'CREATED',
        'CONNECTING',
        'CONNECTED',
        'DISCONNECTED',
        'DRAINING',
        'REVOKED',
      ].includes(status)
    )
      invalid();
    const result = await store.listTunnels(
      c.get('principal'),
      { projectId, relayId, status },
      page.cursor,
      page.limit,
    );
    return c.json({
      data: { tunnels: result.items },
      error: null,
      meta: { nextCursor: result.nextCursor },
    });
  });
  app.post('/api/v1/tunnels', async (c) => {
    query(c.req.url, []);
    const b = await body(c.req.raw),
      p = c.get('principal');
    await store.project(p, id(b.value.projectId));
    await store.mutate(p);
    const result = await store.mutation(
      p,
      'POST',
      c.req.path,
      b.raw,
      c.req.header('Idempotency-Key'),
      async () => ({ status: 201, data: await store.createTunnel(p, b.value) }),
    );
    return c.json(
      { data: result.data, error: null, meta: {} },
      result.status as ContentfulStatusCode,
    );
  });
  app.get('/api/v1/tunnels/:id', async (c) => {
    query(c.req.url, []);
    return c.json({
      data: {
        tunnel: await store.tunnel(c.get('principal'), id(c.req.param('id'))),
      },
      error: null,
      meta: {},
    });
  });
  for (const suffix of ['', '/revoke']) {
    const handler = async (
      c: Context<{ Variables: { principal: Principal } }>,
    ) => {
      query(c.req.url, []);
      const p = c.get('principal'),
        tunnelId = id(c.req.param('id'));
      await store.tunnel(p, tunnelId);
      await store.mutate(p);
      const b = await body(c.req.raw, true);
      fields(b.value, []);
      const result = await store.mutation(
        p,
        c.req.method,
        c.req.path,
        b.raw,
        c.req.header('Idempotency-Key'),
        async () => {
          const data = await store.revoke(p, tunnelId);
          return {
            status: suffix === '' ? 204 : 200,
            data: suffix === '' ? null : data,
          };
        },
        false,
        tunnelId,
      );
      return result.status === 204
        ? c.body(null, 204)
        : c.json({ data: result.data, error: null, meta: {} });
    };
    if (suffix === '') app.delete('/api/v1/tunnels/:id', handler);
    else app.post('/api/v1/tunnels/:id/revoke', handler);
  }
  app.post('/api/v1/tunnels/:id/connect', async (c) => {
    query(c.req.url, []);
    const b = await body(c.req.raw, true);
    fields(b.value, ['minimumGeneration', 'avoidRelayId']);
    if (b.value.avoidRelayId !== undefined) id(b.value.avoidRelayId);
    if (b.value.minimumGeneration !== undefined)
      generation(b.value.minimumGeneration);
    const p = c.get('principal'),
      tunnelId = id(c.req.param('id'));
    await store.tunnel(p, tunnelId);
    await store.mutate(p);
    const result = await store.mutation(
      p,
      'POST',
      c.req.path,
      b.raw,
      c.req.header('Idempotency-Key'),
      async () => ({
        status: 200,
        data: await store.connect(p, tunnelId, b.value),
      }),
      true,
      tunnelId,
    );
    return c.json({ data: result.data, error: null, meta: {} });
  });
  app.get('/api/v1/relays', async (c) => {
    const q = query(c.req.url, ['cursor', 'limit']),
      page = pagination(q);
    const result = await store.listRelays(
      c.get('principal'),
      page.cursor,
      page.limit,
    );
    return c.json({
      data: { relays: result.items },
      error: null,
      meta: { nextCursor: result.nextCursor },
    });
  });
  app.get('/api/v1/relays/:id', async (c) => {
    query(c.req.url, []);
    await store.recheck(c.get('principal'));
    return c.json({
      data: { relay: await store.relay(id(c.req.param('id'))) },
      error: null,
      meta: {},
    });
  });
  app.get('/api/v1/domains', async (c) => {
    const q = query(c.req.url, ['cursor', 'limit', 'tunnelId']),
      page = pagination(q),
      tunnelId = q.get('tunnelId');
    if (tunnelId !== null) id(tunnelId);
    const result = await store.listDomains(
      c.get('principal'),
      tunnelId,
      page.cursor,
      page.limit,
    );
    return c.json({
      data: { domains: result.items },
      error: null,
      meta: { nextCursor: result.nextCursor },
    });
  });
  app.get('/api/v1/domains/:id', async (c) => {
    query(c.req.url, []);
    return c.json({
      data: {
        domain: await store.domain(c.get('principal'), id(c.req.param('id'))),
      },
      error: null,
      meta: {},
    });
  });
  app.post('/api/v1/domains', async (c) => {
    query(c.req.url, []);
    const b = await body(c.req.raw),
      p = c.get('principal');
    fields(b.value, ['hostname', 'tunnelId']);
    await store.tunnel(p, id(b.value.tunnelId));
    await store.mutate(p);
    const result = await store.mutation(
      p,
      'POST',
      c.req.path,
      b.raw,
      c.req.header('Idempotency-Key'),
      async () => ({ status: 201, data: await store.createDomain(p, b.value) }),
      true,
    );
    return c.json(
      { data: result.data, error: null, meta: {} },
      result.status as ContentfulStatusCode,
    );
  });
  for (const action of [
    'challenge',
    'verify',
    'activate',
    'disable',
  ] as const) {
    const handler = async (
      c: Context<{ Variables: { principal: Principal } }>,
    ) => {
      query(c.req.url, []);
      const p = c.get('principal'),
        domainId = id(c.req.param('id'));
      await store.domain(p, domainId);
      await store.mutate(p);
      const b = await body(c.req.raw, true);
      fields(b.value, []);
      if (action === 'verify')
        return c.json({
          data: await store.verifyDomain(p, domainId),
          error: null,
          meta: {},
        });
      const result = await store.mutation(
        p,
        c.req.method,
        c.req.path,
        b.raw,
        c.req.header('Idempotency-Key'),
        async () => {
          if (action === 'disable') {
            await store.disableDomain(p, domainId);
            return { status: 204, data: null };
          }
          return {
            status: 200,
            data:
              action === 'challenge'
                ? await store.challengeDomain(p, domainId)
                : await store.activateDomain(p, domainId),
          };
        },
        action === 'challenge',
        domainId,
      );
      return result.status === 204
        ? c.body(null, 204)
        : c.json({ data: result.data, error: null, meta: {} });
    };
    if (action === 'disable') app.delete('/api/v1/domains/:id', handler);
    else app.post('/api/v1/domains/:id/' + action, handler);
  }
  for (const action of ['register', 'report', 'drain', 'activate'] as const) {
    app.post('/api/v1/internal/relays/:relayId/' + action, async (c) => {
      query(c.req.url, []);
      const raw = bearer(c.req.header('Authorization')),
        relayId = id(c.req.param('relayId'));
      await store.relayAccess(raw, relayId);
      store.rate('relay-node:' + relayId, 900);
      const parsed = await body(
        c.req.raw,
        action === 'drain' || action === 'activate',
      );
      if (action === 'drain' || action === 'activate') fields(parsed.value, []);
      const data =
        action === 'drain' || action === 'activate'
          ? await store.relayPolicy(raw, relayId, action === 'drain')
          : await store.relayReport(
              raw,
              relayId,
              parsed.value,
              action === 'report',
            );
      return c.json({ data, error: null, meta: {} });
    });
  }
  app.post('/api/v1/internal/credentials/verify', async (c) => {
    query(c.req.url, []);
    const relayToken = bearer(c.req.header('Authorization'));
    await store.relayAccess(relayToken);
    const b = await body(c.req.raw);
    fields(b.value, ['relayId', 'tokenHash']);
    return c.json({
      data: await store.verify(
        relayToken,
        id(b.value.relayId),
        hash(b.value.tokenHash),
      ),
      error: null,
      meta: {},
    });
  });
  app.get('/api/v1/tunnels/:id/metrics', async (c) => {
    query(c.req.url, []);
    const { available, observedAt, metrics } = await store.observations(
      c.get('principal'),
      id(c.req.param('id')),
    );
    return c.json({
      data: { available, observedAt, metrics },
      error: null,
      meta: {},
    });
  });
  app.get('/api/v1/tunnels/:id/logs', async (c) => {
    query(c.req.url, ['limit']);
    const raw = c.req.query('limit');
    if (raw !== undefined && !/^[1-4]$/.test(raw)) invalid();
    const { available, observedAt, logs } = await store.observations(
      c.get('principal'),
      id(c.req.param('id')),
    );
    return c.json({
      data: { available, observedAt, logs: logs.slice(0, Number(raw ?? 4)) },
      error: null,
      meta: { retentionSeconds: 3600, maxEntries: 4 },
    });
  });
  app.notFound((c) =>
    c.json(
      {
        data: null,
        error: { code: 'NOT_FOUND', message: 'Endpoint not found' },
        meta: {},
      },
      404,
    ),
  );
  app.onError((error, c) =>
    error instanceof ApiFailure
      ? c.json(
          {
            data: null,
            error: { code: error.code, message: error.message },
            meta: {},
          },
          error.status,
        )
      : c.json(
          {
            data: null,
            error: { code: 'INTERNAL_ERROR', message: 'Internal server error' },
            meta: {},
          },
          500,
        ),
  );
  return app;
}
