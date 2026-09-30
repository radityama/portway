import assert from 'node:assert/strict';
import { test } from 'node:test';
import { parseEnvironment } from '../src/env.ts';

test('API environment validates ports before opening a listener', () => {
  assert.equal(parseEnvironment({ API_PORT: '9090' }).apiPort, 9090);
  for (const API_PORT of ['', 'zero', '0', '-1', '65536', '8080.5']) {
    assert.throws(() => parseEnvironment({ API_PORT }), /API_PORT/);
  }
});

test('invalid connection URLs fail without including credentials in the error', () => {
  for (const key of ['DATABASE_URL', 'REDIS_URL']) {
    assert.throws(
      () =>
        parseEnvironment({ [key]: 'http://user:private-secret@example.com' }),
      (error) => {
        assert.ok(error instanceof Error);
        assert.ok(error.message.includes(key));
        assert.ok(!error.message.includes('private-secret'));
        return true;
      },
    );
  }
  assert.equal(
    parseEnvironment({ REDIS_URL: 'rediss://localhost:6379' }).redisUrl,
    'rediss://localhost:6379',
  );
});
