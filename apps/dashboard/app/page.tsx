import Link from 'next/link';
import { Icon, Mark } from '../components/ui';
export default function HomePage() {
  return (
    <main className="welcome">
      <Link prefetch={false} href="/" className="brand">
        <Mark />
        Portway
      </Link>
      <p className="eyebrow">YOUR INFRASTRUCTURE. YOUR WORKSPACE.</p>
      <h1>
        A path from your
        <br />
        local port to the web.
      </h1>
      <p className="muted">
        Manage your tunnels, verify custom domains and keep an eye on your relay
        fleet. All in one place.
      </p>
      <Link prefetch={false} href="/dashboard" className="button primary">
        Open dashboard <Icon />
      </Link>
      <div className="terminal">
        <span className="terminal-dot" />
        <code>portway 3000</code>
        <span className="muted">Local work. Public reach.</span>
      </div>
      <p className="small muted">Self-hosted reverse tunneling</p>
    </main>
  );
}
