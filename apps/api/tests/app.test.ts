import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createApp } from '../src/app.ts';

test('health returns the documented response envelope', async () => {
  const response = await createApp().request('/health');
  assert.equal(response.status, 200);
  assert.match(response.headers.get('content-type') ?? '', /application\/json/);
  assert.deepEqual(await response.json(), {
    data: { status: 'ok' },
    error: null,
    meta: {},
  });
});

test('unconfigured authentication fails closed', async () => {
  const response = await createApp().request('/api/v1/me');
  assert.equal(response.status, 401);
  const body = await response.json();
  assert.equal(body.data, null);
  assert.equal(body.error.code, 'AUTH_INVALID');
});
