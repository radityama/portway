'use client';
import Link from 'next/link';
export default function ErrorPage({ reset }: { reset: () => void }) {
  return (
    <main className="welcome">
      <h1>Unable to load this page</h1>
      <p className="muted">
        The dashboard could not complete the request. Please try again.
      </p>
      <div className="button-row">
        <button className="button primary" onClick={reset}>
          Try again
        </button>
        <Link prefetch={false} className="button secondary" href="/login">
          Sign in
        </Link>
      </div>
    </main>
  );
}
