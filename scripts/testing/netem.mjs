import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createHash, randomBytes } from 'node:crypto';
import { copyFileSync, existsSync, mkdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { root } from './control.mjs';
const execute = promisify(execFile);

export async function netemFixture(directory, relayPort) {
  const context = join(directory, 'netem-build');
  mkdirSync(context, { mode: 0o700 });
  await execute(
    'go',
    ['build', '-o', join(context, 'netem-proxy'), './tests/load/netem'],
    {
      cwd: root,
      env: { ...process.env, CGO_ENABLED: '0', GOOS: 'linux' },
      timeout: 30_000,
    },
  );
  const dockerfile = join(root, 'tests/load/netem/Dockerfile');
  copyFileSync(dockerfile, join(context, 'Dockerfile'));
  const digest = createHash('sha256')
    .update(readFileSync(dockerfile))
    .update(readFileSync(join(context, 'netem-proxy')))
    .digest('hex')
    .slice(0, 16);
  const image = 'portway-test-netem:' + digest;
  const ca =
    process.env.PORTWAY_NETEM_CA_FILE ?? '/etc/ssl/certs/ca-certificates.crt';
  const args = ['build', '--tag', image];
  if (existsSync(ca)) args.push('--secret', 'id=proxy_ca,src=' + ca);
  if (process.env.PORTWAY_NETEM_BASE_IMAGE)
    args.push(
      '--build-arg',
      'BASE_IMAGE=' + process.env.PORTWAY_NETEM_BASE_IMAGE,
    );
  args.push(context);
  await execute('docker', args, { timeout: 120_000, maxBuffer: 128 * 1024 });
  const name = 'portway-netem-' + randomBytes(6).toString('hex');
  let started = false;
  const command = async (...args) =>
    (await execute('docker', ['exec', name, ...args], { timeout: 5000 }))
      .stdout;
  try {
    await execute(
      'docker',
      [
        'run',
        '--detach',
        '--rm',
        '--name',
        name,
        '--cap-add=NET_ADMIN',
        '--add-host=host.docker.internal:host-gateway',
        '-p',
        '127.0.0.1::8666',
        image,
        '-target',
        `host.docker.internal:${relayPort}`,
      ],
      { timeout: 10_000 },
    );
    started = true;
    // Prefer netem. Some managed kernels omit sch_netem; packet filtering still
    // injects real TCP loss, with bounded bridge delivery delay for latency.
    // Missing NET_ADMIN fails explicitly. Neither backend touches a host link.
    let backend = 'netem';
    try {
      await command(
        'tc',
        'qdisc',
        'add',
        'dev',
        'eth0',
        'root',
        'netem',
        'delay',
        '1ms',
      );
      await command('tc', 'qdisc', 'del', 'dev', 'eth0', 'root');
    } catch (error) {
      if (!error.stderr?.includes('Specified qdisc kind is unknown'))
        throw error;
      backend = 'packet-filter';
      await command('iptables', '-N', 'PORTWAY_CHAOS');
      await command(
        'iptables',
        '-A',
        'OUTPUT',
        '-p',
        'tcp',
        '--sport',
        '8666',
        '-j',
        'PORTWAY_CHAOS',
      );
    }
    const published = (
      await execute('docker', ['port', name, '8666'], { timeout: 5000 })
    ).stdout.trim();
    return {
      port: Number(published.split(':').at(-1)),
      name,
      backend,
      async impair({ delayMS = 0, lossPercent = 0 } = {}) {
        if (
          !Number.isInteger(delayMS) ||
          delayMS < 0 ||
          delayMS > 500 ||
          !Number.isFinite(lossPercent) ||
          lossPercent < 0 ||
          lossPercent > 100
        )
          throw new Error('Invalid fixture impairment');
        if (backend === 'netem')
          await command(
            'tc',
            'qdisc',
            'replace',
            'dev',
            'eth0',
            'root',
            'netem',
            'delay',
            delayMS + 'ms',
            'loss',
            lossPercent + '%',
          );
        else {
          await command('iptables', '-F', 'PORTWAY_CHAOS');
          if (lossPercent === 100)
            await command('iptables', '-A', 'PORTWAY_CHAOS', '-j', 'DROP');
          else if (lossPercent > 0)
            await command(
              'iptables',
              '-A',
              'PORTWAY_CHAOS',
              '-m',
              'statistic',
              '--mode',
              'random',
              '--probability',
              String(lossPercent / 100),
              '-j',
              'DROP',
            );
          await command(
            '/usr/local/bin/netem-proxy',
            '-delay-ms',
            String(delayMS),
          );
          // Wait for the one owned watcher to observe the fixed bounded file.
          await new Promise((resolve) => setTimeout(resolve, 50));
        }
      },
      async clear() {
        if (backend === 'netem')
          await command('tc', 'qdisc', 'del', 'dev', 'eth0', 'root');
        else {
          await command('iptables', '-F', 'PORTWAY_CHAOS');
          await command('/usr/local/bin/netem-proxy', '-delay-ms', '0');
          await new Promise((resolve) => setTimeout(resolve, 50));
        }
      },
      async stats() {
        if (backend === 'netem') {
          const value = JSON.parse(
            await command('tc', '-j', '-s', 'qdisc', 'show', 'dev', 'eth0'),
          )[0];
          return {
            backend,
            droppedPackets: value.drops ?? value.stats?.drops ?? 0,
          };
        }
        const raw = await command(
          'iptables',
          '-L',
          'PORTWAY_CHAOS',
          '-n',
          '-v',
          '-x',
        );
        const rows = [...raw.matchAll(/^\s*(\d+)\s+\d+\s+DROP\s/gm)];
        return {
          backend,
          droppedPackets: rows.reduce(
            (sum, match) => sum + Number(match[1]),
            0,
          ),
        };
      },
      async close() {
        await execute('docker', ['stop', '--time', '2', name], {
          timeout: 10_000,
        }).catch(() => {});
      },
    };
  } catch (error) {
    if (started)
      await execute('docker', ['stop', '--time', '2', name], {
        timeout: 10_000,
      }).catch(() => {});
    throw error;
  }
}
