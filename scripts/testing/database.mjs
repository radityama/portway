import { randomBytes } from 'node:crypto';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { initializeControl } from '../control-init.mjs';
const execute = promisify(execFile);
export const root = fileURLToPath(new URL('../../', import.meta.url));
export async function databaseFixture({ beforeDeploy } = {}) {
  const name = 'portway-database-' + randomBytes(6).toString('hex');
  const directory = mkdtempSync(join(tmpdir(), 'portway-db-'));
  let started = false;
  const redisName = name + '-redis';
  let redisStarted = false;
  try {
    await execute(
      'docker',
      [
        'run',
        '--detach',
        '--rm',
        '--name',
        name,
        '--tmpfs',
        '/var/lib/postgresql/data:rw,size=256m',
        '-e',
        'POSTGRES_USER=portway',
        '-e',
        'POSTGRES_PASSWORD=portway',
        '-e',
        'POSTGRES_DB=portway',
        '-p',
        '127.0.0.1::5432',
        process.env.POSTGRES_IMAGE ?? 'postgres:17-alpine',
      ],
      { timeout: 60_000 },
    );
    started = true;
    const published = (
      await execute('docker', ['port', name, '5432'], { timeout: 5000 })
    ).stdout.trim();
    const port = Number(published.split(':').at(-1));
    const deadline = Date.now() + 30_000;
    while (true) {
      try {
        await execute(
          'docker',
          [
            'exec',
            name,
            'pg_isready',
            '-h',
            '127.0.0.1',
            '-U',
            'portway',
            '-d',
            'portway',
          ],
          { timeout: 1000 },
        );
        break;
      } catch {
        if (Date.now() > deadline)
          throw new Error('Database fixture readiness failed');
        await delay(100);
      }
    }
    await execute(
      'docker',
      [
        'run',
        '--detach',
        '--rm',
        '--name',
        redisName,
        '-p',
        '127.0.0.1::6379',
        process.env.REDIS_IMAGE ?? 'redis:8-alpine',
        'redis-server',
        '--save',
        '',
        '--appendonly',
        'no',
      ],
      { timeout: 60_000 },
    );
    redisStarted = true;
    const redisPublished = (
      await execute('docker', ['port', redisName, '6379'], { timeout: 5000 })
    ).stdout.trim();
    const redisPort = Number(redisPublished.split(':').at(-1));
    const redisDeadline = Date.now() + 10_000;
    while (true) {
      try {
        if (
          (
            await execute('docker', ['exec', redisName, 'redis-cli', 'ping'], {
              timeout: 1000,
            })
          ).stdout.trim() === 'PONG'
        )
          break;
      } catch {
        // The owned Redis container may still be starting; retry within the deadline.
      }
      if (Date.now() > redisDeadline)
        throw new Error('Redis fixture readiness failed');
      await delay(100);
    }
    initializeControl(directory, { RELAY_PORT: '8081' });
    const privateDir = join(directory, '.tmp/dev');
    const env = {
      ...process.env,
      API_STORAGE: 'postgres',
      REDIS_URL: `redis://127.0.0.1:${redisPort}`,
      DATABASE_URL: `postgresql://portway:portway@127.0.0.1:${port}/portway?schema=public`,
      API_SEED_FILE: join(privateDir, 'control-seed.json'),
      PUBLIC_BASE_DOMAIN: 'portway.localhost',
    };
    if (beforeDeploy) await beforeDeploy({ name, env });
    await execute('pnpm', ['db:deploy'], {
      cwd: root,
      env,
      timeout: 30_000,
      maxBuffer: 128 * 1024,
    });
    await execute('pnpm', ['db:seed'], {
      cwd: root,
      env,
      timeout: 20_000,
      maxBuffer: 128 * 1024,
    });
    return {
      name,
      redisName,
      directory,
      privateDir,
      env,
      bearer: readFileSync(join(privateDir, 'api-token'), 'utf8').trim(),
      relayBearer: readFileSync(
        join(privateDir, 'relay-api-token'),
        'utf8',
      ).trim(),
      async close() {
        await execute('docker', ['stop', '--time', '2', redisName], {
          timeout: 10_000,
        }).catch(() => {});
        await execute('docker', ['stop', '--time', '2', name], {
          timeout: 10_000,
        }).catch(() => {});
        rmSync(directory, { recursive: true, force: true });
      },
    };
  } catch (error) {
    if (redisStarted)
      await execute('docker', ['stop', '--time', '2', redisName], {
        timeout: 10_000,
      }).catch(() => {});
    if (started)
      await execute('docker', ['stop', '--time', '2', name], {
        timeout: 10_000,
      }).catch(() => {});
    rmSync(directory, { recursive: true, force: true });
    throw error;
  }
}
