import { createSocket } from 'node:dgram';
export async function dnsFixture() {
  const socket = createSocket('udp4'),
    records = new Map();
  const state = { records, paused: false, queries: 0 };
  socket.on('message', (query, peer) => {
    if (
      query.length < 17 ||
      query.length > 1024 ||
      query.readUInt16BE(4) !== 1 ||
      state.paused
    )
      return;
    let offset = 12;
    const labels = [];
    while (offset < query.length && query[offset]) {
      const count = query[offset++];
      if (count > 63 || offset + count > query.length) return;
      labels.push(query.subarray(offset, offset + count).toString('ascii'));
      offset += count;
    }
    if (offset + 5 > query.length) return;
    offset++;
    if (query.readUInt16BE(offset) !== 16) return;
    offset += 4;
    state.queries++;
    const values = records.get(labels.join('.')) ?? [],
      header = Buffer.from(query.subarray(0, 12));
    header.writeUInt16BE(0x8180, 2);
    header.writeUInt16BE(values.length, 6);
    header.writeUInt16BE(0, 8);
    header.writeUInt16BE(0, 10);
    const answers = values.map((value) => {
      const text = Buffer.from(value),
        chunks = [];
      for (let n = 0; n < text.length; n += 200) {
        const part = text.subarray(n, n + 200);
        chunks.push(Buffer.from([part.length]), part);
      }
      const data = Buffer.concat(chunks),
        answer = Buffer.alloc(12);
      answer.writeUInt16BE(0xc00c, 0);
      answer.writeUInt16BE(16, 2);
      answer.writeUInt16BE(1, 4);
      answer.writeUInt32BE(1, 6);
      answer.writeUInt16BE(data.length, 10);
      return Buffer.concat([answer, data]);
    });
    socket.send(
      Buffer.concat([header, query.subarray(12, offset), ...answers]),
      peer.port,
      peer.address,
    );
  });
  await new Promise((resolve) => socket.bind(0, '127.0.0.1', resolve));
  return {
    ...state,
    records,
    get queries() {
      return state.queries;
    },
    set paused(value) {
      state.paused = value;
    },
    server: '127.0.0.1:' + socket.address().port,
    close: () => new Promise((resolve) => socket.close(resolve)),
  };
}
