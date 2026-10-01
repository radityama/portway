import type { ReactNode } from 'react';
import { profile } from '../../lib/server';
import { Sidebar, SignOut } from '../../components/shell';
import { Failure } from '../../components/ui';
import { MutationProvider } from '../../components/actions';
export const dynamic = 'force-dynamic';
export default async function DashboardLayout({
  children,
}: {
  children: ReactNode;
}) {
  const account = await profile();
  return (
    <div className="dashboard-shell">
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>
      <Sidebar account={account.value?.data ?? null} />
      <div className="dashboard-main">
        <header className="topbar">
          <span className="topbar-context">
            <span className="tiny-mark" />
            {account.value?.data.organization.name ?? 'Workspace'}
          </span>
          <div className="topbar-account">
            <span className="small muted">
              {account.value?.data.user.email}
            </span>
            <SignOut />
          </div>
        </header>
        <MutationProvider>
          <main id="main-content" className="content">
            {account.error && <Failure code={account.error} />}
            {children}
          </main>
        </MutationProvider>
        <footer className="app-footer">
          <span>Portway</span>
          <span>Local → public, on your terms.</span>
        </footer>
      </div>
    </div>
  );
}
