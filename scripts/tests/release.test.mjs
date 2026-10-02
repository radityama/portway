import assert from 'node:assert/strict';
import { test } from 'node:test';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createHash } from 'node:crypto';
import {
  mkdtemp,
  readFile,
  writeFile,
  rm,
  mkdir,
  symlink,
  chmod,
  readdir,
} from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildRelease, releaseOptions, targets } from '../release.mjs';

const execute = promisify(execFile);
const root = fileURLToPath(new URL('../../', import.meta.url));
test('release requires an explicit version, known targets and no ambiguous options', () => {
  assert.equal(targets.length, 6);
  for (const args of [
    [],
    ['--version', 'latest'],
    ['--version', 'v1.2.3/evil'],
    ['--version', 'v01.2.3'],
    ['--version', 'v1.2.3', '--targets', 'linux/386'],
    ['--version', 'v1.2.3', '--targets', 'linux/amd64,linux/amd64'],
    ['--version', 'v1.2.3', '--version', 'v2.0.0'],
  ])
    assert.throws(() => releaseOptions(args));
});
test(
  'native release is reproducible; reviewed installer refuses corruption and unsafe destinations',
  { timeout: 180_000, skip: process.platform === 'win32' },
  async (t) => {
    const dir = await mkdtemp(join(tmpdir(), 'portway-release-test-'));
    t.after(() => rm(dir, { recursive: true, force: true }));
    const target = `${process.platform === 'darwin' ? 'darwin' : 'linux'}/${process.arch === 'arm64' ? 'arm64' : 'amd64'}`;
    const options = (output) =>
      releaseOptions([
        '--version',
        'v0.0.0-test',
        '--allow-dirty',
        '--targets',
        target,
        '--out',
        output,
      ]);
    const first = join(dir, 'first'),
      second = join(dir, 'second');
    await buildRelease(options(first), root);
    await buildRelease(options(second), root);
    assert.deepEqual(
      await readFile(join(first, 'SHA256SUMS')),
      await readFile(join(second, 'SHA256SUMS')),
    );
    const manifest = JSON.parse(
      await readFile(join(first, 'manifest.json'), 'utf8'),
    );
    assert.equal(manifest.artifacts.length, 3);
    for (const item of manifest.artifacts) {
      const bytes = await readFile(join(first, item.name));
      assert.equal(
        createHash('sha256').update(bytes).digest('hex'),
        item.sha256,
      );
      assert.equal(bytes.length, item.bytes);
    }
    await assert.rejects(buildRelease(options(first), root), /already exists/);
    const cli = manifest.artifacts.find((item) => item.binary === 'portway');
    const result = await execute(join(first, cli.name), ['version', '--json'], {
      timeout: 5000,
    });
    const stamp = JSON.parse(result.stdout).data;
    assert.equal(stamp.version, 'v0.0.0-test');
    assert.equal(stamp.revision, manifest.revision);
    const installer = join(root, 'install/install.sh'),
      destination = join(dir, 'bin');
    const run = (extra = []) =>
      execute(
        'sh',
        [
          installer,
          '--version',
          'v0.0.0-test',
          '--dir',
          destination,
          '--assets-dir',
          first,
          ...extra,
        ],
        { timeout: 10_000 },
      );
    const digest = createHash('sha256')
      .update(await readFile(join(first, 'SHA256SUMS')))
      .digest('hex');
    await run(['--checksums-sha256', digest]);
    assert.deepEqual(
      await readFile(join(destination, 'portway')),
      await readFile(join(first, cli.name)),
    );
    const original = await readFile(join(destination, 'portway'));
    await writeFile(join(first, cli.name), 'corrupted binary');
    await assert.rejects(run(), /checksum mismatch/);
    assert.deepEqual(await readFile(join(destination, 'portway')), original);
    assert.deepEqual(await readdir(destination), ['portway']);
    await assert.rejects(
      run(['--checksums-sha256', '0'.repeat(64)]),
      /trusted digest/,
    );
    const sums = await readFile(join(first, 'SHA256SUMS'), 'utf8');
    await writeFile(
      join(first, 'SHA256SUMS'),
      sums +
        sums.split('\n').find((line) => line.endsWith('  ' + cli.name)) +
        '\n',
    );
    await assert.rejects(run(), /duplicate/);
    await rm(join(destination, 'portway'));
    await symlink(join(first, cli.name), join(destination, 'portway'));
    await assert.rejects(run(), /symlink/);
    await chmod(destination, 0o777);
    await assert.rejects(run(), /writable/);
    const link = join(dir, 'link');
    await symlink(destination, link);
    await assert.rejects(
      execute('sh', [
        installer,
        '--version',
        'v0.0.0-test',
        '--dir',
        link,
        '--assets-dir',
        first,
      ]),
      /unsafe destination/,
    );
    const bad = join(dir, 'bad');
    await mkdir(bad);
    await assert.rejects(
      execute('sh', [
        installer,
        '--version',
        'latest',
        '--dir',
        bad,
        '--assets-dir',
        first,
      ]),
      /explicit release/,
    );
  },
);
