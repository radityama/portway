import { existsSync } from 'node:fs';
import { loadEnvFile } from 'node:process';
import { fileURLToPath } from 'node:url';

export function parseEnvironment(source: NodeJS.ProcessEnv) {
  const apiPort = Number(source.API_PORT ?? '8080');
  if (!Number.isInteger(apiPort) || apiPort < 1 || apiPort > 65535) {
    throw new Error('API_PORT must be an integer between 1 and 65535');
  }

  for (const [key, protocols] of [
    ['DATABASE_URL', ['postgres:', 'postgresql:']],
    ['REDIS_URL', ['redis:', 'rediss:']],
  ] as const) {
    if (!source[key]) continue;
    try {
      const url = new URL(source[key]);
      if (
        !(protocols as readonly string[]).includes(url.protocol) ||
        !url.hostname
      ) {
        throw new Error('Invalid URL');
      }
    } catch {
      throw new Error(`${key} must be a valid ${protocols.join(' or ')} URL`);
    }
  }

  return {
    apiPort,
    databaseUrl: source.DATABASE_URL ?? '',
    redisUrl: source.REDIS_URL ?? 'redis://localhost:6379',
    publicBaseDomain: source.PUBLIC_BASE_DOMAIN ?? 'portway.localhost',
  };
}

const envPath = fileURLToPath(new URL('../../../.env', import.meta.url));
if (existsSync(envPath)) loadEnvFile(envPath);
export const env = parseEnvironment(process.env);
