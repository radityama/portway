import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

test('the Prisma schema matches the logical baseline documented in DATABASE.md', () => {
  const documented = readFileSync(
    new URL('../../docs/DATABASE.md', import.meta.url),
    'utf8',
  );
  const block = documented.split('```prisma\n')[1]?.split('```')[0];
  assert.ok(block, 'DATABASE.md must contain the Prisma baseline');
  const implemented = readFileSync(
    new URL('../../prisma/schema.prisma', import.meta.url),
    'utf8',
  );
  const normalize = (text) => text.replace(/\s+/g, ' ').trim();
  assert.equal(normalize(implemented), normalize(block));
});
