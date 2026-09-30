import { serve } from '@hono/node-server';
import { createApp } from './app.js';
import { env } from './env.js';

const server = serve(
  { fetch: createApp().fetch, port: env.apiPort, hostname: '127.0.0.1' },
  (info) => {
    console.log(JSON.stringify({ event: 'api_listening', port: info.port }));
  },
);

let stopping = false;
for (const signal of ['SIGINT', 'SIGTERM'] as const) {
  process.on(signal, () => {
    if (stopping) return;
    stopping = true;
    const deadline = setTimeout(() => process.exit(1), 5_000);
    deadline.unref();
    server.close(() => {
      clearTimeout(deadline);
      process.exit(0);
    });
    if ('closeIdleConnections' in server) server.closeIdleConnections();
  });
}
