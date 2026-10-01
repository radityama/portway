import assert from 'node:assert/strict';
import { test } from 'node:test';
import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { createServer } from 'node:http';
import { request } from 'node:https';
import { databaseFixture } from '../testing/database.mjs';
import { dnsFixture } from '../testing/dns.mjs';
import { databaseClient } from '../../apps/api/src/database.ts';
import { root, freePort, start, stop, until } from '../testing/control.mjs';
function get(host, port, ca, authority = host + ':' + port) {
  return new Promise((resolve, reject) => {
    const req = request(
      {
        hostname: '127.0.0.1',
        port,
        servername: host,
        headers: { Host: authority },
        ca,
        minVersion: 'TLSv1.3',
        timeout: 4000,
        agent: false,
      },
      (res) => {
        const serial = res.socket.getPeerCertificate().serialNumber;
        let body = '';
        res.on('data', (chunk) => {
          body += chunk;
          if (body.length > 4096) req.destroy(new Error('Response too large'));
        });
        res.on('end', () => resolve({ status: res.statusCode, body, serial }));
        res.on('error', reject);
      },
    );
    req.on('timeout', () => req.destroy(new Error('Request timeout')));
    req.on('error', reject);
    req.end();
  });
}
test(
  'real custom HTTPS: DNS proof, wildcard/alias routing, live renewal, outage isolation and removal',
  { timeout: 90_000 },
  async (t) => {
    const fixture = await databaseFixture(),
      dns = await dnsFixture(),
      client = databaseClient(fixture.env.DATABASE_URL),
      processes = [];
    let upstream;
    try {
      const [apiPort, relayPort, publicPort] = await Promise.all([
        freePort(),
        freePort(),
        freePort(),
      ]);
      assert.equal(new Set([apiPort, relayPort, publicPort]).size, 3);
      await client.relay.update({
        where: { id: 'rel_local' },
        data: { port: relayPort },
      });
      execFileSync(
        'go',
        ['run', './cmd/dev-init', '-dir', fixture.privateDir],
        { cwd: root, timeout: 30_000, stdio: 'pipe' },
      );
      const certDir = join(fixture.directory, 'certificates'),
        certBin = join(root, 'bin/portway-cert');
      execFileSync(certBin, ['init', '--dir', certDir], {
        cwd: root,
        timeout: 5000,
        stdio: 'pipe',
      });
      const hosts = '*.portway.localhost,app.example.test,blocked.example.test';
      const issue = (renew) =>
        JSON.parse(
          execFileSync(
            certBin,
            [
              'issue',
              '--dir',
              certDir,
              '--hosts',
              hosts,
              '--days',
              '1',
              '--renew-before',
              renew,
            ],
            { cwd: root, timeout: 5000, stdio: 'pipe' },
          ),
        );
      const first = issue('23h'),
        manifestPath = join(certDir, 'certificate.json'),
        ca = readFileSync(join(certDir, 'ca.pem'));
      const base = 'http://127.0.0.1:' + apiPort + '/api/v1';
      const env = {
        ...fixture.env,
        API_PORT: String(apiPort),
        API_DNS_SERVER: dns.server,
        API_CREDENTIAL_TTL_SECONDS: '120',
        RELAY_PORT: String(relayPort),
        PUBLIC_PORT: String(publicPort),
        RELAY_TLS_CERT_FILE: join(fixture.privateDir, 'relay-cert.pem'),
        RELAY_TLS_KEY_FILE: join(fixture.privateDir, 'relay-key.pem'),
        PUBLIC_TLS_CERT_FILE: first.certFile,
        PUBLIC_TLS_KEY_FILE: first.keyFile,
        PUBLIC_TLS_MANIFEST_FILE: manifestPath,
        PUBLIC_TLS_RELOAD_INTERVAL: '100ms',
        RELAY_API_URL: base,
        RELAY_API_TOKEN_FILE: join(fixture.privateDir, 'relay-api-token'),
        RELAY_ID: 'rel_local',
        RELAY_API_REPORT_INTERVAL: '100ms',
        PORTWAY_JSON: '1',
        PORTWAY_API_URL: base,
        PORTWAY_API_TOKEN_FILE: join(fixture.privateDir, 'api-token'),
        PORTWAY_RELAY_CA_FILE: join(fixture.privateDir, 'ca.pem'),
        PORTWAY_STATE_DIR: join(fixture.directory, 'agent-state'),
      };
      delete env.PORTWAY_GENERATION;
      const call = async (path, method = 'GET', data) => {
        const response = await fetch(base + path, {
          method,
          headers: {
            Authorization: 'Bearer ' + fixture.bearer,
            ...(data === undefined
              ? {}
              : { 'Content-Type': 'application/json' }),
          },
          ...(data === undefined ? {} : { body: JSON.stringify(data) }),
          signal: AbortSignal.timeout(5000),
        });
        return {
          status: response.status,
          value: response.status === 204 ? null : await response.json(),
        };
      };
      const launchAPI = () => {
        const p = start(process.execPath, ['apps/api/dist/index.js'], env);
        processes.push(p);
        return p;
      };
      let api = launchAPI();
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
      const created = await call('/domains', 'POST', {
        hostname: 'app.example.test',
        tunnelId: 'tnl_local_dev',
      });
      assert.equal(created.status, 201);
      const domain = created.value.data.domain,
        dnsRecord = created.value.data.verification;
      assert.equal(
        (await call('/domains/' + domain.id + '/activate', 'POST')).status,
        409,
      );
      dns.records.set(dnsRecord.name, [dnsRecord.value]);
      assert.equal(
        (await call('/domains/' + domain.id + '/verify', 'POST')).status,
        200,
      );
      assert.equal(
        (await call('/domains/' + domain.id + '/activate', 'POST')).status,
        200,
      );
      let hits = 0;
      upstream = createServer((req, res) => {
        hits++;
        res.end(
          JSON.stringify({
            host: req.headers.host,
            forwarded: req.headers['x-forwarded-host'],
          }),
        );
      });
      await new Promise((resolve) => upstream.listen(0, '127.0.0.1', resolve));
      const cli = start(
        join(root, 'bin/portway'),
        [String(upstream.address().port)],
        env,
      );
      processes.push(cli);
      const events = () =>
        cli
          .output()
          .trim()
          .split('\n')
          .filter(Boolean)
          .map((line) => JSON.parse(line));
      await until(() => events().some((e) => e.event === 'ready'));
      const hostname = new URL(
        events().find((e) => e.event === 'ready').public_url,
      ).hostname;
      await until(async () => {
        try {
          return (await get('app.example.test', publicPort, ca)).status === 200;
        } catch {
          return false;
        }
      });
      const initial = await get('app.example.test', publicPort, ca);
      assert.equal(initial.status, 200);
      assert.deepEqual(JSON.parse(initial.body), {
        host: 'app.example.test',
        forwarded: 'app.example.test:' + publicPort,
      });
      assert.equal((await get(hostname, publicPort, ca)).status, 200);
      const before = hits;
      await assert.rejects(get('blocked.example.test', publicPort, ca));
      assert.equal(hits, before);
      assert.equal(
        (
          await get(
            'app.example.test',
            publicPort,
            ca,
            hostname + ':' + publicPort,
          )
        ).status,
        421,
      );
      t.diagnostic(
        'proof-backed alias and wildcard both forward; unapproved certificate SAN is denied',
      );
      const renewed = issue('23h59m59.999s');
      assert.notEqual(renewed.certFile, first.certFile);
      let rotated;
      await until(async () => {
        rotated = await get('app.example.test', publicPort, ca);
        return rotated.serial !== initial.serial;
      });
      const validManifest = readFileSync(manifestPath);
      writeFileSync(manifestPath, '{"domains":[],"unexpected":true}', {
        mode: 0o600,
      });
      await until(() =>
        relay.output().includes('public_certificate_reload_failed'),
      );
      assert.equal(
        (await get('app.example.test', publicPort, ca)).serial,
        rotated.serial,
      );
      writeFileSync(manifestPath, validManifest, { mode: 0o600 });
      t.diagnostic(
        'renewal changes the served serial under unchanged CA trust; malformed replacement keeps the good pair',
      );
      await stop(api);
      execFileSync('docker', ['pause', fixture.name], {
        timeout: 5000,
        stdio: 'pipe',
      });
      try {
        assert.equal(
          (await get('app.example.test', publicPort, ca)).status,
          200,
        );
      } finally {
        execFileSync('docker', ['unpause', fixture.name], {
          timeout: 5000,
          stdio: 'pipe',
        });
      }
      api = launchAPI();
      await until(async () => {
        try {
          return (
            (await fetch(base.replace('/api/v1', '/health'))).status === 200
          );
        } catch {
          return false;
        }
      });
      assert.equal(
        (await call('/domains/' + domain.id)).value.data.domain.status,
        'ACTIVE',
      );
      assert.equal((await call('/domains/' + domain.id, 'DELETE')).status, 204);
      await until(async () => {
        try {
          await get('app.example.test', publicPort, ca);
          return false;
        } catch {
          return true;
        }
      });
      assert.equal((await get(hostname, publicPort, ca)).status, 200);
      const logs = processes.map((p) => p.output()).join('\n');
      assert.equal(logs.includes(fixture.bearer), false);
      assert.equal(logs.includes(fixture.relayBearer), false);
      assert.equal(logs.includes(dnsRecord.value), false);
      t.diagnostic(
        'admitted alias survives API/database outage; restart retains policy and deletion removes alias',
      );
    } catch (error) {
      for (const p of processes) t.diagnostic(p.output().slice(-2000));
      throw error;
    } finally {
      for (const p of processes.reverse()) await stop(p);
      if (upstream) {
        upstream.closeAllConnections();
        await new Promise((resolve) => upstream.close(resolve));
      }
      await client.$disconnect();
      await dns.close();
      await fixture.close();
    }
  },
);
