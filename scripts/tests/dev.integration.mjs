import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { createServer, createConnection } from 'node:net';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { test } from 'node:test';

const root = fileURLToPath(new URL('../../', import.meta.url));

test(
  'make dev starts healthy services, rejects a duplicate startup, and cleans up on interrupt',
  {
    timeout: 180_000,
    skip:
      process.platform === 'win32'
        ? 'Process-group interruption is verified on POSIX'
        : false,
  },
  async () => {
    const used = new Set();
    const env = { ...process.env, NEXT_TELEMETRY_DISABLED: '1' };
    for (const key of [
      'API_PORT',
      'DASHBOARD_PORT',
      'RELAY_PORT',
      'POSTGRES_PORT',
      'REDIS_PORT',
    ]) {
      let port;
      do {
        port = await freePort();
      } while (used.has(port));
      used.add(port);
      env[key] = String(port);
    }
    env.DATABASE_URL = `postgresql://portway:portway@127.0.0.1:${env.POSTGRES_PORT}/portway?schema=public`;
    env.REDIS_URL = `redis://127.0.0.1:${env.REDIS_PORT}`;
    const child = spawn('make', ['dev'], {
      cwd: root,
      env,
      detached: true,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let output = '';
    const append = (chunk) => {
      output = (output + String(chunk)).slice(-64_000);
    };
    child.stdout.on('data', append);
    child.stderr.on('data', append);
    let closed = false;
    const completion = once(child, 'close').then((result) => {
      closed = true;
      return result;
    });

    try {
      const deadline = Date.now() + 120_000;
      while (!output.includes('Portway development ready')) {
        assert.ok(!closed, `Development exited before readiness:\n${output}`);
        assert.ok(
          Date.now() < deadline,
          `Development did not become ready:\n${output}`,
        );
        await delay(200);
      }
      const health = await fetch(`http://127.0.0.1:${env.API_PORT}/health`);
      assert.deepEqual(await health.json(), {
        data: { status: 'ok' },
        error: null,
        meta: {},
      });
      const dashboard = await fetch(`http://127.0.0.1:${env.DASHBOARD_PORT}`);
      assert.equal(dashboard.status, 200);
      assert.match(await dashboard.text(), /Portway/);
      assert.equal(await reachable(Number(env.RELAY_PORT)), true);

      const duplicate = spawnSync(process.execPath, ['scripts/dev.mjs'], {
        cwd: root,
        env,
        encoding: 'utf8',
        timeout: 30_000,
      });
      assert.equal(duplicate.status, 1);
      assert.match(duplicate.stderr, /API_PORT is already in use/);
      assert.equal(
        (await fetch(`http://127.0.0.1:${env.API_PORT}/health`)).status,
        200,
      );

      process.kill(-child.pid, 'SIGINT');
      await Promise.race([
        completion,
        delay(30_000, undefined, { ref: false }).then(() => {
          throw new Error('Shutdown timed out');
        }),
      ]);
      for (const key of [
        'API_PORT',
        'DASHBOARD_PORT',
        'RELAY_PORT',
        'POSTGRES_PORT',
        'REDIS_PORT',
      ]) {
        assert.equal(
          await reachable(Number(env[key])),
          false,
          `${key} remained open after shutdown`,
        );
      }
      const containers = spawnSync(
        'docker',
        [
          'compose',
          '--env-file',
          '.env',
          '-f',
          'deploy/docker/docker-compose.yml',
          'ps',
          '-q',
        ],
        {
          cwd: root,
          env,
          encoding: 'utf8',
          timeout: 10_000,
        },
      );
      assert.equal(containers.status, 0);
      assert.equal(
        containers.stdout.trim(),
        '',
        'Portway containers remained after shutdown',
      );
    } finally {
      if (!closed) {
        process.kill(-child.pid, 'SIGTERM');
        await Promise.race([
          completion,
          delay(30_000, undefined, { ref: false }),
        ]);
        if (!closed) process.kill(-child.pid, 'SIGKILL');
      }
    }
  },
);

function freePort() {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const port = server.address().port;
      server.close(() => resolve(port));
    });
  });
}

function reachable(port) {
  return new Promise((resolve) => {
    const socket = createConnection({ host: '127.0.0.1', port });
    socket.once('connect', () => {
      socket.destroy();
      resolve(true);
    });
    socket.once('error', () => resolve(false));
    socket.setTimeout(500, () => {
      socket.destroy();
      resolve(false);
    });
  });
}
