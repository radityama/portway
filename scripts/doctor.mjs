import { fileURLToPath } from 'node:url';
import { checkToolchains } from './toolchains.mjs';

try {
  checkToolchains(fileURLToPath(new URL('../', import.meta.url)), {
    docker: true,
  });
  console.log('Portway development toolchains and Docker are available.');
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
