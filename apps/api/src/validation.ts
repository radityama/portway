import { isIP } from 'node:net';
import type { ContentfulStatusCode } from 'hono/utils/http-status';

export class ApiFailure extends Error {
  status: ContentfulStatusCode;
  code: string;
  constructor(status: ContentfulStatusCode, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}
export function invalid(): never {
  throw new ApiFailure(400, 'VALIDATION_ERROR', 'Invalid request');
}
export function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    return invalid();
  return value as Record<string, unknown>;
}
export function fields(value: Record<string, unknown>, names: string[]) {
  if (Object.keys(value).some((name) => !names.includes(name))) invalid();
}
export function text(value: unknown, max = 100): string {
  if (
    typeof value !== 'string' ||
    value.length < 1 ||
    value.length > max ||
    value !== value.trim() ||
    [...value].some((c) => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127)
  )
    return invalid();
  return value;
}
export function id(value: unknown): string {
  const result = text(value, 128);
  if (!/^[a-zA-Z0-9_-]+$/.test(result)) return invalid();
  return result;
}
export function slug(value: unknown): string {
  const result = text(value, 63);
  if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(result)) return invalid();
  return result;
}
export function generation(value: unknown, zero = false): string {
  if (
    typeof value !== 'string' ||
    !/^(0|[1-9][0-9]{0,19})$/.test(value) ||
    BigInt(value) > 18446744073709551615n ||
    (!zero && value === '0')
  )
    return invalid();
  return value;
}
export function token(value: unknown): string {
  if (typeof value !== 'string' || !/^[A-Za-z0-9_-]{32,512}$/.test(value))
    return invalid();
  return value;
}
export function hash(value: unknown): string {
  if (typeof value !== 'string' || !/^[a-f0-9]{64}$/.test(value))
    return invalid();
  return value;
}
export function port(value: unknown): number {
  if (
    !Number.isInteger(value) ||
    typeof value !== 'number' ||
    value < 1 ||
    value > 65535
  )
    return invalid();
  return value;
}
export function loopback(value: unknown): string {
  const host = text(value, 45);
  if (!((isIP(host) === 4 && host.startsWith('127.')) || host === '::1'))
    return invalid();
  return host;
}
export function hostname(value: unknown): string {
  const result = text(value, 253);
  if (isIP(result)) return result;
  if (
    !/^[a-z0-9]+(?:[a-z0-9.-]*[a-z0-9])?$/.test(result) ||
    result
      .split('.')
      .some(
        (part) =>
          part.length < 1 ||
          part.length > 63 ||
          part.startsWith('-') ||
          part.endsWith('-'),
      )
  )
    return invalid();
  return result;
}
export function timestamp(value: unknown): number {
  if (
    typeof value !== 'string' ||
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$/.test(value) ||
    !Number.isFinite(Date.parse(value))
  )
    return invalid();
  return Date.parse(value);
}

// JSON.parse establishes syntax; a bounded lexical scan rejects duplicate root
// properties, including escaped aliases. All request schemas are flat objects.
export function parseObject(raw: string): Record<string, unknown> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return invalid();
  }
  const result = object(parsed);
  const seen = new Set<string>();
  let depth = 0;
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i];
    if (c === '"') {
      const start = i++;
      for (; i < raw.length; i++) {
        if (raw[i] === '\\') i++;
        else if (raw[i] === '"') break;
      }
      let next = i + 1;
      while (/\s/.test(raw[next] ?? '') && next < raw.length) next++;
      if (depth === 1 && raw[next] === ':') {
        const key = JSON.parse(raw.slice(start, i + 1)) as string;
        if (seen.has(key)) return invalid();
        seen.add(key);
      }
    } else if (c === '{' || c === '[') depth++;
    else if (c === '}' || c === ']') depth--;
  }
  return result;
}
