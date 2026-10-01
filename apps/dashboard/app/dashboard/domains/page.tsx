import Link from 'next/link';
import { load, profile, queryString } from '../../../lib/server';
import type { Domain, Tunnel } from '../../../lib/contracts';
import {
  Badge,
  DateText,
  Empty,
  Failure,
  Heading,
  Icon,
  Pagination,
} from '../../../components/ui';
import { AddDomain, DomainActions, Refresh } from '../../../components/actions';
export default async function Domains({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const query = queryString(await searchParams, ['cursor', 'tunnelId']);
  const [account, domains, tunnels] = await Promise.all([
    profile(),
    load<{ domains: Domain[] }>('/domains?' + query),
    load<{ tunnels: Tunnel[] }>('/tunnels?limit=100'),
  ]);
  const canEdit = !!account.value && account.value.data.role !== 'VIEWER',
    ts = tunnels.value?.data.tunnels ?? [];
  return (
    <>
      <Heading
        title="Domains"
        description="Your hostname. Your local application."
        action={
          <div className="button-row">
            <Refresh />
            {canEdit && <AddDomain tunnels={ts} />}
          </div>
        }
      />
      <div className="notice">
        <strong>A domain takes three steps.</strong> Add the hostname, verify
        its DNS TXT record, then activate it. Your relay also needs a matching
        certificate and traffic DNS.
      </div>
      <form className="filters" action="/dashboard/domains">
        <label>
          Tunnel
          <select
            aria-label="Tunnel"
            name="tunnelId"
            defaultValue={query.get('tunnelId') ?? ''}
          >
            <option value="">All tunnels</option>
            {ts.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </select>
        </label>
        <button className="button secondary">Apply filter</button>
      </form>
      {tunnels.error && <Failure code={tunnels.error} />}
      {domains.error ? (
        <Failure code={domains.error} />
      ) : domains.value?.data.domains.length ? (
        <div className="domain-list">
          {domains.value.data.domains.map((d) => (
            <section className="panel domain-card" key={d.id}>
              <div className="domain-heading">
                <span className="resource-icon">
                  <Icon name="domain" />
                </span>
                <div>
                  <h2>{d.hostname}</h2>
                  {d.tunnelId && (
                    <Link
                      prefetch={false}
                      className="small muted"
                      href={'/dashboard/tunnels/' + d.tunnelId}
                    >
                      {ts.find((t) => t.id === d.tunnelId)?.name ?? d.tunnelId}{' '}
                      <Icon />
                    </Link>
                  )}
                </div>
                <Badge status={d.status} />
              </div>
              <p className="small muted">
                {d.status === 'ACTIVE'
                  ? 'Routing enabled. Certificate readiness is managed on the relay.'
                  : d.status === 'VERIFIED'
                    ? 'Ownership verified. Activate when DNS and the relay certificate are ready.'
                    : d.status === 'DISABLED'
                      ? 'Association disabled. Hostname reservation retained.'
                      : 'Waiting for ownership proof.'}{' '}
                {d.status === 'PENDING_VERIFICATION' && (
                  <>
                    Challenge expires{' '}
                    <DateText value={d.verificationExpiresAt} />.
                  </>
                )}
              </p>
              {canEdit && <DomainActions domain={d} />}
            </section>
          ))}
        </div>
      ) : (
        <section className="panel">
          <Empty title="Make it your own">
            Add a custom domain and prove ownership with DNS.
          </Empty>
        </section>
      )}
      <Pagination
        path="/dashboard/domains"
        query={query}
        cursor={domains.value?.meta.nextCursor}
      />
      {tunnels.value?.meta.nextCursor && (
        <p className="muted small">
          The tunnel selector shows the first 100 tunnels.
        </p>
      )}
    </>
  );
}
