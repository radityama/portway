import { Resolver } from 'node:dns/promises';
import { createHash, randomBytes } from 'node:crypto';
import { isIP } from 'node:net';
import { ApiFailure, text, invalid } from './validation.ts';
import type { DomainRecord, Domain } from './models.ts';
export const DOMAIN_LIMIT = 128;
export const CHALLENGE_TTL = 86_400_000;
export const ROUTE_TTL = 900_000;
export const proofHash = (value: string) =>
  createHash('sha256').update(value).digest('hex');
export function domainName(value: unknown, base: string): string {
  const host = text(value, 220).toLowerCase();
  if (
    isIP(host) ||
    !host.includes('.') ||
    !/^[a-z0-9.-]+$/.test(host) ||
    !/[a-z]/.test(host.split('.').at(-1)!) ||
    host
      .split('.')
      .some(
        (p) => !p || p.length > 63 || p.startsWith('-') || p.endsWith('-'),
      ) ||
    host === base ||
    host.endsWith('.' + base)
  )
    invalid();
  return host;
}
export function challenge(host: string, now: number) {
  const value = 'portway-verification=' + randomBytes(32).toString('base64url');
  return {
    verificationHash: proofHash(value),
    verificationExpiresAt: new Date(now + CHALLENGE_TTL).toISOString(),
    verification: {
      type: 'TXT' as const,
      name: '_portway-challenge.' + host,
      value,
    },
  };
}
export function domainDto(value: DomainRecord): Domain {
  return {
    id: value.id,
    tunnelId: value.tunnelId,
    hostname: value.hostname,
    status: value.status,
    verifiedAt: value.verifiedAt,
    verificationExpiresAt: value.verificationExpiresAt,
    createdAt: value.createdAt,
    updatedAt: value.updatedAt,
  };
}
export function verificationRequired(): never {
  throw new ApiFailure(
    409,
    'DOMAIN_VERIFICATION_REQUIRED',
    'Domain verification required',
  );
}
export function assertChallenge(domain: DomainRecord, now: number) {
  if (
    domain.status !== 'PENDING_VERIFICATION' ||
    !domain.verificationHash ||
    !domain.verificationExpiresAt ||
    Date.parse(domain.verificationExpiresAt) <= now
  )
    verificationRequired();
}
export interface TXTResolver {
  lookup(name: string): Promise<string[]>;
}
export class DNSProof implements TXTResolver {
  private active = 0;
  private readonly server: string | undefined;
  constructor(server?: string) {
    this.server = server;
    if (server) {
      try {
        // Node validates a numeric address and optional port, never a URL/name.
        const test = new Resolver();
        test.setServers([server]);
      } catch {
        throw new Error('Invalid DNS resolver configuration');
      }
    }
  }
  async lookup(name: string) {
    if (this.active >= 16)
      throw new ApiFailure(
        503,
        'DNS_UNAVAILABLE',
        'DNS verification unavailable',
      );
    this.active++;
    const resolver = new Resolver({ timeout: 1000, tries: 1 });
    if (this.server) resolver.setServers([this.server]);
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      const records = await Promise.race([
        resolver.resolveTxt(name),
        new Promise<never>((_, reject) => {
          timer = setTimeout(() => {
            resolver.cancel();
            reject(new Error());
          }, 2000);
          timer.unref();
        }),
      ]);
      if (records.length > 32) throw new Error();
      const values = records.map((parts) => {
        if (parts.length > 8) throw new Error();
        const value = parts.join('');
        if (Buffer.byteLength(value) > 512) throw new Error();
        return value;
      });
      if (values.reduce((n, v) => n + Buffer.byteLength(v), 0) > 4096)
        throw new Error();
      return values;
    } catch (error) {
      if (
        ['ENODATA', 'ENOTFOUND'].includes(
          (error as NodeJS.ErrnoException).code ?? '',
        )
      )
        return [];
      throw new ApiFailure(
        503,
        'DNS_UNAVAILABLE',
        'DNS verification unavailable',
      );
    } finally {
      clearTimeout(timer);
      this.active--;
    }
  }
}
