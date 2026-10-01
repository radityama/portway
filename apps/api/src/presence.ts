import { randomBytes, createHash } from 'node:crypto';
import { createClient } from 'redis';
import { parseObservations, type Observation } from './observations.ts';
import type { Relay } from './models.ts';
import { ApiFailure, fields, invalid, id, hash } from './validation.ts';
export const PRESENCE_TTL = 15_000;
export type Capacity = {
  activeConnections: number;
  activeTunnels: number;
  retainedTunnels: number;
  activeStreams: number;
  maxConnections: number;
  maxTunnels: number;
  maxStreams: number;
};
export type Report = Capacity & {
  observations?: Observation[];
  instanceId: string;
  status: 'HEALTHY' | 'DEGRADED' | 'DRAINING';
};
export type Presence = Report & {
  relayId: string;
  leaseId: string;
  sequence: number;
  seenAt: number;
  expiresAt: number;
  fingerprint: string;
};
export interface PresenceStore {
  register(relayId: string, report: Report): Promise<Presence> | Presence;
  report(
    relayId: string,
    report: Report,
    leaseId: string,
    sequence: number,
  ): Promise<Presence> | Presence;
  get(
    relayIds: string[],
  ): Promise<Map<string, Presence>> | Map<string, Presence>;
  close(): Promise<void> | void;
}
const capacities = [
  'activeConnections',
  'activeTunnels',
  'retainedTunnels',
  'activeStreams',
  'maxConnections',
  'maxTunnels',
  'maxStreams',
] as const;
export function incarnation(value: unknown): string {
  if (typeof value !== 'string' || !/^[a-f0-9]{32}$/.test(value)) invalid();
  return value;
}
export function sequence(value: unknown): number {
  if (
    typeof value !== 'number' ||
    !Number.isInteger(value) ||
    value < 1 ||
    value > 2147483647
  )
    invalid();
  return value;
}
export function parseReport(
  body: Record<string, unknown>,
  update = false,
): Report {
  fields(body, [
    ...capacities,
    'instanceId',
    'status',
    'observations',
    ...(update ? ['leaseId', 'sequence'] : []),
  ]);
  const instanceId = incarnation(body.instanceId);
  if (
    typeof body.status !== 'string' ||
    !['HEALTHY', 'DEGRADED', 'DRAINING'].includes(body.status)
  )
    invalid();
  const bounds = {
    maxConnections: 10000,
    maxTunnels: 100000,
    maxStreams: 1024,
    activeConnections: 10000,
    activeTunnels: 10000,
    retainedTunnels: 100000,
    activeStreams: 10240000,
  };
  const values = {} as Capacity;
  for (const name of capacities) {
    const v = body[name];
    if (
      typeof v !== 'number' ||
      !Number.isInteger(v) ||
      v < (name.startsWith('max') ? 1 : 0) ||
      v > bounds[name]
    )
      invalid();
    values[name] = v;
  }
  if (
    values.activeConnections > values.maxConnections ||
    values.activeTunnels > values.activeConnections ||
    values.retainedTunnels < values.activeTunnels ||
    values.retainedTunnels > values.maxTunnels ||
    values.activeStreams > values.activeTunnels * values.maxStreams
  )
    invalid();
  return {
    ...values,
    instanceId,
    status: body.status as Report['status'],
    ...(body.observations === undefined
      ? {}
      : { observations: parseObservations(body.observations) }),
  };
}
function fingerprint(report: Report) {
  return createHash('sha256').update(JSON.stringify(report)).digest('hex');
}
function next(
  relayId: string,
  report: Report,
  leaseId: string,
  sequence: number,
  now: number,
): Presence {
  return {
    ...report,
    relayId,
    leaseId,
    sequence,
    seenAt: now,
    expiresAt: now + PRESENCE_TTL,
    fingerprint: fingerprint(report),
  };
}
export function stale(): never {
  throw new ApiFailure(409, 'PRESENCE_STALE', 'Relay report superseded');
}
export function expired(): never {
  throw new ApiFailure(409, 'PRESENCE_EXPIRED', 'Relay presence expired');
}
function unavailable() {
  return new ApiFailure(
    503,
    'PRESENCE_UNAVAILABLE',
    'Relay presence unavailable',
  );
}
export class MemoryPresence implements PresenceStore {
  private readonly entries = new Map<string, Presence>();
  readonly now: () => number;
  constructor(now: () => number = Date.now) {
    this.now = now;
  }
  private sweep() {
    for (const [id, p] of this.entries)
      if (p.expiresAt <= this.now()) this.entries.delete(id);
  }
  register(relayId: string, report: Report) {
    this.sweep();
    const prior = this.entries.get(relayId);
    if (prior?.instanceId === report.instanceId) return structuredClone(prior);
    if (prior && prior.status !== 'DRAINING') stale();
    if (!prior && this.entries.size >= 4096) throw unavailable();
    const value = next(
      relayId,
      report,
      randomBytes(16).toString('hex'),
      0,
      this.now(),
    );
    this.entries.set(relayId, value);
    return structuredClone(value);
  }
  report(relayId: string, report: Report, leaseId: string, seq: number) {
    this.sweep();
    const prior = this.entries.get(relayId);
    if (!prior) expired();
    if (prior.leaseId !== leaseId || prior.instanceId !== report.instanceId)
      stale();
    if (seq <= prior.sequence) {
      if (seq === prior.sequence && prior.fingerprint === fingerprint(report))
        return structuredClone(prior);
      stale();
    }
    const value = next(relayId, report, leaseId, seq, this.now());
    this.entries.set(relayId, value);
    return structuredClone(value);
  }
  get(ids: string[]) {
    this.sweep();
    return new Map(
      ids.flatMap((id) => {
        const p = this.entries.get(id);
        return p ? [[id, structuredClone(p)] as const] : [];
      }),
    );
  }
  close() {
    this.entries.clear();
  }
}
const updateScript = `
local previous=redis.call('GET',KEYS[1])
local value=cjson.decode(ARGV[2])
if previous then
 local old=cjson.decode(previous)
 if ARGV[1]=='register' then
  if old.instanceId==value.instanceId then return previous end
  if old.status~='DRAINING' then return '!stale' end
 else
  if old.leaseId~=value.leaseId or old.instanceId~=value.instanceId then return '!stale' end
  if value.sequence<=old.sequence then
   if value.sequence==old.sequence and old.fingerprint==value.fingerprint then return previous end
   return '!stale'
  end
 end
elseif ARGV[1]~='register' then return '!expired' end
local clock=redis.call('TIME')
value.seenAt=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000)
value.expiresAt=value.seenAt+tonumber(ARGV[3])
-- Preserve validated JSON arrays and decimal strings. Lua cjson re-encoding
-- turns empty log/observation arrays into objects. Only the root clock fields
-- are added here; the caller omits them from the canonical object.
local encoded=string.sub(ARGV[2],1,-2)..',"seenAt":'..string.format('%.0f',value.seenAt)..',"expiresAt":'..string.format('%.0f',value.expiresAt)..'}'
redis.call('SET',KEYS[1],encoded,'PX',ARGV[3])
return encoded`;
export class RedisPresence implements PresenceStore {
  private readonly client: ReturnType<typeof createClient>;
  private connecting: Promise<unknown> | undefined;
  private active = 0;
  constructor(url: string) {
    let parsed: URL;
    try {
      parsed = new URL(url);
      if (!['redis:', 'rediss:'].includes(parsed.protocol) || !parsed.hostname)
        throw new Error();
    } catch {
      throw new Error('Invalid presence configuration');
    }
    this.client = createClient({
      url: parsed.toString(),
      socket: { connectTimeout: 1000, reconnectStrategy: false },
      disableOfflineQueue: true,
      commandsQueueMaxLength: 128,
    });
    this.client.on('error', () => {});
  }
  private async work<T>(operation: () => Promise<T>): Promise<T> {
    if (this.active >= 128) throw unavailable();
    this.active++;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const deadline = new Promise<never>((_, reject) => {
      timer = setTimeout(() => {
        if (this.client.isOpen) this.client.destroy();
        reject(unavailable());
      }, 1000);
      timer.unref();
    });
    try {
      return await Promise.race([
        deadline,
        (async () => {
          if (!this.client.isReady) {
            this.connecting ??= this.client.connect().finally(() => {
              this.connecting = undefined;
            });
            await this.connecting;
          }
          return await operation();
        })(),
      ]);
    } catch (error) {
      if (error instanceof ApiFailure) throw error;
      throw unavailable();
    } finally {
      clearTimeout(timer);
      this.active--;
    }
  }
  private key(id: string) {
    return 'portway:relay:' + id + ':presence';
  }
  private decode(raw: string): Presence {
    try {
      if (Buffer.byteLength(raw) > 67_584) throw unavailable();
      const v = JSON.parse(raw) as Presence;
      fields(v as unknown as Record<string, unknown>, [
        ...capacities,
        'instanceId',
        'status',
        'relayId',
        'leaseId',
        'sequence',
        'seenAt',
        'expiresAt',
        'fingerprint',
        'observations',
      ]);
      id(v.relayId);
      hash(v.fingerprint);
      parseReport(
        Object.fromEntries(
          [
            ...capacities,
            'instanceId',
            'status',
            ...(v.observations === undefined ? [] : ['observations']),
          ].map((k) => [k, v[k as keyof Presence]]),
        ),
      );
      incarnation(v.leaseId);
      if (
        !Number.isInteger(v.sequence) ||
        v.sequence < 0 ||
        v.sequence > 2147483647 ||
        !Number.isSafeInteger(v.expiresAt) ||
        !Number.isSafeInteger(v.seenAt) ||
        v.expiresAt - v.seenAt !== PRESENCE_TTL
      )
        throw unavailable();
      return v;
    } catch {
      throw unavailable();
    }
  }
  private update(
    action: 'register' | 'report',
    relayId: string,
    report: Report,
    leaseId: string,
    seq: number,
  ) {
    return this.work(async () => {
      const value = next(relayId, report, leaseId, seq, Date.now());
      const raw = await this.client
        .withCommandOptions({ timeout: 1000 })
        .eval(updateScript, {
          keys: [this.key(relayId)],
          arguments: [
            action,
            JSON.stringify({
              ...value,
              seenAt: undefined,
              expiresAt: undefined,
            }),
            String(PRESENCE_TTL),
          ],
        });
      if (raw === '!stale') stale();
      if (raw === '!expired') expired();
      if (typeof raw !== 'string') throw unavailable();
      return this.decode(raw);
    });
  }
  register(relayId: string, report: Report) {
    return this.update(
      'register',
      relayId,
      report,
      randomBytes(16).toString('hex'),
      0,
    );
  }
  report(relayId: string, report: Report, leaseId: string, seq: number) {
    return this.update('report', relayId, report, leaseId, seq);
  }
  get(ids: string[]) {
    if (ids.length > 4096) throw unavailable();
    if (!ids.length) return Promise.resolve(new Map<string, Presence>());
    return this.work(async () => {
      const raw = await this.client
        .withCommandOptions({ timeout: 1000 })
        .mGet(ids.map((id) => this.key(id)));
      const result = new Map<string, Presence>();
      raw.forEach((v, i) => {
        if (v !== null) {
          const p = this.decode(v);
          if (p.relayId !== ids[i]) throw unavailable();
          if (p.expiresAt > Date.now()) result.set(p.relayId, p);
        }
      });
      return result;
    });
  }
  async close() {
    if (this.client.isOpen) this.client.destroy();
    await this.connecting?.catch(() => {});
  }
}
export function effective(relay: Relay, p: Presence | undefined): Relay {
  const status =
    relay.status !== 'HEALTHY' ? relay.status : (p?.status ?? 'OFFLINE');
  const capacity = p
    ? (Object.fromEntries(capacities.map((k) => [k, p[k]])) as Capacity)
    : null;
  return {
    ...relay,
    status,
    lastSeenAt: p ? new Date(p.seenAt).toISOString() : null,
    capacity,
  };
}
export function choose(
  relays: Relay[],
  presence: Map<string, Presence>,
  reservations: Map<string, number>,
  current: string | null,
  avoid: string | undefined,
): Relay | undefined {
  const eligible = relays
    .map((r) => effective(r, presence.get(r.id)))
    .filter((r) => {
      const c = r.capacity;
      return (
        r.status === 'HEALTHY' &&
        r.protocol === 'tls' &&
        c &&
        c.activeConnections < c.maxConnections &&
        c.activeStreams < c.maxConnections * c.maxStreams &&
        c.retainedTunnels < c.maxTunnels &&
        Math.max(c.activeTunnels, reservations.get(r.id) ?? 0) <
          Math.min(c.maxConnections, c.maxTunnels)
      );
    });
  const load = (r: Relay) => {
    const c = r.capacity!;
    return Math.max(
      c.activeConnections / c.maxConnections,
      Math.max(c.activeTunnels, reservations.get(r.id) ?? 0) /
        Math.min(c.maxConnections, c.maxTunnels),
    );
  };
  eligible.sort(
    (a, b) =>
      Number(a.id === avoid) - Number(b.id === avoid) ||
      Number(b.id === current) - Number(a.id === current) ||
      load(a) - load(b) ||
      a.id.localeCompare(b.id),
  );
  return eligible[0];
}
export function acknowledgement(p: Presence, drainRequested: boolean) {
  return {
    relayId: p.relayId,
    instanceId: p.instanceId,
    leaseId: p.leaseId,
    sequence: p.sequence,
    expiresAt: new Date(p.expiresAt).toISOString(),
    drainRequested,
  };
}

export type ReportAcknowledgement = ReturnType<typeof acknowledgement>;
