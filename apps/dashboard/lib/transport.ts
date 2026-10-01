import { isIP } from 'node:net';
import { createCipheriv, createDecipheriv, randomBytes } from 'node:crypto';
import { closeSync, fstatSync, lstatSync, openSync, readSync } from 'node:fs';

export const MAX_BODY = 64 * 1024;
export const MAX_REPLY = 256 * 1024;
export const validToken = (value: unknown): value is string =>
  typeof value === 'string' && /^[A-Za-z0-9_-]{32,512}$/.test(value);
export type Envelope<T> = {
  data: T;
  error: null;
  meta: { nextCursor?: string | null };
};
export class ControlError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string) {
    super('Control request failed');
    this.status = status;
    this.code = code;
  }
}
export type DashboardConfig = {
  api: string;
  origin: string;
  timeout: number;
  secure: boolean;
  cookie: string;
  keyFile: string;
};
function loopback(host: string) {
  return (
    host === 'localhost' ||
    host === '[::1]' ||
    (isIP(host) === 4 && host.startsWith('127.'))
  );
}
export function configuration(
  env: Record<string, string | undefined>,
): DashboardConfig {
  try {
    const api = new URL(
      env.DASHBOARD_API_URL ??
        `http://127.0.0.1:${env.API_PORT ?? '8080'}/api/v1`,
    );
    const origin = new URL(
      env.DASHBOARD_ORIGIN ??
        `http://127.0.0.1:${env.PORT ?? env.DASHBOARD_PORT ?? '3000'}`,
    );
    for (const url of [api, origin]) {
      if (
        url.username ||
        url.password ||
        url.search ||
        url.hash ||
        (url.protocol !== 'https:' &&
          !(url.protocol === 'http:' && loopback(url.hostname)))
      )
        throw new Error();
    }
    if (api.pathname !== '/api/v1' || origin.pathname !== '/')
      throw new Error();
    const raw = env.DASHBOARD_API_TIMEOUT_MS ?? '5000';
    if (!/^[0-9]+$/.test(raw) || Number(raw) < 100 || Number(raw) > 10000)
      throw new Error();
    const secure = origin.protocol === 'https:';
    if (
      !env.DASHBOARD_SESSION_KEY_FILE ||
      env.DASHBOARD_SESSION_KEY_FILE.length > 4096
    )
      throw new Error();
    return {
      api: api.toString().replace(/\/$/, ''),
      origin: origin.origin,
      timeout: Number(raw),
      secure,
      cookie: secure ? '__Host-portway_session' : 'portway_session',
      keyFile: env.DASHBOARD_SESSION_KEY_FILE,
    };
  } catch {
    throw new ControlError(503, 'DASHBOARD_CONFIGURATION');
  }
}
export const privateHeaders = {
  'Cache-Control': 'private, no-store',
  'Content-Type': 'application/json',
  'X-Content-Type-Options': 'nosniff',
};
export function failure(error: unknown): Response {
  const e =
    error instanceof ControlError
      ? error
      : new ControlError(503, 'CONTROL_UNAVAILABLE');
  return Response.json(
    {
      data: null,
      error: { code: e.code, message: 'Request could not be completed' },
      meta: {},
    },
    { status: e.status, headers: privateHeaders },
  );
}
export function originCheck(request: Request, config: DashboardConfig) {
  if (
    request.headers.get('origin') !== config.origin ||
    request.headers.get('sec-fetch-site') === 'cross-site'
  )
    throw new ControlError(403, 'ORIGIN_REJECTED');
  if (
    request.headers.get('content-type')?.split(';')[0].trim().toLowerCase() !==
    'application/json'
  )
    throw new ControlError(415, 'JSON_REQUIRED');
}
export function cookieToken(
  request: Request,
  config: DashboardConfig,
): string | null {
  const values = (request.headers.get('cookie') ?? '')
    .split(';')
    .map((v) => v.trim())
    .filter((v) => v.startsWith(config.cookie + '='))
    .map((v) => v.slice(config.cookie.length + 1));
  if (values.length !== 1 || !/^[A-Za-z0-9_-]{40,1024}$/.test(values[0]))
    return null;
  try {
    const raw = Buffer.from(values[0], 'base64url');
    const cipher = createDecipheriv(
      'aes-256-gcm',
      sessionKey(config),
      raw.subarray(0, 12),
    );
    cipher.setAAD(Buffer.from(config.origin + config.cookie));
    cipher.setAuthTag(raw.subarray(12, 28));
    const value = JSON.parse(
      Buffer.concat([
        cipher.update(raw.subarray(28)),
        cipher.final(),
      ]).toString(),
    );
    return validToken(value.token) &&
      typeof value.expiresAt === 'string' &&
      Date.parse(value.expiresAt) > Date.now()
      ? value.token
      : null;
  } catch (error) {
    if (error instanceof ControlError) throw error;
    return null;
  }
}
let loadedKey: { path: string; key: Buffer } | undefined;
export function prepareSession(config: DashboardConfig) {
  sessionKey(config);
}
function sessionKey(config: DashboardConfig): Buffer {
  if (loadedKey?.path === config.keyFile) return loadedKey.key;
  let fd: number | undefined;
  try {
    const info = lstatSync(config.keyFile);
    if (
      !info.isFile() ||
      info.size > 64 ||
      (process.platform !== 'win32' && (info.mode & 0o077) !== 0)
    )
      throw new Error();
    fd = openSync(config.keyFile, 'r');
    const opened = fstatSync(fd);
    if (info.dev !== opened.dev || info.ino !== opened.ino) throw new Error();
    const bytes = Buffer.alloc(65);
    let size = 0;
    while (size < bytes.length) {
      const n = readSync(fd, bytes, size, bytes.length - size, null);
      if (!n) break;
      size += n;
    }
    if (size > 64) throw new Error();
    const raw = bytes.subarray(0, size).toString('utf8').trim();
    if (!/^[A-Za-z0-9_-]{43}$/.test(raw)) throw new Error();
    const key = Buffer.from(raw, 'base64url');
    if (key.length !== 32 || key.toString('base64url') !== raw)
      throw new Error();
    loadedKey = { path: config.keyFile, key };
    return key;
  } catch {
    throw new ControlError(503, 'DASHBOARD_CONFIGURATION');
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
}
export function sessionCookie(
  config: DashboardConfig,
  token: string,
  expiresAt: string,
) {
  const expiry = Date.parse(expiresAt),
    remaining = Math.floor((expiry - Date.now()) / 1000);
  if (
    !validToken(token) ||
    !Number.isFinite(expiry) ||
    remaining < 1 ||
    remaining > 3610
  )
    throw new ControlError(502, 'INVALID_API_RESPONSE');
  const nonce = randomBytes(12),
    cipher = createCipheriv('aes-256-gcm', sessionKey(config), nonce);
  cipher.setAAD(Buffer.from(config.origin + config.cookie));
  const encrypted = Buffer.concat([
    cipher.update(JSON.stringify({ token, expiresAt })),
    cipher.final(),
  ]);
  const value = Buffer.concat([nonce, cipher.getAuthTag(), encrypted]).toString(
    'base64url',
  );
  return `${config.cookie}=${value}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${Math.min(remaining, 3600)}; Expires=${new Date(expiry).toUTCString()}${config.secure ? '; Secure' : ''}`;
}
export const clearCookie = (config: DashboardConfig) =>
  `${config.cookie}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0; Expires=Thu, 01 Jan 1970 00:00:00 GMT${config.secure ? '; Secure' : ''}`;

export async function readJSON(source: Request | Response, max: number) {
  const declared = source.headers.get('content-length');
  if (declared !== null && (!/^\d+$/.test(declared) || Number(declared) > max))
    throw new ControlError(413, 'PAYLOAD_TOO_LARGE');
  if (!source.body) throw new ControlError(400, 'VALIDATION_ERROR');
  const reader = source.body.getReader(),
    parts: Uint8Array[] = [];
  let total = 0,
    timer: ReturnType<typeof setTimeout> | undefined;
  const deadline = new Promise<never>((_, reject) => {
    timer = setTimeout(() => {
      reject(new ControlError(408, 'REQUEST_TIMEOUT'));
      void reader.cancel().catch(() => {});
    }, 5000);
    timer.unref?.();
  });
  try {
    while (true) {
      const part = await Promise.race([reader.read(), deadline]);
      if (part.done) break;
      total += part.value.byteLength;
      if (total > max) {
        void reader.cancel().catch(() => {});
        throw new ControlError(413, 'PAYLOAD_TOO_LARGE');
      }
      parts.push(part.value);
    }
    const value: unknown = JSON.parse(
      new TextDecoder('utf-8', { fatal: true }).decode(
        Buffer.concat(parts, total),
      ),
    );
    if (!value || typeof value !== 'object' || Array.isArray(value))
      throw new Error();
    return value as Record<string, unknown>;
  } catch (e) {
    if (e instanceof ControlError) throw e;
    throw new ControlError(400, 'VALIDATION_ERROR');
  } finally {
    clearTimeout(timer);
    reader.releaseLock();
  }
}

let active = 0;
export async function callAPI<T>(
  config: DashboardConfig,
  path: string,
  token: string | null,
  options: {
    method?: string;
    body?: unknown;
    key?: string;
    signal?: AbortSignal;
  } = {},
): Promise<{ status: number; envelope: Envelope<T> | null }> {
  if (!path.startsWith('/') || path.startsWith('//') || /[\\#\r\n]/.test(path))
    throw new ControlError(400, 'VALIDATION_ERROR');
  if (token !== null && !validToken(token))
    throw new ControlError(401, 'AUTH_INVALID');
  if (active >= 32) throw new ControlError(503, 'CAPACITY_REACHED');
  active++;
  let response: Response | undefined;
  try {
    const signal = AbortSignal.any([
      AbortSignal.timeout(config.timeout),
      ...(options.signal ? [options.signal] : []),
    ]);
    response = await fetch(config.api + path, {
      method: options.method ?? 'GET',
      headers: {
        Accept: 'application/json',
        ...(token ? { Authorization: 'Bearer ' + token } : {}),
        ...(options.body === undefined
          ? {}
          : { 'Content-Type': 'application/json' }),
        ...(options.key ? { 'Idempotency-Key': options.key } : {}),
      },
      ...(options.body === undefined
        ? {}
        : { body: JSON.stringify(options.body) }),
      signal,
      cache: 'no-store',
      redirect: 'manual',
    });
    if (response.status >= 300 && response.status < 400)
      throw new ControlError(502, 'INVALID_API_RESPONSE');
    if (response.status === 204) return { status: 204, envelope: null };
    if (
      response.headers.get('content-type')?.split(';')[0].trim() !==
      'application/json'
    )
      throw new ControlError(502, 'INVALID_API_RESPONSE');
    let value: Record<string, unknown>;
    try {
      value = await readJSON(response, MAX_REPLY);
    } catch {
      throw new ControlError(
        signal.aborted ? 503 : 502,
        signal.aborted ? 'CONTROL_UNAVAILABLE' : 'INVALID_API_RESPONSE',
      );
    }
    if (
      !value.meta ||
      typeof value.meta !== 'object' ||
      Array.isArray(value.meta)
    )
      throw new ControlError(502, 'INVALID_API_RESPONSE');
    if (!response.ok) {
      const error = value.error as { code?: unknown } | null;
      throw new ControlError(
        response.status,
        typeof error?.code === 'string' && /^[A-Z_]{1,64}$/.test(error.code)
          ? error.code
          : 'CONTROL_UNAVAILABLE',
      );
    }
    if (value.error !== null || !value.data || typeof value.data !== 'object')
      throw new ControlError(502, 'INVALID_API_RESPONSE');
    return {
      status: response.status,
      envelope: value as unknown as Envelope<T>,
    };
  } catch (error) {
    if (error instanceof ControlError) throw error;
    throw new ControlError(503, 'CONTROL_UNAVAILABLE');
  } finally {
    active--;
    if (response?.body && !response.body.locked)
      await response.body.cancel().catch(() => {});
  }
}

export function allowedRoute(
  path: string,
  method: string,
  query: URLSearchParams,
): boolean {
  if (
    path.length > 400 ||
    !/^\/[a-z]+(?:\/[A-Za-z0-9_-]{1,128}){0,2}$/.test(path) ||
    query.toString().length > 8192
  )
    return false;
  const get =
    /^\/(me|projects|tunnels|domains|relays)$/.test(path) ||
    /^\/(projects|tunnels|domains|relays)\/[A-Za-z0-9_-]+$/.test(path) ||
    /^\/tunnels\/[A-Za-z0-9_-]+\/(logs|metrics)$/.test(path);
  const post =
    /^\/(projects|tunnels|domains)$/.test(path) ||
    /^\/tunnels\/[A-Za-z0-9_-]+\/revoke$/.test(path) ||
    /^\/domains\/[A-Za-z0-9_-]+\/(challenge|verify|activate)$/.test(path);
  const remove = /^\/(tunnels|domains)\/[A-Za-z0-9_-]+$/.test(path);
  if (
    !(method === 'GET'
      ? get
      : method === 'POST'
        ? post
        : method === 'DELETE' && remove)
  )
    return false;
  const names = [
    'cursor',
    'limit',
    ...(path === '/tunnels'
      ? ['projectId', 'status', 'relayId']
      : path === '/domains'
        ? ['tunnelId']
        : []),
  ];
  const collection =
    /^\/(projects|tunnels|domains|relays)$/.test(path) && method === 'GET';
  const recentLogs =
    /^\/tunnels\/[A-Za-z0-9_-]+\/logs$/.test(path) && method === 'GET';
  for (const key of query.keys()) {
    if (query.getAll(key).length !== 1) return false;
    if (recentLogs) {
      if (key !== 'limit' || !/^[1-4]$/.test(query.get(key) ?? ''))
        return false;
    } else if (!collection || !names.includes(key)) return false;
  }
  return true;
}
