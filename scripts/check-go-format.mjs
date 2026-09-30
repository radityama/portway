import { spawnSync } from 'node:child_process';

const result = spawnSync('gofmt', ['-l', 'cmd', 'internal', 'tests'], {
  encoding: 'utf8',
});
if (result.error || result.status !== 0) {
  console.error('gofmt is unavailable or failed. Install the Go toolchain.');
  process.exitCode = 1;
} else if (result.stdout.trim()) {
  console.error(`Run make fmt to format:\n${result.stdout.trim()}`);
  process.exitCode = 1;
}
