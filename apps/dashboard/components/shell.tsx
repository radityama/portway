'use client';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { useState } from 'react';
import type { Profile } from '../lib/contracts';
import { Icon, Mark, errorMessage } from './ui';
import { request } from '../lib/browser';

const navigation = [
  [
    'Overview',
    '/dashboard',
    'M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z',
  ],
  [
    'Tunnels',
    '/dashboard/tunnels',
    'M4 6h16M4 18h16M7 3 4 6l3 3M17 15l3 3-3 3M8 12h8',
  ],
  [
    'Domains',
    '/dashboard/domains',
    'M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0ZM3 12h18M12 3c5 5 5 13 0 18-5-5-5-13 0-18Z',
  ],
  [
    'Relays',
    '/dashboard/relays',
    'M5 4h14v6H5zM5 14h14v6H5zM8 7h.01M8 17h.01M12 10v4',
  ],
  ['Logs', '/dashboard/logs', 'M5 3h14v18H5zM8 8h8M8 12h8M8 16h5'],
  [
    'Settings',
    '/dashboard/settings',
    'M12 8a4 4 0 1 1 0 8 4 4 0 0 1 0-8ZM12 3v2M12 19v2M3 12h2M19 12h2M5.6 5.6 7 7M17 17l1.4 1.4M5.6 18.4 7 17M17 7l1.4-1.4',
  ],
];
export function SignOut() {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState('');
  return (
    <>
      <button
        className="button secondary"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          setError('');
          try {
            const result = await request<{ revoked: boolean }>(
              '/api/session',
              'DELETE',
              {},
            );
            window.location.assign(
              result?.revoked ? '/login' : '/login?reason=local-logout',
            );
          } catch {
            setError(errorMessage('CONTROL_UNAVAILABLE'));
            setBusy(false);
          }
        }}
      >
        {busy ? 'Signing out…' : 'Sign out'}
      </button>
      {error && (
        <p role="alert" className="small">
          {error}
        </p>
      )}
    </>
  );
}
export function Sidebar({ account }: { account: Profile | null }) {
  const path = usePathname();
  return (
    <aside className="sidebar">
      <Link prefetch={false} className="brand" href="/dashboard">
        <Mark />
        <span>
          Portway<span className="brand-caption">LOCAL → PUBLIC</span>
        </span>
      </Link>
      <p className="nav-label">WORKSPACE</p>
      <nav aria-label="Main navigation">
        {navigation.map(([name, href, icon]) => {
          const active =
            href === '/dashboard' ? path === href : path.startsWith(href);
          return (
            <Link
              prefetch={false}
              key={href}
              href={href}
              aria-current={active ? 'page' : undefined}
              className={`nav-link ${active ? 'selected' : ''}`}
            >
              <svg
                aria-hidden="true"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.6"
                strokeLinecap="round"
                strokeLinejoin="round"
              >
                <path d={icon} />
              </svg>
              {name}
              {active && (
                <span className="nav-arrow">
                  <Icon />
                </span>
              )}
            </Link>
          );
        })}
      </nav>
      <div className="sidebar-bottom">
        <div className="workspace-label">
          <span className="avatar">
            {(account?.organization.name ?? 'P').slice(0, 1).toUpperCase()}
          </span>
          <div>
            <strong>{account?.organization.name ?? 'Workspace'}</strong>
            <span className="small muted">
              {account?.role.toLowerCase() ?? 'Account'}
            </span>
          </div>
        </div>
        <p className="small muted">
          A path from your local port
          <br />
          to the web.
        </p>
      </div>
    </aside>
  );
}
