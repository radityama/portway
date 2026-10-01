import {
  allowedRoute,
  callAPI,
  clearCookie,
  configuration,
  cookieToken,
  ControlError,
  failure,
  MAX_BODY,
  originCheck,
  privateHeaders,
  readJSON,
} from '../../../../lib/transport';
import { revalidatePath } from 'next/cache';
export const runtime = 'nodejs';
export const dynamic = 'force-dynamic';
async function handle(
  request: Request,
  context: { params: Promise<{ path: string[] }> },
) {
  let config;
  try {
    config = configuration(process.env);
    const path = '/' + (await context.params).path.join('/'),
      query = new URL(request.url).searchParams;
    if (!allowedRoute(path, request.method, query))
      throw new ControlError(404, 'NOT_FOUND');
    if (request.method !== 'GET') originCheck(request, config);
    const token = cookieToken(request, config);
    if (!token) throw new ControlError(401, 'AUTH_INVALID');
    const body =
      request.method === 'GET' ? undefined : await readJSON(request, MAX_BODY);
    const key = request.headers.get('idempotency-key') ?? undefined;
    if (key && !/^[A-Za-z0-9_-]{8,128}$/.test(key))
      throw new ControlError(400, 'VALIDATION_ERROR');
    const result = await callAPI(
      config,
      path + (query.size ? '?' + query.toString() : ''),
      token,
      { method: request.method, body, key, signal: request.signal },
    );
    if (request.method !== 'GET') revalidatePath('/dashboard', 'layout');
    return result.status === 204
      ? new Response(null, { status: 204, headers: privateHeaders })
      : Response.json(result.envelope, {
          status: result.status,
          headers: privateHeaders,
        });
  } catch (error) {
    const response = failure(error);
    if (config && error instanceof ControlError && error.status === 401)
      response.headers.set('Set-Cookie', clearCookie(config));
    return response;
  }
}
export { handle as GET, handle as POST, handle as DELETE };
