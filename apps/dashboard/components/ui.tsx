import Link from 'next/link';
import type { ReactNode } from 'react';

export function Icon({ name = 'arrow' }: { name?: string }) {
  const paths: Record<string, string> = {
    arrow: 'M6 18 18 6M6 6h12v12',
    local: 'M3 4h18v13H3zM8 21h8M12 17v4',
    project: 'M3 6h7l2 2h9v12H3zM3 6V4h7l2 2',
    domain:
      'M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0ZM3 12h18M12 3c5 5 5 13 0 18-5-5-5-13 0-18Z',
    relay: 'M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01M12 10v4',
  };
  return (
    <svg
      className="inline-icon"
      aria-hidden="true"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d={paths[name] ?? paths.arrow} />
    </svg>
  );
}

export function Mark() {
  return (
    <span className="brand-mark" aria-hidden="true">
      <svg viewBox="0 0 24 24" fill="none">
        <path
          d="M7 19V5h6a4 4 0 0 1 0 8H7m4 6 7-7m-5 0h5v5"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </span>
  );
}
export function Heading({
  eyebrow = 'WORKSPACE',
  title,
  description,
  action,
}: {
  eyebrow?: string;
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="page-heading">
      <div>
        <p className="eyebrow">{eyebrow}</p>
        <h1>{title}</h1>
        <p className="muted">{description}</p>
      </div>
      {action}
    </div>
  );
}
export function Badge({ status }: { status: string }) {
  const kind = ['HEALTHY', 'ACTIVE', 'VERIFIED', 'CONNECTED'].includes(status)
    ? 'good'
    : ['REVOKED', 'OFFLINE', 'DISABLED'].includes(status)
      ? 'quiet'
      : 'pending';
  return (
    <span className={`badge ${kind}`}>
      <span className="status-dot" />
      {status.toLowerCase().replaceAll('_', ' ')}
    </span>
  );
}
const messages: Record<string, string> = {
  CONTROL_UNAVAILABLE:
    'The control plane is unavailable. Try again when it is reachable.',
  STORAGE_UNAVAILABLE:
    'The control plane cannot reach its database. Please try again.',
  PRESENCE_UNAVAILABLE:
    'Relay health is temporarily unavailable. Please try again.',
  FORBIDDEN: 'Your role does not allow this action.',
  AUTH_INVALID: 'The API key is invalid. Check the key and try again.',
  AUTH_EXPIRED: 'This credential has expired. Sign in with a current API key.',
  AUTH_REVOKED: 'This credential was revoked. Sign in with a current API key.',
  DNS_UNAVAILABLE:
    'DNS verification is temporarily unavailable. Your domain has not been verified.',
  DOMAIN_VERIFICATION_REQUIRED:
    'Ownership is not verified. Publish the current TXT record, then verify again.',
  DOMAIN_ALREADY_ASSIGNED:
    'This hostname is already reserved. Choose another hostname or manage its existing association.',
  PROJECT_CONFLICT: 'That project slug is already in use.',
  TUNNEL_REVOKED:
    'This tunnel has been revoked. Create a new tunnel to continue.',
  CREDENTIAL_ALREADY_ISSUED:
    'The one-time value was already issued. Use a new domain challenge to recover a missing proof.',
  RATE_LIMITED: 'Too many requests. Wait a moment before trying again.',
  VALIDATION_ERROR: 'Check the fields and try again.',
  NOT_IMPLEMENTED: 'This data is not available yet.',
  ORIGIN_REJECTED:
    'This dashboard address does not match its configured origin.',
  DASHBOARD_CONFIGURATION:
    'The dashboard is not configured to reach the control plane.',
  INVALID_API_RESPONSE: 'The control plane returned an unexpected response.',
};
export const errorMessage = (code: string) =>
  messages[code] ?? 'The request could not be completed. Please try again.';
export function Failure({ code }: { code: string }) {
  return (
    <div className="notice error" role="alert">
      <strong>Unable to complete this request</strong>
      <p>{errorMessage(code)}</p>
      <span className="mono small">{code}</span>
    </div>
  );
}
export function Empty({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <div className="empty-state">
      <span className="empty-symbol" aria-hidden="true">
        <Icon />
      </span>
      <h3>{title}</h3>
      <p className="muted">{children}</p>
    </div>
  );
}
export function Pagination({
  path,
  query,
  cursor,
}: {
  path: string;
  query: URLSearchParams;
  cursor?: string | null;
}) {
  const first = new URLSearchParams(query),
    next = new URLSearchParams(query);
  first.delete('cursor');
  if (cursor) next.set('cursor', cursor);
  return (
    <div className="pagination">
      <span className="muted small">Up to 50 results per page</span>
      <div>
        {query.has('cursor') && (
          <Link
            prefetch={false}
            className="button secondary"
            href={`${path}?${first}`}
          >
            First page
          </Link>
        )}
        {cursor && (
          <Link
            prefetch={false}
            className="button secondary"
            href={`${path}?${next}`}
          >
            Next page →
          </Link>
        )}
      </div>
    </div>
  );
}
export function DateText({ value }: { value: string | null | undefined }) {
  return (
    <>
      {value
        ? new Date(value).toLocaleString('en-GB', {
            timeZone: 'UTC',
            dateStyle: 'medium',
            timeStyle: 'short',
          }) + ' UTC'
        : 'Not reported'}
    </>
  );
}
