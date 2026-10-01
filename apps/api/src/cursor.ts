import { createHmac, randomBytes } from 'node:crypto';
import { fields, id, invalid, object } from './validation.ts';
import type { Principal } from './models.ts';
export class CursorCodec {
  private readonly key = randomBytes(32);
  private readonly now: () => number;
  constructor(now: () => number = Date.now) {
    this.now = now;
  }
  private binding(p: Principal, kind: string, filters: string) {
    return `${p.keyHash}:${p.sessionHash ?? ''}:${kind}:${filters}`;
  }
  read(
    p: Principal,
    kind: string,
    cursor: string | undefined,
    filters: string,
  ): string | undefined {
    if (cursor === undefined) return undefined;
    if (cursor.length > 1024 || !/^[A-Za-z0-9_-]+\.[a-f0-9]{64}$/.test(cursor))
      invalid();
    const [payload, signature] = cursor.split('.');
    if (
      createHmac('sha256', this.key)
        .update(this.binding(p, kind, filters) + ':' + payload)
        .digest('hex') !== signature
    )
      invalid();
    let decoded: unknown;
    try {
      decoded = JSON.parse(Buffer.from(payload!, 'base64url').toString());
    } catch {
      return invalid();
    }
    const value = object(decoded);
    fields(value, ['id', 'expiresAt']);
    if (typeof value.expiresAt !== 'number' || value.expiresAt <= this.now())
      invalid();
    return id(value.id);
  }
  write(p: Principal, kind: string, after: string, filters: string) {
    const payload = Buffer.from(
      JSON.stringify({ id: after, expiresAt: this.now() + 600_000 }),
    ).toString('base64url');
    return (
      payload +
      '.' +
      createHmac('sha256', this.key)
        .update(this.binding(p, kind, filters) + ':' + payload)
        .digest('hex')
    );
  }
}
