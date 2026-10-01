import { test } from 'node:test';
import { runControlScenario } from '../testing/control.mjs';
test(
  'real API → CLI → relay: HTTPS/SSE survive API outage; expiry reconnects after restart; revocation stops bootstrap',
  { timeout: 45_000 },
  () => runControlScenario(),
);
