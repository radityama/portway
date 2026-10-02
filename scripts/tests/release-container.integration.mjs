import assert from 'node:assert/strict';
import { test } from 'node:test';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { randomBytes } from 'node:crypto';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { buildRelease, releaseOptions } from '../release.mjs';
import {
  root,
  start,
  stop,
  until,
  freePort,
  publicGet,
} from '../testing/control.mjs';

const execute = promisify(execFile);
test(
  'versioned non-root relay image forwards verified HTTPS and shuts down under constraints',
  { timeout: 240_000 },
  async () => {
    assert.equal(process.platform, 'linux', 'Container smoke requires Linux');
    assert.ok(process.getuid() > 0, 'Run fixture as a non-root account');
    const id = randomBytes(6).toString('hex'),
      version = 'v0.0.0-container-' + id;
    const image = 'portway-release-test:' + id,
      container = 'portway-release-test-' + id;
    const probeName = container + '-probe';
    const assets = join(root, 'dist/releases', version),
      fixture = await mkdtemp(join(tmpdir(), 'portway-image-'));
    let imageBuilt = false,
      containerStarted = false,
      agent,
      upstream;
    const docker = (args, timeout = 15_000) =>
      execute('docker', args, { cwd: root, timeout, maxBuffer: 512 * 1024 });
    const removeContainer = async (name, graceful = false) => {
      if (graceful) {
        try {
          await docker(['stop', '--time', '5', name]);
        } catch (error) {
          if (!error.stderr?.includes('No such container')) throw error;
        }
      }
      await until(async () => {
        try {
          await docker(['rm', '--force', name]);
          return true;
        } catch (error) {
          if (error.stderr?.includes('No such container')) return true;
          if (error.stderr?.includes('already in progress')) return false;
          throw error;
        }
      }, 5000);
    };
    try {
      const arch = process.arch === 'arm64' ? 'arm64' : 'amd64';
      const build = await buildRelease(
        releaseOptions([
          '--version',
          version,
          '--allow-dirty',
          '--targets',
          'linux/' + arch,
        ]),
        root,
      );
      await docker(
        [
          'build',
          '-f',
          'deploy/docker/relay.Dockerfile',
          '--build-arg',
          'VERSION=' + version,
          '--build-arg',
          'TARGETARCH=' + arch,
          '--build-arg',
          'REVISION=' + build.revision,
          '-t',
          image,
          '.',
        ],
        120_000,
      );
      imageBuilt = true;
      assert.equal(
        (
          await docker([
            'image',
            'inspect',
            image,
            '--format',
            '{{.Config.User}}',
          ])
        ).stdout.trim(),
        '65532:65532',
      );
      const probe = await docker([
        'run',
        '--rm',
        '--name',
        probeName,
        '--read-only',
        '--network',
        'none',
        '--cap-drop',
        'ALL',
        '--pids-limit',
        '64',
        '--memory',
        '128m',
        '--entrypoint',
        '/usr/local/bin/portway',
        image,
        'version',
        '--json',
      ]);
      assert.equal(JSON.parse(probe.stdout).data.version, version);
      const privateDir = join(fixture, 'private');
      await execute('go', ['run', './cmd/dev-init', '-dir', privateDir], {
        cwd: root,
        timeout: 30_000,
        maxBuffer: 65536,
      });
      const [relayPort, publicPort] = await Promise.all([
        freePort(),
        freePort(),
      ]);
      assert.notEqual(relayPort, publicPort);
      const variables = {
        RELAY_PORT: String(relayPort),
        RELAY_BIND_HOST: '0.0.0.0',
        PUBLIC_PORT: String(publicPort),
        PUBLIC_BIND_HOST: '0.0.0.0',
        RELAY_TLS_CERT_FILE: '/run/portway/relay-cert.pem',
        RELAY_TLS_KEY_FILE: '/run/portway/relay-key.pem',
        RELAY_CREDENTIALS_FILE: '/run/portway/relay-credentials.json',
        PUBLIC_TLS_CERT_FILE: '/run/portway/public-cert.pem',
        PUBLIC_TLS_KEY_FILE: '/run/portway/public-key.pem',
        RELAY_SHUTDOWN_TIMEOUT: '2s',
      };
      await docker([
        'run',
        '--detach',
        '--rm',
        '--name',
        container,
        '--read-only',
        '--cap-drop',
        'ALL',
        '--pids-limit',
        '128',
        '--memory',
        '128m',
        '--user',
        `${process.getuid()}:${process.getgid()}`,
        '--mount',
        `type=bind,src=${privateDir},dst=/run/portway,readonly`,
        '-p',
        `127.0.0.1:${relayPort}:${relayPort}`,
        '-p',
        `127.0.0.1:${publicPort}:${publicPort}`,
        ...Object.entries(variables).flatMap(([key, value]) => [
          '-e',
          key + '=' + value,
        ]),
        image,
      ]);
      containerStarted = true;
      await until(async () =>
        (await docker(['logs', container])).stdout.includes('relay_listening'),
      );
      upstream = createServer((_req, res) =>
        res.end('packaged-release-response'),
      );
      await new Promise((resolve) => upstream.listen(0, '127.0.0.1', resolve));
      const env = { ...process.env };
      for (const key of Object.keys(env))
        if (key.startsWith('PORTWAY_')) delete env[key];
      Object.assign(env, {
        PORTWAY_JSON: '1',
        PORTWAY_CONFIG_DIR: join(fixture, 'profile'),
        PORTWAY_STATE_DIR: join(fixture, 'state'),
        PORTWAY_RELAY_ADDR: `127.0.0.1:${relayPort}`,
        PORTWAY_RELAY_CA_FILE: join(privateDir, 'ca.pem'),
        PORTWAY_TOKEN_FILE: join(privateDir, 'agent-token'),
        PORTWAY_SHUTDOWN_TIMEOUT: '2s',
      });
      const binary = join(assets, `portway_${version}_linux_${arch}`);
      agent = start(binary, [String(upstream.address().port)], env);
      const events = () =>
        agent
          .output()
          .trim()
          .split('\n')
          .filter(Boolean)
          .map((line) => JSON.parse(line));
      await until(() => {
        assert.equal(agent.closed(), false, agent.output());
        return events().some((e) => e.event === 'ready');
      });
      const url = events().find((e) => e.event === 'ready').public_url;
      assert.deepEqual(
        await publicGet(
          url + '/',
          await readFile(join(privateDir, 'public-ca.pem')),
        ),
        { status: 200, body: 'packaged-release-response' },
      );
      const stopped = await execute(binary, ['stop', 'tnl_local_dev'], {
        cwd: root,
        env,
        timeout: 5000,
      });
      assert.equal(JSON.parse(stopped.stdout).event, 'stop_requested');
      await until(() => agent.closed());
      assert.equal((await agent.completion)[0], 0);
      const logs = (await docker(['logs', container])).stdout;
      const token = (
        await readFile(join(privateDir, 'agent-token'), 'utf8')
      ).trim();
      assert.equal(
        logs.includes(token) || agent.output().includes(token),
        false,
        'credential in runtime output',
      );
    } finally {
      await stop(agent);
      if (upstream) {
        upstream.closeAllConnections();
        await new Promise((resolve) => upstream.close(resolve));
      }
      await removeContainer(container, containerStarted);
      await removeContainer(probeName);
      if (imageBuilt) await docker(['image', 'rm', '--force', image]);
      await rm(assets, { recursive: true, force: true });
      await rm(fixture, { recursive: true, force: true });
    }
  },
);
