import { mkdir, access } from 'node:fs/promises';
import { createServer } from 'node:net';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { execFile, spawnSync } from 'node:child_process';
import { promisify } from 'node:util';
import { setTimeout as delay } from 'node:timers/promises';
import {
  ensureEnvironment,
  loadEnvironment,
  developmentPorts,
} from './environment.mjs';
import { checkToolchains } from './toolchains.mjs';
import { ProcessSupervisor } from './supervisor.mjs';

const root = fileURLToPath(new URL('../', import.meta.url));
const controller = new AbortController();
const supervisor = new ProcessSupervisor((error) => controller.abort(error));
let interrupted = false;
let dependenciesStarted = false;
const compose = [
  'compose',
  '--env-file',
  join(root, '.env'),
  '-f',
  join(root, 'deploy/docker/docker-compose.yml'),
];

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => {
    interrupted = true;
    controller.abort(new Error('Development shutdown requested'));
    void supervisor.stop();
  });
}

try {
  checkToolchains(root, { docker: true });
  if (ensureEnvironment(root)) console.log('Created .env from .env.example.');
  loadEnvironment(root);
  const ports = developmentPorts(process.env);
  await access(join(root, 'node_modules/.modules.yaml')).catch(() => {
    throw new Error('Dependencies are missing; run make setup first');
  });
  for (const key of ['API_PORT', 'DASHBOARD_PORT', 'RELAY_PORT']) {
    await assertPortAvailable(ports[key], key);
  }
  controller.signal.throwIfAborted();

  await supervisor.run('go', ['run', './cmd/dev-init'], { cwd: root });
  controller.signal.throwIfAborted();

  dependenciesStarted = true;
  await supervisor.run(
    'docker',
    [...compose, 'up', '-d', '--wait', '--wait-timeout', '60'],
    { cwd: root },
  );
  controller.signal.throwIfAborted();
  await mkdir(join(root, '.tmp'), { recursive: true });
  const relayPath = join(
    root,
    '.tmp',
    process.platform === 'win32' ? 'portway-relay.exe' : 'portway-relay',
  );
  const agentPath = join(
    root,
    '.tmp',
    process.platform === 'win32' ? 'portway.exe' : 'portway',
  );
  await supervisor.run('go', ['build', '-o', relayPath, './cmd/relay'], {
    cwd: root,
  });
  await supervisor.run('go', ['build', '-o', agentPath, './cmd/portway'], {
    cwd: root,
  });
  controller.signal.throwIfAborted();

  supervisor.start(relayPath, [], { cwd: root });
  supervisor.start('pnpm', ['--filter', '@portway/api', 'dev'], { cwd: root });
  supervisor.start('pnpm', ['--filter', '@portway/dashboard', 'dev'], {
    cwd: root,
    env: {
      ...process.env,
      PORT: String(ports.DASHBOARD_PORT),
      NEXT_TELEMETRY_DISABLED: '1',
    },
  });

  await waitUntilReady(ports, agentPath, controller.signal);
  console.log(
    `Portway development ready\nAPI       http://127.0.0.1:${ports.API_PORT}/health\nDashboard http://127.0.0.1:${ports.DASHBOARD_PORT}\nRelay     127.0.0.1:${ports.RELAY_PORT} (TLS authenticated, registration verified)`,
  );
  await new Promise((resolve) => {
    if (controller.signal.aborted) resolve();
    else controller.signal.addEventListener('abort', resolve, { once: true });
  });
  if (!interrupted) throw controller.signal.reason;
} catch (error) {
  if (!interrupted) {
    console.error(`Portway development failed: ${error.message}`);
    process.exitCode = 1;
  }
} finally {
  await supervisor.stop();
  if (dependenciesStarted) {
    const result = spawnSync('docker', [...compose, 'down', '--timeout', '5'], {
      cwd: root,
      stdio: 'inherit',
      timeout: 20_000,
    });
    if (result.error || result.status !== 0) {
      console.error(
        'Could not stop Portway dependencies; run make docker-down.',
      );
      process.exitCode = 1;
    }
  }
}

async function assertPortAvailable(port, key) {
  await new Promise((resolve, reject) => {
    const probe = createServer();
    probe.once('error', () => reject(new Error(`${key} is already in use`)));
    probe.listen(port, '127.0.0.1', () => probe.close(resolve));
  });
}

async function waitUntilReady(ports, agentPath, signal) {
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    signal.throwIfAborted();
    try {
      const timeout = AbortSignal.any([signal, AbortSignal.timeout(2_000)]);
      const [api, dashboard] = await Promise.all([
        fetch(`http://127.0.0.1:${ports.API_PORT}/health`, { signal: timeout }),
        fetch(`http://127.0.0.1:${ports.DASHBOARD_PORT}`, { signal: timeout }),
      ]);
      const health = await api.json();
      await dashboard.body?.cancel();
      if (api.ok && health.data?.status === 'ok' && dashboard.ok) {
        await probeRelay(ports.RELAY_PORT, agentPath, signal);
        return;
      }
    } catch {
      signal.throwIfAborted();
    }
    await delay(250, undefined, { signal });
  }
  throw new Error(
    'API, dashboard, or relay did not become ready within 60 seconds',
  );
}

async function probeRelay(port, agentPath, signal) {
  await promisify(execFile)(agentPath, ['register', '--once'], {
    cwd: root,
    signal,
    timeout: 5000,
    maxBuffer: 64_000,
    env: {
      ...process.env,
      PORTWAY_JSON: '1',
      PORTWAY_RELAY_ADDR: `127.0.0.1:${port}`,
      PORTWAY_CONNECT_TIMEOUT: '2s',
      PORTWAY_HANDSHAKE_TIMEOUT: '2s',
      PORTWAY_REGISTRATION_TIMEOUT: '2s',
    },
  });
}
