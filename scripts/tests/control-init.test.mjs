import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtempSync, readFileSync, statSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { initializeControl } from '../control-init.mjs';

test('control development setup creates distinct private hashed keys and preserves existing provisioning', () => {
  const root = mkdtempSync(join(tmpdir(), 'portway-control-'));
  try {
    assert.equal(
      initializeControl(root, {
        RELAY_PORT: '9091',
        PUBLIC_BASE_DOMAIN: 'portway.localhost',
      }),
      true,
    );
    const directory = join(root, '.tmp/dev');
    const seedText = readFileSync(join(directory, 'control-seed.json'), 'utf8');
    const seed = JSON.parse(seedText);
    const api = readFileSync(join(directory, 'api-token'), 'utf8').trim(),
      relay = readFileSync(join(directory, 'relay-api-token'), 'utf8').trim();
    assert.notEqual(api, relay);
    assert.ok(!seedText.includes(api) && !seedText.includes(relay));
    assert.equal(
      seed.apiKeys[0].tokenHash,
      createHash('sha256').update(api).digest('hex'),
    );
    assert.equal(seed.relays[0].port, 9091);
    if (process.platform !== 'win32')
      for (const name of ['api-token', 'relay-api-token', 'control-seed.json'])
        assert.equal(statSync(join(directory, name)).mode & 0o077, 0);
    assert.equal(initializeControl(root, { RELAY_PORT: '8081' }), false);
    assert.equal(
      readFileSync(join(directory, 'control-seed.json'), 'utf8'),
      seedText,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
