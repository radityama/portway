import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createSocket } from 'node:dgram';
import { DNSProof, domainName } from '../src/domains.ts';
test('domain names reject reserved space and unsafe DNS authorities', () => {
  assert.equal(
    domainName('App.Example.test', 'portway.localhost'),
    'app.example.test',
  );
  for (const value of [
    '*.example.test',
    'portway.localhost',
    'p-abc.portway.localhost',
    '127.0.0.1',
    'localhost',
    'http://app.example.test',
    'app.example.test:443',
    'app.example.test.',
    'app..test',
    'app_1.test',
    'a.123',
    '-x.test',
    'x-.test',
    'a'.repeat(64) + '.test',
    ['app.example.test'],
  ])
    assert.throws(() => domainName(value, 'portway.localhost'));
  assert.throws(() => new DNSProof('https://attacker.example.test/'));
});
test(
  'real DNS lookup is bounded, supports split TXT records, and releases saturated work',
  { timeout: 10_000 },
  async () => {
    const socket = createSocket('udp4');
    await new Promise<void>((resolve) => socket.bind(0, '127.0.0.1', resolve));
    let pause = false;
    socket.on('message', (message, peer) => {
      if (pause) return;
      const header = Buffer.from(message.subarray(0, 12));
      header.writeUInt16BE(0x8180, 2);
      header.writeUInt16BE(1, 6);
      const text = Buffer.from('portway-verification=proof');
      const data = Buffer.concat([
        Buffer.from([10]),
        text.subarray(0, 10),
        Buffer.from([text.length - 10]),
        text.subarray(10),
      ]);
      const answer = Buffer.alloc(12);
      answer.writeUInt16BE(0xc00c, 0);
      answer.writeUInt16BE(16, 2);
      answer.writeUInt16BE(1, 4);
      answer.writeUInt32BE(1, 6);
      answer.writeUInt16BE(data.length, 10);
      socket.send(
        Buffer.concat([header, message.subarray(12), answer, data]),
        peer.port,
        peer.address,
      );
    });
    const address = socket.address();
    assert.equal(typeof address, 'object');
    const resolver = new DNSProof('127.0.0.1:' + address.port);
    try {
      assert.deepEqual(
        await resolver.lookup('_portway-challenge.app.example.test'),
        ['portway-verification=proof'],
      );
      pause = true;
      const start = Date.now();
      const pending = Array.from({ length: 16 }, () =>
        resolver.lookup('_portway-challenge.app.example.test'),
      );
      await assert.rejects(
        resolver.lookup('_portway-challenge.app.example.test'),
        { code: 'DNS_UNAVAILABLE' },
      );
      assert.ok(
        (await Promise.allSettled(pending)).every(
          (r) => r.status === 'rejected',
        ),
      );
      assert.ok(Date.now() - start < 2500);
      pause = false;
      assert.equal(
        (await resolver.lookup('_portway-challenge.app.example.test')).length,
        1,
      );
    } finally {
      socket.close();
    }
  },
);
