import type { Server } from 'node:http';
import { serve } from '@hono/node-server';
import { createApp } from './app.ts';
import { env } from './env.ts';
import { ControlStore } from './store.ts';
import { loadSeed } from './seed.ts';

let app: ReturnType<typeof createApp>;
try {
  app = createApp(
    new ControlStore({
      seed: loadSeed(env.seedFile),
      baseDomain: env.publicBaseDomain,
      credentialTTL: env.credentialTTL,
    }),
  );
} catch {
  console.error(JSON.stringify({ event: 'api_configuration_failed' }));
  process.exit(1);
}
const server = serve(
  { fetch: app.fetch, port: env.apiPort, hostname: '127.0.0.1' },
  (info) => {
    console.log(JSON.stringify({ event: 'api_listening', port: info.port }));
  },
) as Server;

server.maxConnections = 128;
server.headersTimeout = 5000;
server.requestTimeout = 10_000;
server.timeout = 10_000;
server.keepAliveTimeout = 5000;
server.maxHeadersCount = 128;

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
