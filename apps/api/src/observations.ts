import type { Tunnel } from './models.ts';
import type { Presence } from './presence.ts';
import { fields, generation, id, invalid, object } from './validation.ts';

export const LATENCY_BOUNDS = [
  0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
] as const;
export const METHODS = [
  'GET',
  'POST',
  'PUT',
  'PATCH',
  'DELETE',
  'HEAD',
  'OPTIONS',
  'CONNECT',
  'TRACE',
  'OTHER',
] as const;
export type RequestLog = {
  id: string;
  timestamp: string;
  method: string;
  status: number;
  durationMs: number;
  bytesIn: string;
  bytesOut: string;
  outcome: 'complete' | 'error' | 'canceled';
};
export type TunnelMetrics = {
  tunnelId: string;
  generation: string;
  requests: string;
  errors: string;
  bytesIn: string;
  bytesOut: string;
  activeRequests: number;
  latencyBuckets: string[];
  latencySumSeconds: number;
};
export type Observation = TunnelMetrics & { logs: RequestLog[] };
export type ObservationView = {
  available: boolean;
  observedAt: string | null;
  metrics: TunnelMetrics | null;
  logs: RequestLog[];
};
function number(value: unknown, max: number, integer = false): number {
  if (
    typeof value !== 'number' ||
    !Number.isFinite(value) ||
    value < 0 ||
    value > max ||
    (integer && !Number.isInteger(value))
  )
    invalid();
  return value;
}
function log(value: unknown): RequestLog {
  const v = object(value);
  fields(v, [
    'id',
    'timestamp',
    'method',
    'status',
    'durationMs',
    'bytesIn',
    'bytesOut',
    'outcome',
  ]);
  if (
    typeof v.id !== 'string' ||
    !/^[a-f0-9]{32}$/.test(v.id) ||
    typeof v.timestamp !== 'string' ||
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(v.timestamp) ||
    !Number.isFinite(Date.parse(v.timestamp)) ||
    Date.parse(v.timestamp) > Date.now() + 30_000 ||
    typeof v.method !== 'string' ||
    !METHODS.some((m) => m === v.method) ||
    !['complete', 'error', 'canceled'].includes(String(v.outcome))
  )
    invalid();
  const status = number(v.status, 599, true);
  if (status > 0 && status < 100) invalid();
  return {
    id: v.id,
    timestamp: v.timestamp,
    method: v.method,
    status,
    durationMs: number(v.durationMs, 9223372036855),
    bytesIn: generation(v.bytesIn, true),
    bytesOut: generation(v.bytesOut, true),
    outcome: v.outcome as RequestLog['outcome'],
  };
}
export function parseObservations(value: unknown): Observation[] {
  if (!Array.isArray(value) || value.length > 32) invalid();
  const ids = new Set<string>();
  return value.map((input) => {
    const v = object(input);
    fields(v, [
      'tunnelId',
      'generation',
      'requests',
      'errors',
      'bytesIn',
      'bytesOut',
      'activeRequests',
      'latencyBuckets',
      'latencySumSeconds',
      'logs',
    ]);
    const tunnelId = id(v.tunnelId);
    if (ids.has(tunnelId)) invalid();
    ids.add(tunnelId);
    const requests = generation(v.requests, true),
      errors = generation(v.errors, true);
    if (
      BigInt(errors) > BigInt(requests) ||
      !Array.isArray(v.latencyBuckets) ||
      v.latencyBuckets.length !== 12 ||
      !Array.isArray(v.logs) ||
      v.logs.length > 4
    )
      invalid();
    const latencyBuckets = v.latencyBuckets.map((b) => generation(b, true));
    if (
      latencyBuckets[11] !== requests ||
      latencyBuckets.some(
        (b, i) => i > 0 && BigInt(b) < BigInt(latencyBuckets[i - 1]!),
      )
    )
      invalid();
    const logs = v.logs.map(log);
    if (
      new Set(logs.map((l) => l.id)).size !== logs.length ||
      BigInt(logs.length) > BigInt(requests)
    )
      invalid();
    return {
      tunnelId,
      generation: generation(v.generation),
      requests,
      errors,
      bytesIn: generation(v.bytesIn, true),
      bytesOut: generation(v.bytesOut, true),
      activeRequests: number(v.activeRequests, 10000, true),
      latencyBuckets,
      latencySumSeconds: number(v.latencySumSeconds, 1e30),
      logs,
    };
  });
}
export function observationView(
  tunnel: Tunnel,
  presence: Presence | undefined,
  now: number,
): ObservationView {
  const o = presence?.observations?.find(
    (v) => v.tunnelId === tunnel.id && v.generation === tunnel.generation,
  );
  if (
    tunnel.status === 'REVOKED' ||
    !presence ||
    presence.relayId !== tunnel.relayId ||
    presence.expiresAt <= now ||
    !o
  )
    return { available: false, observedAt: null, metrics: null, logs: [] };
  const { logs, ...metrics } = o;
  return {
    available: true,
    observedAt: new Date(presence.seenAt).toISOString(),
    metrics,
    logs: logs
      .filter(
        (l) =>
          Date.parse(l.timestamp) > now - 3_600_000 &&
          Date.parse(l.timestamp) <= now + 30_000,
      )
      .sort((a, b) => Date.parse(b.timestamp) - Date.parse(a.timestamp)),
  };
}
