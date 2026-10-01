import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer as tcpServer } from 'node:net';
import { createServer } from 'node:http';
import { request } from 'node:https';
import { setTimeout as delay } from 'node:timers/promises';
import { initializeControl } from '../control-init.mjs';
import { databaseClient } from '../../apps/api/src/database.ts';

export const root = fileURLToPath(new URL('../../', import.meta.url));
export async function freePort() {
  const s = tcpServer();
  await new Promise((r) => s.listen(0, '127.0.0.1', r));
  const p = s.address().port;
  await new Promise((r) => s.close(r));
  return p;
}
export function start(command, args, env) {
  const child = spawn(command, args, {
    cwd: root,
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '';
  let closed = false;
  child.stdout.on('data', (b) => {
    output = (output + b).slice(-65536);
  });
  child.stderr.on('data', (b) => {
    output = (output + b).slice(-65536);
  });
  const completion = once(child, 'close').then((result) => {
    closed = true;
    return result;
  });
  return { child, completion, output: () => output, closed: () => closed };
}
export async function stop(process) {
  if (!process || process.closed()) return;
  process.child.kill('SIGTERM');
  const deadline = setTimeout(() => process.child.kill('SIGKILL'), 6000);
  try {
    await process.completion;
  } finally {
    clearTimeout(deadline);
  }
}
export async function until(predicate, ms = 12_000) {
  const deadline = Date.now() + ms;
  while (!(await predicate())) {
    assert.ok(Date.now() < deadline, 'Integration readiness deadline exceeded');
    await delay(30);
  }
}
export function publicGet(url, ca, onData = () => {}) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const req = request(
      {
        hostname: '127.0.0.1',
        port: u.port,
        servername: u.hostname,
        path: u.pathname,
        headers: { Host: u.host },
        ca,
        minVersion: 'TLSv1.3',
        timeout: 5000,
      },
      (res) => {
        const pieces = [];
        let size = 0;
        res.on('data', (b) => {
          onData(b);
          size += b.length;
          if (size > 65536) req.destroy(new Error('Response limit'));
          else pieces.push(b);
        });
        res.on('end', () =>
          resolve({
            status: res.statusCode,
            body: Buffer.concat(pieces).toString(),
          }),
        );
        res.on('error', reject);
      },
    );
    req.on('timeout', () => req.destroy(new Error('Request timeout')));
    req.on('error', reject);
    req.end();
  });
}
export async function runControlScenario(database) {
  const fixture =
    database?.directory ?? mkdtempSync(join(tmpdir(), 'portway-control-e2e-'));
  const processes = [];
  let upstream;
  try {
    const [apiPort, relayPort, publicPort] = await Promise.all([
      freePort(),
      freePort(),
      freePort(),
    ]);
    assert.equal(new Set([apiPort, relayPort, publicPort]).size, 3);
    if (database) {
      const client = databaseClient(database.env.DATABASE_URL);
      try {
        await client.relay.update({
          where: { id: 'rel_local' },
          data: { port: relayPort },
        });
      } finally {
        await client.$disconnect();
      }
    } else initializeControl(fixture, { RELAY_PORT: String(relayPort) });
    const privateDir = join(fixture, '.tmp/dev');
    execFileSync('go', ['run', './cmd/dev-init', '-dir', privateDir], {
      cwd: root,
      timeout: 30_000,
      stdio: 'pipe',
    });
    const base = `http://127.0.0.1:${apiPort}/api/v1`;
    const bearer = readFileSync(join(privateDir, 'api-token'), 'utf8').trim();
    const env = {
      ...process.env,
      ...database?.env,
      API_PORT: String(apiPort),
      API_STORAGE: database ? 'postgres' : 'memory',
      API_SEED_FILE: join(privateDir, 'control-seed.json'),
      API_CREDENTIAL_TTL_SECONDS: '3',
      PUBLIC_BASE_DOMAIN: 'portway.localhost',
      RELAY_PORT: String(relayPort),
      PUBLIC_PORT: String(publicPort),
      RELAY_TLS_CERT_FILE: join(privateDir, 'relay-cert.pem'),
      RELAY_TLS_KEY_FILE: join(privateDir, 'relay-key.pem'),
      PUBLIC_TLS_CERT_FILE: join(privateDir, 'public-cert.pem'),
      PUBLIC_TLS_KEY_FILE: join(privateDir, 'public-key.pem'),
      RELAY_API_URL: base,
      RELAY_API_TOKEN_FILE: join(privateDir, 'relay-api-token'),
      RELAY_ID: 'rel_local',
      PORTWAY_JSON: '1',
      PORTWAY_API_URL: base,
      PORTWAY_API_TOKEN_FILE: join(privateDir, 'api-token'),
      PORTWAY_RELAY_CA_FILE: join(privateDir, 'ca.pem'),
      PORTWAY_STATE_DIR: join(fixture, 'agent-state'),
    };
    delete env.PORTWAY_GENERATION;
    let api = start(process.execPath, ['apps/api/dist/index.js'], env);
    processes.push(api);
    await until(async () => {
      try {
        return (await fetch(base.replace('/api/v1', '/health'))).status === 200;
      } catch {
        return false;
      }
    });
    const relay = start(join(root, 'bin/portway-relay'), [], env);
    processes.push(relay);
    await until(() => relay.output().includes('relay_presence_registered'));
    await until(
      async () =>
        (await fetch(base.replace('/api/v1', '/ready'))).status === 200,
    );
    let upstreamHits = 0;
    upstream = createServer((req, res) => {
      upstreamHits++;
      if (req.url === '/events') {
        res.writeHead(200, { 'Content-Type': 'text/event-stream' });
        res.flushHeaders();
        res.write('data: first\n\n');
        const timer = setTimeout(() => res.end('data: last\n\n'), 700);
        req.on('close', () => clearTimeout(timer));
      } else {
        res.end('local-response');
      }
    });
    await new Promise((r) => upstream.listen(0, '127.0.0.1', r));
    const cli = start(
      join(root, 'bin/portway'),
      [String(upstream.address().port)],
      env,
    );
    processes.push(cli);
    await until(() => cli.output().includes('"event":"ready"'));
    assert.equal(cli.closed(), false);
    const ready = () =>
      cli
        .output()
        .trim()
        .split('\n')
        .map((line) => JSON.parse(line))
        .filter((e) => e.event === 'ready');
    const publicURL = ready()[0].public_url;
    const ca = readFileSync(join(privateDir, 'public-ca.pem'));
    let session;
    if (database) {
      const login = await fetch(base + '/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: bearer }),
      });
      assert.equal(login.status, 200);
      session = (await login.json()).data.session.accessToken;
    }
    await stop(api);
    assert.deepEqual(await publicGet(publicURL + '/', ca), {
      status: 200,
      body: 'local-response',
    });
    assert.deepEqual(await publicGet(publicURL + '/events', ca), {
      status: 200,
      body: 'data: first\n\ndata: last\n\n',
    });
    await until(() => cli.output().includes('control_unavailable'));
    assert.equal(cli.closed(), false);
    assert.equal(upstreamHits, 2);
    api = start(process.execPath, ['apps/api/dist/index.js'], env);
    processes.push(api);
    await until(() => ready().length >= 2);
    assert.deepEqual(await publicGet(publicURL + '/', ca), {
      status: 200,
      body: 'local-response',
    });
    if (database) {
      // An actual API process restart must preserve the login session and policy.
      assert.equal(
        (
          await fetch(base + '/me', {
            headers: { Authorization: 'Bearer ' + session },
          })
        ).status,
        200,
      );
      const before = ready().length;
      execFileSync('docker', ['pause', database.name], {
        timeout: 5000,
        stdio: 'pipe',
      });
      try {
        assert.deepEqual(await publicGet(publicURL + '/', ca), {
          status: 200,
          body: 'local-response',
        });
        assert.deepEqual(await publicGet(publicURL + '/events', ca), {
          status: 200,
          body: 'data: first\n\ndata: last\n\n',
        });
        const start = Date.now();
        const failed = await fetch(base + '/me', {
          headers: { Authorization: 'Bearer ' + session },
          signal: AbortSignal.timeout(5000),
        });
        assert.equal(failed.status, 503);
        assert.equal((await failed.json()).error.code, 'STORAGE_UNAVAILABLE');
        assert.ok(Date.now() - start < 4500);
        assert.equal(
          (await fetch(base.replace('/api/v1', '/health'))).status,
          200,
        );
      } finally {
        execFileSync('docker', ['unpause', database.name], {
          timeout: 5000,
          stdio: 'pipe',
        });
      }
      await until(() => ready().length > before);
    }
    const events = cli
      .output()
      .trim()
      .split('\n')
      .map((line) => JSON.parse(line));
    const generations = events
      .filter((e) => e.event === 'relay_assigned')
      .map((e) => BigInt(e.generation));
    assert.ok(generations[1] > generations[0]);
    const revoke = await fetch(base + '/tunnels/tnl_local_dev/revoke', {
      method: 'POST',
      headers: { Authorization: 'Bearer ' + bearer },
    });
    assert.equal(revoke.status, 200);
    await until(() => cli.closed(), 12_000);
    const [code] = await cli.completion;
    assert.equal(code, 1);
    assert.ok(cli.output().includes('CONTROL_REJECTED'));
    assert.equal(upstreamHits, database ? 5 : 3);
    if (database) {
      await stop(api);
      api = start(process.execPath, ['apps/api/dist/index.js'], env);
      processes.push(api);
      await until(async () => {
        try {
          return (
            (await fetch(base.replace('/api/v1', '/ready'))).status === 200
          );
        } catch {
          return false;
        }
      });
      const retry = await fetch(base + '/tunnels/tnl_local_dev/connect', {
        method: 'POST',
        headers: { Authorization: 'Bearer ' + bearer },
      });
      assert.equal(retry.status, 409);
      assert.equal((await retry.json()).error.code, 'TUNNEL_REVOKED');
      assert.ok(!api.output().includes(session));
    }
    assert.ok(!cli.output().includes(bearer));
    assert.ok(
      !relay
        .output()
        .includes(
          readFileSync(join(privateDir, 'relay-api-token'), 'utf8').trim(),
        ),
    );
  } finally {
    for (const child of processes.reverse()) await stop(child);
    if (upstream) await new Promise((r) => upstream.close(r));
    if (!database) rmSync(fixture, { recursive: true, force: true });
  }
}
