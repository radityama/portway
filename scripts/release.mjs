import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createHash } from 'node:crypto';
import {
  readFile,
  mkdir,
  mkdtemp,
  rename,
  rm,
  lstat,
  chmod,
} from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const execute = promisify(execFile);
export const targets = [
  'linux/amd64',
  'linux/arm64',
  'darwin/amd64',
  'darwin/arm64',
  'windows/amd64',
  'windows/arm64',
];
export function releaseOptions(args) {
  const options = {};
  for (let i = 0; i < args.length; i++) {
    const key = args[i];
    if (
      !['--version', '--out', '--targets', '--allow-dirty'].includes(key) ||
      Object.hasOwn(options, key)
    )
      throw new Error('Unknown or duplicate release option');
    if (key === '--allow-dirty') options[key] = true;
    else {
      if (!args[i + 1] || args[i + 1].startsWith('--'))
        throw new Error('Missing release option');
      options[key] = args[++i];
    }
  }
  const version = options['--version'];
  if (
    typeof version !== 'string' ||
    version.length > 64 ||
    !/^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*)?$/.test(
      version,
    )
  )
    throw new Error('Use an explicit vMAJOR.MINOR.PATCH release');
  const selected = options['--targets']?.split(',') ?? targets;
  if (
    selected.length === 0 ||
    new Set(selected).size !== selected.length ||
    selected.some((target) => !targets.includes(target))
  )
    throw new Error('Unsupported or duplicate target');
  return {
    version,
    selected,
    out: options['--out'],
    allowDirty: options['--allow-dirty'] === true,
  };
}
export async function buildRelease(
  options,
  root = fileURLToPath(new URL('../', import.meta.url)),
) {
  const output = resolve(
    root,
    options.out ?? `dist/releases/${options.version}`,
  );
  try {
    await lstat(output);
    throw new Error('Release output already exists; choose a new directory');
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }
  const git = async (args) =>
    (
      await execute('git', args, {
        cwd: root,
        timeout: 10_000,
        maxBuffer: 1024 * 1024,
      })
    ).stdout.trim();
  const revision = await git(['rev-parse', 'HEAD']);
  const dirty =
    (await git(['status', '--porcelain', '--untracked-files=normal'])) !== '';
  if (dirty && !options.allowDirty)
    throw new Error(
      'Release source is dirty; commit it or opt into --allow-dirty for local development',
    );
  const goVersion = (
    await execute('go', ['version'], { cwd: root, timeout: 10_000 })
  ).stdout.trim();
  const pin = (await readFile(join(root, '.go-version'), 'utf8')).trim();
  if (!goVersion.includes(` go${pin} `))
    throw new Error(`Release requires Go ${pin}`);
  await mkdir(dirname(output), { recursive: true });
  const temporary = await mkdtemp(join(dirname(output), '.portway-release-'));
  try {
    const artifacts = [];
    // Two compiler processes at most, including cold cross-toolchain caches.
    const jobs = options.selected.flatMap((target) =>
      ['portway', 'portway-relay', 'portway-cert'].map((binary) => ({
        target,
        binary,
      })),
    );
    const worker = async () => {
      for (;;) {
        const job = jobs.shift();
        if (!job) return;
        const [os, arch] = job.target.split('/');
        const name = `${job.binary}_${options.version}_${os}_${arch}${os === 'windows' ? '.exe' : ''}`;
        const command = {
          portway: 'portway',
          'portway-relay': 'relay',
          'portway-cert': 'certctl',
        }[job.binary];
        const file = join(temporary, name);
        await execute(
          'go',
          [
            'build',
            '-trimpath',
            '-buildvcs=false',
            '-ldflags',
            `-s -w -X main.version=${options.version} -X main.revision=${revision}`,
            '-o',
            file,
            './cmd/' + command,
          ],
          {
            cwd: root,
            env: { ...process.env, CGO_ENABLED: '0', GOOS: os, GOARCH: arch },
            timeout: 180_000,
            maxBuffer: 64 * 1024,
          },
        );
        const bytes = await readFile(file);
        if (os !== 'windows') await chmod(file, 0o755);
        artifacts.push({
          name,
          os,
          arch,
          binary: job.binary,
          bytes: bytes.length,
          sha256: createHash('sha256').update(bytes).digest('hex'),
        });
      }
    };
    // Always join both workers before removing a failed build's directory.
    const results = await Promise.allSettled([worker(), worker()]);
    for (const result of results)
      if (result.status === 'rejected') throw result.reason;
    artifacts.sort((a, b) => a.name.localeCompare(b.name, 'en'));
    const manifest = Buffer.from(
      JSON.stringify(
        { version: options.version, revision, dirty, goVersion, artifacts },
        null,
        2,
      ) + '\n',
    );
    const { writeFile } = await import('node:fs/promises');
    await writeFile(join(temporary, 'manifest.json'), manifest);
    const sums =
      [
        ...artifacts.map((item) => `${item.sha256}  ${item.name}`),
        `${createHash('sha256').update(manifest).digest('hex')}  manifest.json`,
      ]
        .sort()
        .join('\n') + '\n';
    await writeFile(join(temporary, 'SHA256SUMS'), sums);
    await rename(temporary, output);
    return { output, artifacts: artifacts.length, revision, dirty };
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
}
if (
  process.argv[1] &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  try {
    console.log(
      JSON.stringify(await buildRelease(releaseOptions(process.argv.slice(2)))),
    );
  } catch (error) {
    console.error(`Release failed: ${error.message}`);
    process.exitCode = 1;
  }
}
