import assert from 'node:assert/strict';
import { test } from 'node:test';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, writeFile, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildRelease, releaseOptions } from '../release.mjs';

const execute = promisify(execFile);
const root = fileURLToPath(new URL('../../', import.meta.url));
const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex');

test(
  'native installer verifies assets, replaces atomically and preserves the installed binary on rejection',
  { timeout: 180_000 },
  async (t) => {
    const os = { linux: 'linux', darwin: 'darwin', win32: 'windows' }[
      process.platform
    ];
    assert.ok(os, 'Unsupported installer test platform');
    assert.ok(['x64', 'arm64'].includes(process.arch));
    const target = `${os}/${process.arch === 'arm64' ? 'arm64' : 'amd64'}`;
    const version = process.env.PORTWAY_TEST_RELEASE_VERSION ?? 'v0.0.0-native';
    const fixture = await mkdtemp(join(tmpdir(), 'portway-native-'));
    t.after(() => rm(fixture, { recursive: true, force: true }));
    const assets = join(fixture, 'assets');
    const destination = join(fixture, 'bin');
    await buildRelease(
      releaseOptions([
        '--version',
        version,
        '--targets',
        target,
        '--out',
        assets,
        '--allow-dirty',
      ]),
      root,
    );
    const manifest = JSON.parse(
      await readFile(join(assets, 'manifest.json'), 'utf8'),
    );
    assert.equal(manifest.artifacts.length, 3);
    for (const item of manifest.artifacts) {
      const bytes = await readFile(join(assets, item.name));
      assert.equal(sha256(bytes), item.sha256);
      assert.equal(bytes.length, item.bytes);
    }
    const artifact = manifest.artifacts.find(
      (item) => item.binary === 'portway',
    );
    const executable = join(
      destination,
      os === 'windows' ? 'portway.exe' : 'portway',
    );
    const checksumPath = join(assets, 'SHA256SUMS');
    const sums = await readFile(checksumPath);
    const pinned = sha256(sums);
    const install = (digest = pinned) =>
      execute(
        os === 'windows' ? 'pwsh' : 'sh',
        os === 'windows'
          ? [
              '-NoLogo',
              '-NoProfile',
              '-NonInteractive',
              '-File',
              join(root, 'install/install.ps1'),
              '-Version',
              version,
              '-Directory',
              destination,
              '-AssetsDirectory',
              assets,
              '-ChecksumsSha256',
              digest,
            ]
          : [
              join(root, 'install/install.sh'),
              '--version',
              version,
              '--dir',
              destination,
              '--assets-dir',
              assets,
              '--checksums-sha256',
              digest,
            ],
        { timeout: 15_000, maxBuffer: 65536 },
      );
    await install();
    const original = await readFile(executable);
    assert.equal(sha256(original), artifact.sha256);
    const stamp = JSON.parse(
      (await execute(executable, ['version', '--json'], { timeout: 5000 }))
        .stdout,
    ).data;
    assert.equal(stamp.version, version);
    assert.equal(stamp.revision, manifest.revision);
    await install();
    assert.deepEqual(await readFile(executable), original);
    await writeFile(join(assets, artifact.name), 'corrupted');
    await assert.rejects(install(), /checksum mismatch/i);
    assert.deepEqual(await readFile(executable), original);
    await assert.rejects(install('0'.repeat(64)), /trusted digest/i);
    assert.deepEqual(await readFile(executable), original);
    await writeFile(join(assets, artifact.name), original);
    const line = sums
      .toString('utf8')
      .split('\n')
      .find((line) => line.endsWith('  ' + artifact.name));
    await writeFile(checksumPath, sums.toString('utf8') + line + '\n');
    await assert.rejects(install(''), /duplicate/i);
    assert.deepEqual(await readFile(executable), original);
    assert.deepEqual(await readdir(destination), [
      os === 'windows' ? 'portway.exe' : 'portway',
    ]);
  },
);
