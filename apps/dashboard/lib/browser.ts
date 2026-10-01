'use client';
export class DashboardError extends Error {
  readonly code: string;
  constructor(code: string) {
    super('Request failed');
    this.code = code;
  }
}
export async function request<T>(
  path: string,
  method = 'GET',
  body?: unknown,
  key?: string,
): Promise<T | null> {
  try {
    const response = await fetch(path, {
      method,
      credentials: 'same-origin',
      cache: 'no-store',
      signal: AbortSignal.timeout(10000),
      headers: {
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
        ...(key ? { 'Idempotency-Key': key } : {}),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    if (response.status === 204) return null;
    const result = await response.json();
    if (!response.ok) {
      if (response.status === 401)
        window.location.assign('/login?reason=expired');
      throw new DashboardError(result.error?.code ?? 'CONTROL_UNAVAILABLE');
    }
    return result.data as T;
  } catch (error) {
    if (error instanceof DashboardError) throw error;
    throw new DashboardError('CONTROL_UNAVAILABLE');
  }
}
