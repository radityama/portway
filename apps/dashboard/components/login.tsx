'use client';
import { useState } from 'react';
import { request, DashboardError } from '../lib/browser';
import { Failure } from './ui';
export function LoginForm({ reason }: { reason?: string }) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState('');
  return (
    <>
      {reason === 'local-logout' && (
        <div className="notice" role="status">
          Signed out locally. The control plane could not confirm remote
          revocation; the session will expire normally.
        </div>
      )}
      {reason === 'expired' && (
        <div className="notice" role="status">
          Your session has ended. Sign in again to continue.
        </div>
      )}
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          const form = e.currentTarget,
            token = new FormData(form).get('token');
          setBusy(true);
          setError('');
          try {
            await request('/api/session', 'POST', { token });
            form.reset();
            window.location.assign('/dashboard');
          } catch (error) {
            form.reset();
            setError(
              error instanceof DashboardError
                ? error.code
                : 'CONTROL_UNAVAILABLE',
            );
            setBusy(false);
          }
        }}
      >
        <label>
          API key
          <input
            name="token"
            type="password"
            required
            minLength={32}
            maxLength={512}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            placeholder="Your provisioned API key"
          />
        </label>
        <p className="muted small">
          Use a user API key provided by your workspace administrator.
        </p>
        {error && <Failure code={error} />}
        <button className="button primary full-width" disabled={busy}>
          {busy ? 'Signing in…' : 'Sign in to workspace →'}
        </button>
      </form>
    </>
  );
}
