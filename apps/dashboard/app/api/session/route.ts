import {
  callAPI,
  clearCookie,
  configuration,
  cookieToken,
  ControlError,
  failure,
  MAX_BODY,
  originCheck,
  prepareSession,
  privateHeaders,
  readJSON,
  sessionCookie,
  validToken,
} from '../../../lib/transport';

export const runtime = 'nodejs';
export const dynamic = 'force-dynamic';
export async function POST(request: Request) {
  try {
    const config = configuration(process.env);
    originCheck(request, config);
    const body = await readJSON(request, MAX_BODY);
    if (Object.keys(body).length !== 1 || !validToken(body.token))
      throw new ControlError(400, 'VALIDATION_ERROR');
    prepareSession(config);
    const result = await callAPI<{
      session: { accessToken: string; expiresAt: string };
    }>(config, '/auth/login', null, {
      method: 'POST',
      body,
      signal: request.signal,
    });
    const session = result.envelope?.data.session;
    if (!session) throw new ControlError(502, 'INVALID_API_RESPONSE');
    const cookie = sessionCookie(
      config,
      session.accessToken,
      session.expiresAt,
    );
    const old = cookieToken(request, config);
    if (old)
      await callAPI(config, '/auth/logout', old, {
        method: 'POST',
        body: {},
        signal: request.signal,
      }).catch(() => {});
    return Response.json(
      { data: { expiresAt: session.expiresAt }, error: null, meta: {} },
      { headers: { ...privateHeaders, 'Set-Cookie': cookie } },
    );
  } catch (error) {
    return failure(error);
  }
}
export async function DELETE(request: Request) {
  try {
    const config = configuration(process.env);
    originCheck(request, config);
    const body = await readJSON(request, MAX_BODY);
    if (Object.keys(body).length)
      throw new ControlError(400, 'VALIDATION_ERROR');
    const token = cookieToken(request, config);
    let revoked = !token;
    if (token) {
      try {
        await callAPI(config, '/auth/logout', token, {
          method: 'POST',
          body: {},
          signal: request.signal,
        });
        revoked = true;
      } catch (error) {
        revoked = error instanceof ControlError && error.status === 401;
      }
    }
    return Response.json(
      { data: { revoked }, error: null, meta: {} },
      { headers: { ...privateHeaders, 'Set-Cookie': clearCookie(config) } },
    );
  } catch (error) {
    return failure(error);
  }
}
