import { randomBytes, createHash } from 'node:crypto';
import {
  existsSync,
  mkdirSync,
  writeFileSync,
  chmodSync,
  renameSync,
} from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { loadEnvironment } from './environment.mjs';

// Private development provisioning only. Runtime mutations are process-local
// until Phase 10; this file is never rewritten by the API.
export function initializeControl(root, source = process.env) {
  const directory = join(root, '.tmp/dev');
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  chmodSync(directory, 0o700);
  const seedPath = join(directory, 'control-seed.json');
  if (existsSync(seedPath)) {
    for (const name of ['api-token', 'relay-api-token'])
      if (!existsSync(join(directory, name)))
        throw new Error('Development control-plane credentials are incomplete');
    return false;
  }
  const port = Number(source.RELAY_PORT ?? '8081');
  const base = source.PUBLIC_BASE_DOMAIN ?? 'portway.localhost';
  if (
    !Number.isInteger(port) ||
    port < 1 ||
    port > 65535 ||
    !/^[a-z0-9.-]+$/.test(base)
  )
    throw new Error('Invalid development relay configuration');
  const hash = (value) => createHash('sha256').update(value).digest('hex');
  const apiToken = randomBytes(32).toString('base64url');
  const relayToken = randomBytes(32).toString('base64url');
  const now = new Date().toISOString();
  const expires = new Date(Date.now() + 7 * 24 * 3600_000).toISOString();
  const seed = {
    users: [
      {
        id: 'usr_local',
        email: 'developer@portway.localhost',
        displayName: 'Local developer',
        createdAt: now,
        updatedAt: now,
      },
    ],
    organizations: [
      { id: 'org_local', name: 'Local development', slug: 'local' },
    ],
    memberships: [
      { userId: 'usr_local', organizationId: 'org_local', role: 'OWNER' },
    ],
    apiKeys: [
      {
        id: 'key_local',
        userId: 'usr_local',
        organizationId: 'org_local',
        tokenHash: hash(apiToken),
        expiresAt: expires,
      },
    ],
    projects: [
      {
        id: 'prj_local',
        organizationId: 'org_local',
        name: 'Local development',
        slug: 'local',
        createdAt: now,
        updatedAt: now,
      },
    ],
    tunnels: [
      {
        id: 'tnl_local_dev',
        projectId: 'prj_local',
        name: 'Local service',
        slug: 'local-service',
        type: 'EPHEMERAL',
        status: 'CREATED',
        protocol: 'http',
        localHost: '127.0.0.1',
        localPort: 3000,
        publicHostname: `p-${hash('tnl_local_dev').slice(0, 32)}.${base}`,
        relayId: null,
        generation: '0',
        createdAt: now,
        updatedAt: now,
        lastConnectedAt: null,
      },
    ],
    relays: [
      {
        id: 'rel_local',
        name: 'Local relay',
        region: 'local',
        hostname: 'localhost',
        port,
        protocol: 'tls',
        status: 'HEALTHY',
        lastSeenAt: null,
      },
    ],
    relayKeys: [
      { relayId: 'rel_local', tokenHash: hash(relayToken), expiresAt: expires },
    ],
  };
  // Publish the seed last so the API never loads partially provisioned keys.
  for (const [name, content] of [
    ['api-token', apiToken + '\n'],
    ['relay-api-token', relayToken + '\n'],
    ['control-seed.json', JSON.stringify(seed, null, 2) + '\n'],
  ]) {
    const temporary = join(
      directory,
      `${name}.${randomBytes(8).toString('hex')}.tmp`,
    );
    writeFileSync(temporary, content, { mode: 0o600, flag: 'wx' });
    renameSync(temporary, join(directory, name));
  }
  return true;
}
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const root = fileURLToPath(new URL('../', import.meta.url));
    loadEnvironment(root);
    console.log(
      initializeControl(root)
        ? 'Private development control-plane credentials created (valid for 7 days).'
        : 'Existing development control-plane credentials preserved.',
    );
  } catch {
    console.error('Control-plane development provisioning failed');
    process.exitCode = 1;
  }
}
