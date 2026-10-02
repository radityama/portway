import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { createHash, randomBytes } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';
import { once } from 'node:events';
import { databaseFixture } from './database.mjs';
import { root, freePort, start, stop, until } from './control.mjs';
import { netemFixture } from './netem.mjs';
import { databaseClient } from '../../apps/api/src/database.ts';
import { provision } from '../../apps/api/src/provision.ts';
import { loadSeed } from '../../apps/api/src/seed.ts';
import { digest } from '../../apps/api/src/store.ts';

export async function platformFixture({
  network = false,
  maxStreams = 32,
} = {}) {
  const database = await databaseFixture();
  const client = databaseClient(database.env.DATABASE_URL);
  const processes = [],
    agents = [],
    holds = new Map(),
    hits = new Map(),
    relayPorts = new Map(),
    secrets = [database.bearer, database.relayBearer];
  let api, relay, bridge, upstream;
  let active = 0,
    peakActive = 0,
    canceled = 0;
  const close = async () => {
    for (const hold of holds.values()) hold.end('released');
    for (const process of processes.slice().reverse()) await stop(process);
    if (upstream) {
      upstream.closeAllConnections();
      await new Promise((resolve) => upstream.close(resolve));
    }
    await bridge?.close();
    await client.$disconnect();
    await database.close();
  };
  try {
    const ports = await Promise.all(
      Array.from({ length: 4 }, () => freePort()),
    );
    assert.equal(new Set(ports).size, ports.length);
    const [apiPort, relayPort, publicPort, metricsPort] = ports;
    execFileSync('go', ['run', './cmd/dev-init', '-dir', database.privateDir], {
      cwd: root,
      timeout: 30_000,
      stdio: 'pipe',
    });
    if (network) bridge = await netemFixture(database.directory, relayPort);
    await client.relay.update({
      where: { id: 'rel_local' },
      data: { port: bridge?.port ?? relayPort },
    });
    const base = `http://127.0.0.1:${apiPort}/api/v1`;
    const env = {
      ...database.env,
      API_PORT: String(apiPort),
      API_CREDENTIAL_TTL_SECONDS: '300',
      RELAY_PORT: String(relayPort),
      PUBLIC_PORT: String(publicPort),
      RELAY_BIND_HOST: network ? '0.0.0.0' : '127.0.0.1',
      PUBLIC_BIND_HOST: '127.0.0.1',
      RELAY_METRICS_PORT: String(metricsPort),
      PORTWAY_METRICS_PORT: '0',
      RELAY_API_URL: base,
      RELAY_API_TOKEN_FILE: join(database.privateDir, 'relay-api-token'),
      RELAY_ID: 'rel_local',
      RELAY_API_REPORT_INTERVAL: '500ms',
      RELAY_TLS_CERT_FILE: join(database.privateDir, 'relay-cert.pem'),
      RELAY_TLS_KEY_FILE: join(database.privateDir, 'relay-key.pem'),
      PUBLIC_TLS_CERT_FILE: join(database.privateDir, 'public-cert.pem'),
      PUBLIC_TLS_KEY_FILE: join(database.privateDir, 'public-key.pem'),
      RELAY_MAX_CONNECTIONS: '64',
      PUBLIC_MAX_CONNECTIONS: '128',
      RELAY_MAX_STREAMS: String(maxStreams),
      PORTWAY_MAX_STREAMS: String(maxStreams),
      RELAY_STREAM_TIMEOUT: '10s',
      PORTWAY_STREAM_TIMEOUT: '10s',
      RELAY_SHUTDOWN_TIMEOUT: '2s',
      PORTWAY_SHUTDOWN_TIMEOUT: '2s',
      PORTWAY_JSON: '1',
      PORTWAY_API_URL: base,
      PORTWAY_API_TOKEN_FILE: join(database.privateDir, 'api-token'),
      PORTWAY_RELAY_CA_FILE: join(database.privateDir, 'ca.pem'),
      PORTWAY_STATE_DIR: join(database.directory, 'agent-state'),
    };
    delete env.PORTWAY_GENERATION;
    const call = async (path, method = 'GET', body) => {
      const response = await fetch(base + path, {
        method,
        headers: {
          Authorization: 'Bearer ' + database.bearer,
          ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        signal: AbortSignal.timeout(5000),
      });
      return {
        status: response.status,
        value: response.status === 204 ? null : await response.json(),
      };
    };
    const startAPI = async () => {
      api = start(process.execPath, ['apps/api/dist/index.js'], env);
      processes.push(api);
      await until(async () => {
        try {
          return (
            (
              await fetch(base.replace('/api/v1', '/health'), {
                signal: AbortSignal.timeout(1000),
              })
            ).status === 200
          );
        } catch {
          return false;
        }
      });
      return api;
    };
    const startRelay = async (overrides = {}) => {
      const configuration = { ...env, ...overrides };
      relay = start(join(root, 'bin/portway-relay'), [], configuration);
      processes.push(relay);
      relayPorts.set(relay, configuration.RELAY_METRICS_PORT);
      try {
        await until(() => {
          assert.equal(relay.closed(), false, relay.output());
          return relay.output().includes('relay_presence_registered');
        });
      } catch (error) {
        throw new Error('Fixture relay startup failed: ' + relay.output(), {
          cause: error,
        });
      }
      return relay;
    };
    const startStandby = async () => {
      const [relayPort, publicPort, metricsPort] = await Promise.all(
        Array.from({ length: 3 }, () => freePort()),
      );
      assert.equal(new Set([relayPort, publicPort, metricsPort]).size, 3);
      const token = randomBytes(32).toString('base64url');
      secrets.push(token);
      const tokenFile = join(database.privateDir, 'relay-api-token-backup');
      writeFileSync(tokenFile, token + '\n', { mode: 0o600 });
      const seed = loadSeed(database.env.API_SEED_FILE);
      seed.relays.push({
        id: 'rel_z_backup',
        name: 'Chaos standby',
        region: 'local',
        hostname: 'localhost',
        port: relayPort,
        protocol: 'tls',
        status: 'HEALTHY',
        lastSeenAt: null,
      });
      seed.relayKeys.push({
        relayId: 'rel_z_backup',
        tokenHash: digest(token),
        expiresAt: new Date(Date.now() + 3600000).toISOString(),
      });
      await provision(client, seed);
      return startRelay({
        RELAY_ID: 'rel_z_backup',
        RELAY_API_TOKEN_FILE: tokenFile,
        RELAY_PORT: String(relayPort),
        PUBLIC_PORT: String(publicPort),
        RELAY_METRICS_PORT: String(metricsPort),
        RELAY_BIND_HOST: '127.0.0.1',
      });
    };
    await startAPI();
    await startRelay();
    upstream = createServer(async (req, res) => {
      const id = req.headers['x-load-id'] ?? 'unlabeled';
      if (typeof id !== 'string' || id.length > 100 || hits.size >= 10_000) {
        res.writeHead(503);
        res.end();
        return;
      }
      hits.set(id, (hits.get(id) ?? 0) + 1);
      active++;
      peakActive = Math.max(active, peakActive);
      const canceledWrite = new AbortController();
      res.once('close', () => {
        canceledWrite.abort();
        active--;
        holds.delete(id);
        if (!res.writableFinished) canceled++;
      });
      try {
        if (req.url === '/hold') {
          holds.set(id, res);
          return;
        }
        if (req.url.startsWith('/bytes?size=')) {
          const raw = new URL(req.url, 'http://localhost').searchParams.get(
            'size',
          );
          const size = Number(raw);
          if (
            !/^\d+$/.test(raw) ||
            !Number.isSafeInteger(size) ||
            size > 64 * 1024 * 1024
          ) {
            res.writeHead(400);
            res.end();
            return;
          }
          res.setHeader('Content-Length', size);
          const chunk = Buffer.alloc(16 * 1024, 0x61);
          let left = size;
          while (left > 0 && !res.destroyed) {
            const length = Math.min(chunk.length, left);
            left -= length;
            if (!res.write(chunk.subarray(0, length)))
              await once(res, 'drain', { signal: canceledWrite.signal });
          }
          res.end();
          return;
        }
        const hash = createHash('sha256');
        let length = 0;
        for await (const part of req) {
          length += part.length;
          hash.update(part);
          if (length > 16 * 1024 * 1024) {
            res.writeHead(413);
            res.end();
            return;
          }
        }
        res.setHeader('Content-Type', 'application/json');
        res.end(JSON.stringify({ id, length, digest: hash.digest('hex') }));
      } catch {
        res.destroy();
      }
    });
    upstream.maxConnections = 128;
    upstream.headersTimeout = 5000;
    upstream.requestTimeout = 20_000;
    upstream.timeout = 20_000;
    await new Promise((resolve) => upstream.listen(0, '127.0.0.1', resolve));
    const events = (agent) =>
      agent
        .output()
        .trim()
        .split('\n')
        .filter(Boolean)
        .map((line) => JSON.parse(line));
    const ready = (agent) => events(agent).filter((e) => e.event === 'ready');
    const startAgent = async (tunnelId) => {
      const agent = start(
        join(root, 'bin/portway'),
        [String(upstream.address().port)],
        { ...env, PORTWAY_TUNNEL_ID: tunnelId },
      );
      processes.push(agent);
      agents.push(agent);
      await until(() => ready(agent).length > 0);
      return agent;
    };
    const primary = await startAgent('tnl_local_dev');
    const metrics = async (node = relay) => {
      const response = await fetch(
        `http://127.0.0.1:${relayPorts.get(node)}/metrics`,
        {
          signal: AbortSignal.timeout(2000),
        },
      );
      assert.equal(response.status, 200);
      const lines = (await response.text()).split('\n'),
        values = new Map();
      for (const line of lines) {
        const match = /^([a-zA-Z_][a-zA-Z0-9_]*) ([0-9.eE+-]+)$/.exec(line);
        if (match) values.set(match[1], Number(match[2]));
      }
      return values;
    };
    return {
      database,
      client,
      bridge,
      processes,
      agents,
      primary,
      call,
      ready,
      events,
      startAPI,
      startRelay,
      startStandby,
      startAgent,
      metrics,
      close,
      get api() {
        return api;
      },
      get relay() {
        return relay;
      },
      url(agent = primary) {
        return ready(agent).at(-1).public_url;
      },
      ca: readFileSync(join(database.privateDir, 'public-ca.pem')),
      secrets,
      holds,
      hits,
      get active() {
        return active;
      },
      get peakActive() {
        return peakActive;
      },
      get canceled() {
        return canceled;
      },
      release(id) {
        holds.get(id)?.end('released');
      },
    };
  } catch (error) {
    await close();
    throw error;
  }
}
