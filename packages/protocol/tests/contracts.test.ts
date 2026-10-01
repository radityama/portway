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
  INITIAL_STREAM_WINDOW,
  INITIAL_CONNECTION_WINDOW,
  WINDOW_UPDATE_SIZE,
  PROTOCOL_VERSION,
  HEARTBEAT_INTERVAL_MS,
  HEARTBEAT_TIMEOUT_MS,
} from '../src/index.ts';
import type { OpenStream } from '../src/index.ts';

const fixtures = JSON.parse(
  readFileSync(
    new URL('../../../tests/fixtures/protocol-v1.json', import.meta.url),
    'utf8',
  ),
);

test('WebSocket shares the optional OPEN_STREAM upgrade marker and legacy fields', () => {
  const frame = fixtures.websocket.open;
  const wire = Buffer.from(frame.wire_hex, 'hex');
  const open: OpenStream = JSON.parse(
    Buffer.from(frame.payload_hex, 'hex').toString(),
  );
  assert.equal(open.upgrade, 'websocket');
  assert.equal(open.method, 'GET');
  assert.equal(open.content_length, 0);
  assert.equal(wire[1], FRAME_TYPES.OPEN_STREAM);
  assert.equal(wire.readBigUInt64BE(4), 1n);
  assert.equal(wire.readUInt32BE(12), wire.length - FRAME_HEADER_SIZE);
  assert.deepEqual(
    wire.subarray(FRAME_HEADER_SIZE),
    Buffer.from(frame.payload_hex, 'hex'),
  );
  assert.equal(CAPABILITIES.WEBSOCKET, 'websocket');
  assert.equal(CAPABILITIES.STREAMING, 'streaming');
});

test('heartbeat fixtures share strict nonce/timestamp payloads and timing', () => {
  assert.equal(HEARTBEAT_INTERVAL_MS, fixtures.heartbeat.interval_ms);
  assert.equal(HEARTBEAT_TIMEOUT_MS, fixtures.heartbeat.timeout_ms);
  const frames = fixtures.frames.filter(
    (frame: { name: string }) => frame.name === 'PING' || frame.name === 'PONG',
  );
  assert.equal(frames.length, 2);
  assert.equal(frames[0].payload_hex, frames[1].payload_hex);
  for (const frame of frames) {
    assert.equal(frame.stream_id, '0');
    assert.deepEqual(
      JSON.parse(Buffer.from(frame.payload_hex, 'hex').toString()),
      {
        nonce: '0123456789abcdef',
        timestamp: '2026-10-01T00:00:00Z',
      },
    );
  }
});

test('GOAWAY fixture shares the connection-level shutdown contract', () => {
  const frame = fixtures.frames.find(
    (frame: { name: string }) => frame.name === 'GOAWAY',
  );
  assert.equal(frame.stream_id, '0');
  assert.deepEqual(
    JSON.parse(Buffer.from(frame.payload_hex, 'hex').toString()),
    { code: 'SHUTDOWN' },
  );
  assert.equal(STREAM_ERROR_CODES.DRAINING, 'STREAM_DRAINING');
  assert.equal(REGISTER_ERROR_CODES.DRAINING, 'REGISTER_DRAINING');
});

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

test('flow-control windows and binary updates share the Go contract', () => {
  const flow = fixtures.flow_control;
  assert.equal(INITIAL_STREAM_WINDOW, flow.initial_stream_window);
  assert.equal(INITIAL_CONNECTION_WINDOW, flow.initial_connection_window);
  assert.equal(WINDOW_UPDATE_SIZE, flow.window_update_size);
  const stream = fixtures.frames.find(
    (frame: { name: string }) => frame.name === 'WINDOW_UPDATE',
  );
  for (const fixture of [stream, flow.connection_update]) {
    const wire = Buffer.from(fixture.wire_hex, 'hex');
    assert.equal(wire[1], FRAME_TYPES.WINDOW_UPDATE);
    assert.equal(wire.readBigUInt64BE(4), BigInt(fixture.stream_id));
    assert.equal(wire.readUInt32BE(12), WINDOW_UPDATE_SIZE);
    assert.equal(wire.length, FRAME_HEADER_SIZE + WINDOW_UPDATE_SIZE);
    assert.deepEqual(
      wire.subarray(FRAME_HEADER_SIZE),
      Buffer.from(fixture.payload_hex, 'hex'),
    );
    const delta = wire.readUInt32BE(FRAME_HEADER_SIZE);
    assert.ok(
      delta > 0 &&
        delta <=
          (fixture.stream_id === '0'
            ? INITIAL_CONNECTION_WINDOW
            : INITIAL_STREAM_WINDOW),
    );
  }
  assert.equal(flow.connection_update.stream_id, '0');
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
    DRAINING: 'REGISTER_DRAINING',
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
    if (fixture.type !== FRAME_TYPES.WINDOW_UPDATE) {
      assert.equal(BigInt(fixture.stream_id) !== 0n, streamType);
    }
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
