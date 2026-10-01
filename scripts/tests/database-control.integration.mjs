import { test } from 'node:test';
import { databaseFixture } from '../testing/database.mjs';
import { runControlScenario } from '../testing/control.mjs';
test(
  'real PostgreSQL → API → CLI → relay: durable restart, database outage isolation and terminal revocation',
  { timeout: 90_000 },
  async () => {
    const database = await databaseFixture();
    try {
      await runControlScenario(database);
    } finally {
      await database.close();
    }
  },
);
