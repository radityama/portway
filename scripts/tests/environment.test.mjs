import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import {
  developmentPorts,
  ensureEnvironment,
  loadEnvironment,
} from '../environment.mjs';

test('setup creates an environment file once and preserves local changes', () => {
  const root = mkdtempSync(join(tmpdir(), 'portway-env-'));
  try {
    writeFileSync(join(root, '.env.example'), 'API_PORT=8080\n');
    assert.equal(ensureEnvironment(root), true);
    assert.equal(readFileSync(join(root, '.env'), 'utf8'), 'API_PORT=8080\n');
    writeFileSync(join(root, '.env'), 'API_PORT=9090\n');
    assert.equal(ensureEnvironment(root), false);
    assert.equal(readFileSync(join(root, '.env'), 'utf8'), 'API_PORT=9090\n');
  } finally {
    rmSync(root, { recursive: true });
  }
});

test('shell variables take precedence over values in the environment file', () => {
  const root = mkdtempSync(join(tmpdir(), 'portway-env-'));
  const key = 'PORTWAY_TEST_ENV_PRECEDENCE';
  const previous = process.env[key];
  try {
    writeFileSync(join(root, '.env'), `${key}=from-file\n`);
    process.env[key] = 'from-shell';
    loadEnvironment(root);
    assert.equal(process.env[key], 'from-shell');
  } finally {
    if (previous === undefined) delete process.env[key];
    else process.env[key] = previous;
    rmSync(root, { recursive: true });
  }
});

test('development ports reject invalid numbers and collisions before startup', () => {
  for (const value of ['0', '65536', 'abc', '8080.5', '']) {
    assert.throws(() => developmentPorts({ API_PORT: value }), /API_PORT/);
  }
  assert.throws(
    () => developmentPorts({ DASHBOARD_PORT: '8080' }),
    /conflicts/,
  );
  assert.equal(developmentPorts({ API_PORT: '9090' }).API_PORT, 9090);
});
