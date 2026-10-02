import assert from 'node:assert/strict';
import { request } from 'node:https';
import { createHash } from 'node:crypto';

// Consume responses incrementally. Payloads and response storage stay bounded
// independently of workload size; callers may verify a digest without buffering.
export function traffic(
  url,
  ca,
  {
    method = 'GET',
    size = 0,
    chunked = false,
    id = 'probe',
    onHeaders,
    timeout = 20_000,
  } = {},
) {
  const started = performance.now();
  return new Promise((resolve, reject) => {
    const target = new URL(url);
    const req = request(
      {
        hostname: '127.0.0.1',
        port: target.port,
        servername: target.hostname,
        path: target.pathname + target.search,
        method,
        ca,
        minVersion: 'TLSv1.3',
        agent: false,
        headers: {
          Host: target.host,
          'X-Load-ID': id,
          ...(chunked ? {} : { 'Content-Length': size }),
        },
      },
      (res) => {
        onHeaders?.(res);
        const hash = createHash('sha256');
        let length = 0,
          small = Buffer.alloc(0);
        res.on('data', (part) => {
          length += part.length;
          hash.update(part);
          if (length <= 1024) small = Buffer.concat([small, part]);
          else small = Buffer.alloc(0);
          if (length > 64 * 1024 * 1024)
            req.destroy(new Error('Response exceeds test limit'));
        });
        res.on('error', reject);
        res.on('end', () =>
          resolve({
            status: res.statusCode,
            length,
            digest: hash.digest('hex'),
            small: small.toString(),
            durationMS: performance.now() - started,
          }),
        );
      },
    );
    // An absolute deadline complements the socket's idle timeout; impairment
    // and upload writes must not outlive a scenario when progress continues.
    const deadline = setTimeout(
      () => req.destroy(new Error('Traffic deadline')),
      timeout,
    );
    req.on('close', () => clearTimeout(deadline));
    req.on('error', reject);
    req.setTimeout(timeout, () =>
      req.destroy(new Error('Traffic idle deadline')),
    );
    const chunk = Buffer.alloc(16 * 1024, 0x61);
    let remaining = size;
    const write = () => {
      while (remaining > 0 && !req.destroyed) {
        const part = chunk.subarray(0, Math.min(chunk.length, remaining));
        remaining -= part.length;
        if (!req.write(part)) {
          req.once('drain', write);
          return;
        }
      }
      if (!req.destroyed) req.end();
    };
    write();
  });
}

export function payloadDigest(size) {
  const hash = createHash('sha256');
  const chunk = Buffer.alloc(16 * 1024, 0x61);
  while (size > 0) {
    const length = Math.min(chunk.length, size);
    hash.update(chunk.subarray(0, length));
    size -= length;
  }
  return hash.digest('hex');
}

export async function workload(count, concurrency, operation) {
  assert.ok(Number.isInteger(count) && count >= 1 && count <= 10_000);
  assert.ok(
    Number.isInteger(concurrency) && concurrency >= 1 && concurrency <= 128,
  );
  const started = performance.now(),
    durations = [];
  let next = 0;
  const results = await Promise.allSettled(
    Array.from({ length: Math.min(count, concurrency) }, async () => {
      while (next < count) {
        const index = next++;
        const result = await operation(index);
        durations.push(result.durationMS);
      }
    }),
  );
  // Join every worker even after one fails, so fixture cleanup cannot race
  // pending requests and rejected promises cannot escape the test harness.
  for (const result of results)
    if (result.status === 'rejected') throw result.reason;
  const elapsedMS = performance.now() - started;
  durations.sort((a, b) => a - b);
  const percentile = (p) => durations[Math.ceil(p * durations.length) - 1];
  return {
    requests: count,
    concurrency,
    elapsedMS,
    requestsPerSecond: (count * 1000) / elapsedMS,
    latencyMS: {
      p50: percentile(0.5),
      p95: percentile(0.95),
      p99: percentile(0.99),
      max: durations.at(-1),
    },
  };
}
