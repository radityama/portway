import { Hono } from 'hono';

export function createApp() {
  const app = new Hono();

  app.get('/health', (c) =>
    c.json({
      data: { status: 'ok' },
      error: null,
      meta: {},
    }),
  );

  app.get('/api/v1/me', (c) =>
    c.json(
      {
        data: null,
        error: {
          code: 'NOT_IMPLEMENTED',
          message: 'Authentication is not implemented in the starter.',
        },
        meta: {},
      },
      501,
    ),
  );

  return app;
}
