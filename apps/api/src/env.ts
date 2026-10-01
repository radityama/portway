import { DNSProof } from './domains.ts';
import { existsSync } from 'node:fs';
import { loadEnvFile } from 'node:process';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';

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

  const storage = source.API_STORAGE ?? 'postgres';
  if (storage !== 'postgres' && storage !== 'memory')
    throw new Error('API_STORAGE must be postgres or memory');
  const baseDomain = source.PUBLIC_BASE_DOMAIN ?? 'portway.localhost';
  if (
    baseDomain.length > 218 ||
    !/[a-z]/.test(baseDomain) ||
    !baseDomain.includes('.') ||
    !/^[a-z0-9]+(?:[a-z0-9.-]*[a-z0-9])?$/.test(baseDomain) ||
    baseDomain
      .split('.')
      .some((s) => !s || s.length > 63 || s.startsWith('-') || s.endsWith('-'))
  )
    throw new Error('PUBLIC_BASE_DOMAIN must be a DNS name');
  const credentialTTL = Number(source.API_CREDENTIAL_TTL_SECONDS ?? '300');
  if (
    !Number.isInteger(credentialTTL) ||
    credentialTTL < 1 ||
    credentialTTL > 900
  )
    throw new Error('API_CREDENTIAL_TTL_SECONDS must be between 1 and 900');
  const root = fileURLToPath(new URL('../../../', import.meta.url));
  const defaultSeed = resolve(root, '.tmp/dev/control-seed.json');
  const configuredSeed =
    source.API_SEED_FILE ?? (existsSync(defaultSeed) ? defaultSeed : '');
  if (source.API_DNS_SERVER) new DNSProof(source.API_DNS_SERVER);
  return {
    dnsServer: source.API_DNS_SERVER,
    apiPort,
    storage,
    seedFile: configuredSeed ? resolve(root, configuredSeed) : '',
    credentialTTL: credentialTTL * 1000,
    databaseUrl: source.DATABASE_URL ?? '',
    redisUrl: source.REDIS_URL ?? 'redis://localhost:6379',
    publicBaseDomain: baseDomain,
  };
}

const envPath = fileURLToPath(new URL('../../../.env', import.meta.url));
if (existsSync(envPath)) loadEnvFile(envPath);
export const env = parseEnvironment(process.env);
