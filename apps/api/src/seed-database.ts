import { databaseClient } from './database.ts';
import { provision } from './provision.ts';
import { loadSeed } from './seed.ts';
import { env } from './env.ts';

const client = databaseClient(env.databaseUrl);
try {
  const seed = loadSeed(env.seedFile);
  if (!seed) throw new Error();
  await provision(client, seed, env.publicBaseDomain);
  console.log(JSON.stringify({ event: 'database_seed_complete' }));
} catch {
  console.error(JSON.stringify({ event: 'database_seed_failed' }));
  process.exitCode = 1;
} finally {
  await client.$disconnect();
}
