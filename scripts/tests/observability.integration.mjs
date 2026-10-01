import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { createServer } from 'node:http';
import { request } from 'node:https';
import { connect } from 'node:tls';
import { execFileSync } from 'node:child_process';
import { databaseFixture } from '../testing/database.mjs';
import { root, freePort, start, stop, until } from '../testing/control.mjs';
import { databaseClient } from '../../apps/api/src/database.ts';

function publicRequest(
  url,
  ca,
  { method = 'GET', body = '', cancel = false } = {},
) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const req = request(
      {
        hostname: '127.0.0.1',
        port: u.port,
        servername: u.hostname,
        path: u.pathname + u.search,
        method,
        headers: {
          Host: u.host,
          'Content-Length': Buffer.byteLength(body),
          Authorization: 'Bearer request-secret',
          Cookie: 'private-cookie',
        },
        ca,
        minVersion: 'TLSv1.3',
        timeout: 5000,
      },
      (res) => {
        const parts = [];
        res.on('data', (b) => {
          parts.push(b);
          if (cancel) {
            req.destroy();
            resolve({
              status: res.statusCode,
              body: Buffer.concat(parts).toString(),
            });
          }
        });
        res.on('end', () =>
          resolve({
            status: res.statusCode,
            body: Buffer.concat(parts).toString(),
          }),
        );
        res.on('error', (e) => {
          if (!cancel) reject(e);
        });
      },
    );
    req.on('error', (e) => {
      if (!cancel) reject(e);
    });
    req.on('timeout', () => req.destroy(new Error('request deadline')));
    req.end(body);
  });
}
function websocket(url, ca) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const socket = connect(
      {
        host: '127.0.0.1',
        port: Number(u.port),
        servername: u.hostname,
        ca,
        minVersion: 'TLSv1.3',
      },
      () => {
        socket.write(
          `GET /ws?secret=query-secret HTTP/1.1\r\nHost: ${u.host}\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nAuthorization: Bearer request-secret\r\n\r\n`,
        );
        socket.write(Buffer.from([0x81, 0x82, 1, 2, 3, 4, 104 ^ 1, 105 ^ 2]));
      },
    );
    let received = Buffer.alloc(0);
    socket.setTimeout(5000, () =>
      socket.destroy(new Error('WebSocket deadline')),
    );
    socket.on('data', (b) => {
      received = Buffer.concat([received, b]);
      if (received.includes(Buffer.from([0x81, 2, 104, 105]))) {
        assert.match(received.toString(), /101 Switching Protocols/);
        socket.end();
        resolve();
      }
    });
    socket.on('error', reject);
  });
}
test(
  'real relay/agent scrapes and fenced scoped observations survive Redis/API outages without recording secrets',
  { timeout: 90_000 },
  async (t) => {
    const fixture = await databaseFixture();
    const processes = [];
    const client = databaseClient(fixture.env.DATABASE_URL);
    let upstream;
    try {
      const ports = await Promise.all(
        Array.from({ length: 5 }, () => freePort()),
      );
      assert.equal(new Set(ports).size, 5);
      const [apiPort, relayPort, publicPort, relayMetrics, agentMetrics] =
        ports;
      await client.relay.update({
        where: { id: 'rel_local' },
        data: { port: relayPort },
      });
      execFileSync(
        'go',
        ['run', './cmd/dev-init', '-dir', fixture.privateDir],
        { cwd: root, timeout: 30000, stdio: 'pipe' },
      );
      const base = `http://127.0.0.1:${apiPort}/api/v1`;
      const env = {
        ...fixture.env,
        API_PORT: String(apiPort),
        API_CREDENTIAL_TTL_SECONDS: '60',
        RELAY_PORT: String(relayPort),
        PUBLIC_PORT: String(publicPort),
        RELAY_METRICS_PORT: String(relayMetrics),
        PORTWAY_METRICS_PORT: String(agentMetrics),
        RELAY_API_URL: base,
        RELAY_API_TOKEN_FILE: join(fixture.privateDir, 'relay-api-token'),
        RELAY_ID: 'rel_local',
        RELAY_API_REPORT_INTERVAL: '200ms',
        RELAY_TLS_CERT_FILE: join(fixture.privateDir, 'relay-cert.pem'),
        RELAY_TLS_KEY_FILE: join(fixture.privateDir, 'relay-key.pem'),
        PUBLIC_TLS_CERT_FILE: join(fixture.privateDir, 'public-cert.pem'),
        PUBLIC_TLS_KEY_FILE: join(fixture.privateDir, 'public-key.pem'),
        PORTWAY_JSON: '1',
        PORTWAY_API_URL: base,
        PORTWAY_API_TOKEN_FILE: join(fixture.privateDir, 'api-token'),
        PORTWAY_RELAY_CA_FILE: join(fixture.privateDir, 'ca.pem'),
        PORTWAY_STATE_DIR: join(fixture.directory, 'agent-state'),
      };
      delete env.PORTWAY_GENERATION;
      const call = async (path) => {
        const r = await fetch(base + path, {
          headers: { Authorization: 'Bearer ' + fixture.bearer },
          signal: AbortSignal.timeout(3000),
        });
        return { status: r.status, value: await r.json() };
      };
      let api = start(process.execPath, ['apps/api/dist/index.js'], env);
      processes.push(api);
      await until(async () => {
        try {
          return (
            (await fetch(base.replace('/api/v1', '/health'))).status === 200
          );
        } catch {
          return false;
        }
      });
      const relay = start(join(root, 'bin/portway-relay'), [], env);
      processes.push(relay);
      await until(() => relay.output().includes('relay_presence_registered'));
      upstream = createServer((req, res) => {
        if (req.url.startsWith('/events')) {
          res.writeHead(200, { 'Content-Type': 'text/event-stream' });
          res.write('data: first\n\n');
          const timer = setTimeout(() => res.end('data: last\n\n'), 800);
          res.on('close', () => clearTimeout(timer));
          return;
        }
        if (req.url.startsWith('/cancel')) {
          res.writeHead(200, { 'Content-Type': 'text/event-stream' });
          res.write('data: first\n\n');
          const timer = setTimeout(() => res.end('finished'), 5000);
          res.on('close', () => clearTimeout(timer));
          return;
        }
        let body = '';
        req.on('data', (b) => {
          body += b;
        });
        req.on('end', () => {
          res.statusCode = req.url.startsWith('/fail') ? 500 : 200;
          res.end('reply:' + body);
        });
      });
      upstream.on('upgrade', (req, socket, head) => {
        const accept = createHash('sha1')
          .update(
            req.headers['sec-websocket-key'] +
              '258EAFA5-E914-47DA-95CA-C5AB0DC85B11',
          )
          .digest('base64');
        socket.write(
          `HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`,
        );
        const echo = () => socket.write(Buffer.from([0x81, 2, 104, 105]));
        if (head.length) echo();
        socket.on('data', echo);
        socket.on('end', () => socket.end());
        socket.on('error', () => socket.destroy());
      });
      await new Promise((r) => upstream.listen(0, '127.0.0.1', r));
      const cli = start(
        join(root, 'bin/portway'),
        [String(upstream.address().port)],
        env,
      );
      processes.push(cli);
      await until(() => cli.output().includes('"event":"ready"'));
      const events = () =>
        cli
          .output()
          .trim()
          .split('\n')
          .filter(Boolean)
          .map((l) => JSON.parse(l));
      const url = events().find((e) => e.event === 'ready').public_url;
      const ca = readFileSync(join(fixture.privateDir, 'public-ca.pem'));
      const scrape = async (port) => {
        const r = await fetch(`http://127.0.0.1:${port}/metrics`, {
          signal: AbortSignal.timeout(3000),
        });
        assert.equal(r.status, 200);
        return r.text();
      };
      assert.equal(
        (await call('/tunnels/tnl_local_dev/metrics')).value.data.available,
        false,
      );
      const streaming = publicRequest(url + '/events', ca);
      await until(async () => {
        const v = (await call('/tunnels/tnl_local_dev/metrics')).value.data;
        return (
          v.available &&
          v.metrics.requests === '0' &&
          v.metrics.activeRequests === 1
        );
      });
      assert.deepEqual(
        (await call('/tunnels/tnl_local_dev/logs')).value.data.logs,
        [],
      );
      assert.equal((await streaming).body, 'data: first\n\ndata: last\n\n');
      assert.deepEqual(
        await publicRequest(url + '/?secret=query-secret', ca, {
          method: 'POST',
          body: 'private-body',
        }),
        { status: 200, body: 'reply:private-body' },
      );
      assert.equal((await publicRequest(url + '/fail', ca)).status, 500);
      await websocket(url, ca);
      await until(
        async () =>
          BigInt(
            (await call('/tunnels/tnl_local_dev/metrics')).value.data.metrics
              ?.requests ?? '0',
          ) >= 4n,
      );
      const observed = (await call('/tunnels/tnl_local_dev/metrics')).value
        .data;
      assert.equal(observed.available, true);
      assert.equal(observed.metrics.requests, '4');
      assert.equal(observed.metrics.activeRequests, 0);
      assert.ok(BigInt(observed.metrics.bytesIn) >= 20n);
      assert.ok(BigInt(observed.metrics.bytesOut) > 40n);
      assert.equal(
        observed.metrics.latencyBuckets[11],
        observed.metrics.requests,
      );
      const logs = (await call('/tunnels/tnl_local_dev/logs')).value.data.logs;
      assert.equal(logs.length, 4);
      assert.ok(logs.some((l) => l.status === 101));
      assert.ok(logs.some((l) => l.status === 500 && l.outcome === 'error'));
      assert.ok(logs.some((l) => l.status === 200 && l.outcome === 'complete'));
      const relayText = await scrape(relayMetrics),
        agentText = await scrape(agentMetrics);
      assert.match(relayText, /portway_relay_connections_active 1\n/);
      assert.match(relayText, /portway_relay_streams_total 4\n/);
      assert.match(
        relayText,
        /portway_relay_request_duration_seconds_count 4\n/,
      );
      assert.match(agentText, /portway_agent_streams_total 4\n/);
      assert.match(agentText, /portway_agent_bytes_in_total [1-9]/);
      const operatorResponse = await fetch(
        base.replace('/api/v1', '/metrics'),
        { headers: { Authorization: 'Bearer ' + fixture.relayBearer } },
      );
      assert.equal(operatorResponse.status, 200);
      const operatorText = await operatorResponse.text();
      assert.match(operatorText, /portway_api_requests_total/);
      const output =
        JSON.stringify({ observed, logs }) +
        relayText +
        agentText +
        operatorText +
        relay.output() +
        api.output() +
        cli.output();
      for (const secret of [
        'query-secret',
        'private-body',
        'request-secret',
        'private-cookie',
        fixture.bearer,
        fixture.relayBearer,
      ])
        assert.equal(output.includes(secret), false, secret);
      t.diagnostic(
        'HTTP, SSE, WebSocket, counters, structured logs and scoped reads verified',
      );
      execFileSync('docker', ['pause', fixture.redisName], {
        timeout: 5000,
        stdio: 'pipe',
      });
      try {
        assert.equal(
          (await call('/tunnels/tnl_local_dev/metrics')).status,
          503,
        );
        assert.equal((await publicRequest(url + '/', ca)).status, 200);
        assert.match(
          await scrape(relayMetrics),
          /portway_relay_request_duration_seconds_count 5\n/,
        );
      } finally {
        execFileSync('docker', ['unpause', fixture.redisName], {
          timeout: 5000,
          stdio: 'pipe',
        });
      }
      await until(async () => {
        try {
          return (
            (await call('/tunnels/tnl_local_dev/metrics')).value.data.metrics
              ?.requests === '5'
          );
        } catch {
          return false;
        }
      });
      await stop(api);
      assert.equal((await publicRequest(url + '/', ca)).status, 200);
      assert.match(
        await scrape(relayMetrics),
        /portway_relay_request_duration_seconds_count 6\n/,
      );
      api = start(process.execPath, ['apps/api/dist/index.js'], env);
      processes.push(api);
      await until(async () => {
        try {
          return (
            (await call('/tunnels/tnl_local_dev/metrics')).value.data.metrics
              ?.requests === '6'
          );
        } catch {
          return false;
        }
      });
      await publicRequest(url + '/cancel', ca, { cancel: true });
      await until(async () => {
        const l = (await call('/tunnels/tnl_local_dev/logs')).value.data.logs;
        return l.some((e) => e.outcome === 'canceled');
      });
      assert.equal(events().filter((e) => e.event === 'ready').length, 1);
      t.diagnostic(
        'Redis and API outages preserved forwarding and locally accumulated observations; cancellation recorded',
      );
      await stop(cli);
      assert.match(await scrape(agentMetrics).catch(() => ''), /^$/);
      await until(async () =>
        /portway_relay_streams_active 0\n/.test(await scrape(relayMetrics)),
      );
    } finally {
      for (const p of processes.reverse()) await stop(p);
      if (upstream) {
        upstream.closeAllConnections();
        await new Promise((r) => upstream.close(r));
      }
      await client.$disconnect();
      await fixture.close();
    }
  },
);
