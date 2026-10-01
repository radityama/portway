import { copyFileSync, constants, existsSync } from 'node:fs';
import { loadEnvFile } from 'node:process';
import { join } from 'node:path';

export function ensureEnvironment(root) {
  const target = join(root, '.env');
  if (existsSync(target)) return false;
  copyFileSync(join(root, '.env.example'), target, constants.COPYFILE_EXCL);
  return true;
}

export function loadEnvironment(root) {
  const path = join(root, '.env');
  if (existsSync(path)) loadEnvFile(path);
}

export function developmentPorts(source) {
  const defaults = {
    API_PORT: 8080,
    DASHBOARD_PORT: 3000,
    RELAY_PORT: 8081,
    PUBLIC_PORT: 8443,
    POSTGRES_PORT: 5432,
    REDIS_PORT: 6379,
  };
  const ports = {};
  for (const [key, fallback] of Object.entries(defaults)) {
    const port = Number(source[key] ?? fallback);
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      throw new Error(`${key} must be an integer between 1 and 65535`);
    }
    if (Object.values(ports).includes(port)) {
      throw new Error(`${key} conflicts with another development service port`);
    }
    ports[key] = port;
  }
  return ports;
}
