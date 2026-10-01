import { Hono } from 'hono';
import type { Context } from 'hono';
import type { ContentfulStatusCode } from 'hono/utils/http-status';
import { ControlStore, digest } from './store.ts';
import type { Principal, TunnelStatus } from './models.ts';
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
export function createApp(store = new ControlStore()) {
  const app = new Hono<{ Variables: { principal: Principal } }>();
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
      store.rate('ip:' + (env?.incoming?.socket?.remoteAddress ?? 'local'));
      await next();
    } finally {
      active--;
    }
  });
  app.use('/api/v1/*', async (c, next) => {
    if (
      (c.req.path === '/api/v1/auth/login' && c.req.method === 'POST') ||
      (c.req.path === '/api/v1/internal/credentials/verify' &&
        c.req.method === 'POST')
    ) {
      await next();
      return;
    }
    const p = store.authenticate(bearer(c.req.header('Authorization')));
    store.rate('key:' + p.keyHash);
    c.set('principal', p);
    await next();
  });
  app.get('/health', (c) =>
    c.json({ data: { status: 'ok' }, error: null, meta: {} }),
  );
  app.get('/ready', (c) =>
    store.ready()
      ? c.json({
          data: { status: 'ready', storage: 'memory' },
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
    const p = store.authenticate(rawToken);
    const result = store.mutation(
      p,
      'POST',
      c.req.path,
      b.raw,
      c.req.header('Idempotency-Key'),
      () => ({ status: 200, data: store.login(rawToken) }),
      true,
    );
    return c.json({ data: result.data, error: null, meta: {} });
  });
  app.post('/api/v1/auth/logout', async (c) => {
    query(c.req.url, []);
    const b = await body(c.req.raw, true);
    fields(b.value, []);
    store.logout(c.get('principal'));
    return c.body(null, 204);
  });
  app.get('/api/v1/me', (c) => {
    query(c.req.url, []);
    const p = c.get('principal');
    store.recheck(p);
    return c.json({
      data: {
        user: store.users.get(p.userId),
        organization: store.organizations.get(p.organizationId),
        role: p.role,
      },
      error: null,
      meta: {},
    });
  });
  app.get('/api/v1/projects', (c) => {
    const q = query(c.req.url, ['cursor', 'limit']);
    const page = pagination(q);
    const p = c.get('principal');
    const result = store.page(
      p,
      'projects',
      [...store.projects.values()].filter(
        (v) => v.organizationId === p.organizationId,
      ),
      page.cursor,
      page.limit,
      '',
    );
    return c.json({
      data: { projects: result.items },
      error: null,
      meta: { nextCursor: result.nextCursor },
    });
  });
  app.post('/api/v1/projects', async (c) => {
    query(c.req.url, []);
    const b = await body(c.req.raw);
    const p = c.get('principal');
    store.mutate(p);
    fields(b.value, ['name', 'slug']);
    const result = store.mutation(
      p,
      'POST',
      c.req.path,
      b.raw,
      c.req.header('Idempotency-Key'),
      () => ({ status: 201, data: store.createProject(p, b.value) }),
    );
    return c.json(
      { data: result.data, error: null, meta: {} },
      result.status as ContentfulStatusCode,
    );
  });
  app.get('/api/v1/projects/:id', (c) => {
    query(c.req.url, []);
    return c.json({
      data: {
        project: store.project(c.get('principal'), id(c.req.param('id'))),
      },
      error: null,
      meta: {},
    });
  });
  app.delete('/api/v1/projects/:id', async (c) => {
    query(c.req.url, []);
    const p = c.get('principal');
    const projectId = id(c.req.param('id'));
    store.mutate(p, true);
    const b = await body(c.req.raw, true);
    fields(b.value, []);
    store.mutation(
      p,
      'DELETE',
      c.req.path,
      b.raw,
      c.req.header('Idempotency-Key'),
      () => {
        store.deleteProject(p, projectId);
        return { status: 204, data: null };
      },
      false,
      projectId,
    );
    return c.body(null, 204);
  });
  app.get('/api/v1/tunnels', (c) => {
    const q = query(c.req.url, [
      'cursor',
      'limit',
      'projectId',
      'status',
      'relayId',
    ]);
    const page = pagination(q);
    const p = c.get('principal');
    const projectId = q.get('projectId');
    if (projectId !== null) store.project(p, id(projectId));
    const relayId = q.get('relayId');
    if (relayId !== null) id(relayId);
    const status = q.get('status');
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
    const values = [...store.tunnels.values()].filter(
      (t) =>
        store.projects.get(t.projectId)?.organizationId === p.organizationId &&
        (projectId === null || t.projectId === projectId) &&
        (relayId === null || t.relayId === relayId) &&
        (status === null || t.status === (status as TunnelStatus)),
    );
    const result = store.page(
      p,
      'tunnels',
      values,
      page.cursor,
      page.limit,
      JSON.stringify([projectId, status, relayId]),
    );
    return c.json({
      data: { tunnels: result.items },
      error: null,
      meta: { nextCursor: result.nextCursor },
    });
  });
  app.post('/api/v1/tunnels', async (c) => {
    query(c.req.url, []);
    const b = await body(c.req.raw);
    const p = c.get('principal');
    // Access checks precede idempotency replay as well as a new mutation.
    store.project(p, id(b.value.projectId));
    store.mutate(p);
    const result = store.mutation(
      p,
      'POST',
      c.req.path,
      b.raw,
      c.req.header('Idempotency-Key'),
      () => ({ status: 201, data: store.createTunnel(p, b.value) }),
    );
    return c.json(
      { data: result.data, error: null, meta: {} },
      result.status as ContentfulStatusCode,
    );
  });
  app.get('/api/v1/tunnels/:id', (c) => {
    query(c.req.url, []);
    return c.json({
      data: { tunnel: store.tunnel(c.get('principal'), id(c.req.param('id'))) },
      error: null,
      meta: {},
    });
  });
  for (const suffix of ['', '/revoke']) {
    const handler = async (
      c: Context<{ Variables: { principal: Principal } }>,
    ) => {
      query(c.req.url, []);
      const p = c.get('principal');
      const tunnelId = id(c.req.param('id'));
      store.tunnel(p, tunnelId);
      store.mutate(p);
      const b = await body(c.req.raw, true);
      fields(b.value, []);
      const result = store.mutation(
        p,
        c.req.method,
        c.req.path,
        b.raw,
        c.req.header('Idempotency-Key'),
        () => {
          const data = store.revoke(p, tunnelId);
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
    fields(b.value, ['minimumGeneration']);
    if (b.value.minimumGeneration !== undefined)
      generation(b.value.minimumGeneration);
    const p = c.get('principal');
    const tunnelId = id(c.req.param('id'));
    store.tunnel(p, tunnelId);
    store.mutate(p);
    const result = store.mutation(
      p,
      'POST',
      c.req.path,
      b.raw,
      c.req.header('Idempotency-Key'),
      () => ({ status: 200, data: store.connect(p, tunnelId, b.value) }),
      true,
      tunnelId,
    );
    return c.json({ data: result.data, error: null, meta: {} });
  });
  app.get('/api/v1/relays', (c) => {
    const q = query(c.req.url, ['cursor', 'limit']);
    const page = pagination(q);
    const p = c.get('principal');
    const result = store.page(
      p,
      'relays',
      [...store.relays.values()],
      page.cursor,
      page.limit,
      '',
    );
    return c.json({
      data: { relays: result.items },
      error: null,
      meta: { nextCursor: result.nextCursor },
    });
  });
  app.get('/api/v1/relays/:id', (c) => {
    query(c.req.url, []);
    store.recheck(c.get('principal'));
    return c.json({
      data: { relay: store.relay(id(c.req.param('id'))) },
      error: null,
      meta: {},
    });
  });
  app.post('/api/v1/internal/credentials/verify', async (c) => {
    query(c.req.url, []);
    const relayToken = bearer(c.req.header('Authorization'));
    if (!store.relayKeys.has(digest(relayToken)))
      throw new ApiFailure(401, 'AUTH_INVALID', 'Relay authentication failed');
    const b = await body(c.req.raw);
    fields(b.value, ['relayId', 'tokenHash']);
    return c.json({
      data: store.verify(
        relayToken,
        id(b.value.relayId),
        hash(b.value.tokenHash),
      ),
      error: null,
      meta: {},
    });
  });
  for (const path of [
    '/api/v1/domains',
    '/api/v1/domains/*',
    '/api/v1/tunnels/:id/logs',
    '/api/v1/tunnels/:id/metrics',
  ])
    app.all(path, (c) => {
      if (path.includes(':id'))
        store.tunnel(c.get('principal'), id(c.req.param('id')));
      throw new ApiFailure(
        501,
        'NOT_IMPLEMENTED',
        'Endpoint is not implemented',
      );
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
  app.onError((error, c) => {
    if (error instanceof ApiFailure)
      return c.json(
        {
          data: null,
          error: { code: error.code, message: error.message },
          meta: {},
        },
        error.status,
      );
    return c.json(
      {
        data: null,
        error: { code: 'INTERNAL_ERROR', message: 'Internal server error' },
        meta: {},
      },
      500,
    );
  });
  return app;
}
