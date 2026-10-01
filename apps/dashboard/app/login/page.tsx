import Link from 'next/link';
import { LoginForm } from '../../components/login';
import { Icon, Mark } from '../../components/ui';
export const dynamic = 'force-dynamic';
export default async function LoginPage({
  searchParams,
}: {
  searchParams: Promise<{ reason?: string }>;
}) {
  const reason = (await searchParams).reason;
  return (
    <main className="login-page">
      <section className="login-story">
        <Link prefetch={false} className="brand" href="/">
          <Mark />
          Portway
        </Link>
        <div>
          <p className="eyebrow">FROM LOCAL TO POSSIBLE</p>
          <h1>
            Small port.
            <br />
            Open world.
          </h1>
          <p>
            Give your local work a place on the web.
            <br />
            Keep the infrastructure in your hands.
          </p>
          <div className="path-illustration" aria-hidden="true">
            <span>localhost</span>
            <i />
            <span>
              <Icon />
            </span>
            <i />
            <span>the web</span>
          </div>
        </div>
        <span className="small">Built for developers. Owned by you.</span>
      </section>
      <section className="login-panel">
        <div>
          <p className="eyebrow">YOUR WORKSPACE AWAITS</p>
          <h2>Welcome to Portway</h2>
          <p className="muted">Sign in to manage your connections.</p>
          <LoginForm reason={reason} />
          <div className="login-note">
            <span aria-hidden="true">◇</span>
            <p className="small muted">
              Your key is exchanged for an expiring session. It is never saved
              in browser storage.
            </p>
          </div>
        </div>
      </section>
    </main>
  );
}
