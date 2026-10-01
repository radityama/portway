import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import {
  CAPABILITIES,
  FRAME_HEADER_SIZE,
  FRAME_TYPES,
  MAX_CAPABILITIES,
  MAX_CAPABILITY_NAME_SIZE,
  MAX_HANDSHAKE_PAYLOAD_SIZE,
  MAX_PAYLOAD_SIZE,
  MAX_TUNNEL_ID_SIZE,
  REGISTER_ERROR_CODES,
  MAX_DATA_SIZE,
  MAX_OPEN_PAYLOAD_SIZE,
  MAX_HTTP_HEADER_SIZE,
  STREAM_ERROR_CODES,
  PROTOCOL_VERSION,
} from '../src/index.ts';

const fixtures = JSON.parse(
  readFileSync(
    new URL('../../../tests/fixtures/protocol-v1.json', import.meta.url),
    'utf8',
  ),
);

test('TypeScript shares the Go wire constants and bounded handshake contract', () => {
  assert.equal(PROTOCOL_VERSION, fixtures.version);
  assert.equal(FRAME_HEADER_SIZE, fixtures.header_size);
  assert.equal(MAX_PAYLOAD_SIZE, fixtures.max_payload_size);
  assert.equal(MAX_HANDSHAKE_PAYLOAD_SIZE, fixtures.max_handshake_payload_size);
  assert.equal(MAX_CAPABILITIES, fixtures.max_capabilities);
  assert.equal(MAX_CAPABILITY_NAME_SIZE, fixtures.max_capability_name_size);
  assert.deepEqual(CAPABILITIES, fixtures.capabilities);
  assert.deepEqual(
    FRAME_TYPES,
    Object.fromEntries(
      fixtures.frames.map((frame: { name: string; type: number }) => [
        frame.name,
        frame.type,
      ]),
    ),
  );
});

test('HTTP stream fixtures share limits and canonical metadata', () => {
  const payload = (name: string) =>
    JSON.parse(
      Buffer.from(
        fixtures.frames.find((frame: { name: string }) => frame.name === name)
          .payload_hex,
        'hex',
      ).toString('utf8'),
    );
  assert.equal(MAX_DATA_SIZE, 16384);
  assert.equal(MAX_OPEN_PAYLOAD_SIZE, 65536);
  assert.equal(MAX_HTTP_HEADER_SIZE, 32768);
  assert.deepEqual(payload('OPEN_STREAM'), {
    method: 'POST',
    target: '/echo?x=1',
    host: 'p-abc.portway.localhost',
    headers: [['Content-Type', 'application/json']],
    content_length: 3,
  });
  assert.equal(
    payload('OPEN_STREAM_ERROR').code,
    STREAM_ERROR_CODES.UNAVAILABLE,
  );
  assert.equal(payload('RESET_STREAM').code, STREAM_ERROR_CODES.CANCELLED);
});

test('registration preserves all 64 generation bits and shares error codes', () => {
  const payload = (name: string) =>
    JSON.parse(
      Buffer.from(
        fixtures.frames.find((frame: { name: string }) => frame.name === name)
          .payload_hex,
        'hex',
      ).toString('utf8'),
    );
  const request = payload('REGISTER');
  const ack = payload('REGISTER_OK');
  assert.equal(typeof request.generation, 'string');
  assert.equal(BigInt(request.generation), (1n << 64n) - 1n);
  assert.equal(ack.generation, request.generation);
  assert.equal(ack.tunnel_id, request.tunnel_id);
  assert.equal(MAX_TUNNEL_ID_SIZE, 128);
  assert.deepEqual(REGISTER_ERROR_CODES, {
    INVALID: 'REGISTER_INVALID',
    FORBIDDEN: 'REGISTER_FORBIDDEN',
    STALE: 'REGISTER_STALE',
    CAPACITY: 'REGISTER_CAPACITY',
    CONFLICT: 'REGISTER_CONFLICT',
  });
  assert.equal(payload('REGISTER_ERROR').code, REGISTER_ERROR_CODES.STALE);
});

test('all shared fixtures have exact network-order headers and payload bytes', () => {
  for (const fixture of fixtures.frames) {
    const wire = Buffer.from(fixture.wire_hex, 'hex');
    const payload = Buffer.from(fixture.payload_hex, 'hex');
    assert.equal(wire[0], PROTOCOL_VERSION);
    assert.equal(wire[1], fixture.type);
    assert.equal(wire[2], 0);
    assert.equal(wire[3], 0);
    assert.equal(wire.readBigUInt64BE(4), BigInt(fixture.stream_id));
    assert.equal(wire.readUInt32BE(12), payload.length);
    assert.equal(wire.length, FRAME_HEADER_SIZE + payload.length);
    assert.deepEqual(wire.subarray(FRAME_HEADER_SIZE), payload);
    const streamType = fixture.type >= 0x10 && fixture.type <= 0x16;
    assert.equal(BigInt(fixture.stream_id) !== 0n, streamType);
  }
  const data = fixtures.frames.find(
    (frame: { name: string }) => frame.name === 'DATA',
  );
  assert.equal(BigInt(data.stream_id), (1n << 64n) - 1n);
});

test('documented frame values match TypeScript without filling unassigned IDs', () => {
  const doc = readFileSync(
    new URL('../../../docs/PROTOCOL.md', import.meta.url),
    'utf8',
  );
  const types = Object.fromEntries(
    [...doc.matchAll(/^0x([0-9A-F]{2}) ([A-Z_]+)$/gm)].map((match) => [
      match[2],
      Number.parseInt(match[1], 16),
    ]),
  );
  assert.deepEqual(types, FRAME_TYPES);
});
