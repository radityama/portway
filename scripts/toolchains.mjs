import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

export function checkToolchains(root, { docker = false } = {}) {
  const nodePin = readFileSync(join(root, '.node-version'), 'utf8').trim();
  const actualNode = process.versions.node.split('.').map(Number);
  const requiredNode = nodePin.split('.').map(Number);
  if (
    actualNode[0] !== requiredNode[0] ||
    actualNode[1] < requiredNode[1] ||
    (actualNode[1] === requiredNode[1] && actualNode[2] < requiredNode[2])
  ) {
    throw new Error(`Use Node.js ${nodePin} (see .node-version)`);
  }

  const go = output('go', ['version'], root);
  const goVersion = /go version go(\d+)\.(\d+)/.exec(go);
  if (!goVersion || Number(goVersion[1]) !== 1 || Number(goVersion[2]) < 26) {
    throw new Error('A Go 1.26+ toolchain is required (see .go-version)');
  }

  const { packageManager } = JSON.parse(
    readFileSync(join(root, 'package.json'), 'utf8'),
  );
  const pnpmPin = packageManager.split('@')[1];
  if (output('pnpm', ['--version'], root) !== pnpmPin) {
    throw new Error(
      `Use pnpm ${pnpmPin}; enable the package-manager shim with corepack enable`,
    );
  }

  if (docker) {
    output('docker', ['compose', 'version'], root);
    output('docker', ['info', '--format', '{{.ServerVersion}}'], root);
  }
}

function output(command, args, cwd) {
  const result = spawnSync(command, args, {
    cwd,
    encoding: 'utf8',
    timeout: 15_000,
  });
  if (result.error || result.status !== 0) {
    throw new Error(
      `${command} is unavailable or failed; check its installation${command === 'docker' ? ' and daemon' : ''}`,
    );
  }
  return result.stdout.trim();
}
