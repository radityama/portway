import 'server-only';
import { cookies } from 'next/headers';
import { redirect } from 'next/navigation';
import { cache } from 'react';
import { callAPI, configuration, cookieToken, ControlError } from './transport';
import type { Profile } from './contracts';

export async function load<T>(path: string) {
  const config = configuration(process.env),
    jar = await cookies();
  const token = cookieToken(
    new Request(config.origin, {
      headers: {
        Cookie: `${config.cookie}=${jar.get(config.cookie)?.value ?? ''}`,
      },
    }),
    config,
  );
  if (!token) redirect('/login');
  try {
    const result = await callAPI<T>(config, path, token);
    return { value: result.envelope!, error: null };
  } catch (error) {
    if (error instanceof ControlError && error.status === 401)
      redirect('/login');
    return {
      value: null,
      error: error instanceof ControlError ? error.code : 'CONTROL_UNAVAILABLE',
    };
  }
}
export const profile = cache(() => load<Profile>('/me'));
export function queryString(
  params: Record<string, string | string[] | undefined>,
  names: string[],
) {
  const query = new URLSearchParams();
  for (const name of names) {
    const value = params[name];
    if (typeof value === 'string' && value.length <= 4096 && value !== '')
      query.set(name, value);
    else if (Array.isArray(value)) query.set(name, 'invalid');
  }
  query.set('limit', '50');
  return query;
}
