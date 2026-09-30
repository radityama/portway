import { fileURLToPath } from 'node:url';
import { checkToolchains } from './toolchains.mjs';
import { ensureEnvironment } from './environment.mjs';
import { ProcessSupervisor } from './supervisor.mjs';

const root = fileURLToPath(new URL('../', import.meta.url));
const supervisor = new ProcessSupervisor();
try {
  checkToolchains(root);
  if (ensureEnvironment(root)) console.log('Created .env from .env.example.');
  await supervisor.run('pnpm', ['install', '--frozen-lockfile'], { cwd: root });
  await supervisor.run('pnpm', ['db:generate'], { cwd: root });
  await supervisor.run('go', ['run', './cmd/dev-init'], { cwd: root });
  console.log('Portway setup complete. Start development with make dev.');
} catch (error) {
  console.error(`Portway setup failed: ${error.message}`);
  process.exitCode = 1;
} finally {
  await supervisor.stop();
}
