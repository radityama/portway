import Link from 'next/link';
import { load, profile } from '../../../../lib/server';
import type { Domain, Tunnel } from '../../../../lib/contracts';
import {
  Badge,
  DateText,
  Failure,
  Heading,
  Empty,
} from '../../../../components/ui';
import { Copy, Refresh, RevokeTunnel } from '../../../../components/actions';
export default async function Detail({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  if (!/^[A-Za-z0-9_-]{1,128}$/.test(id))
    return <Failure code="TUNNEL_NOT_FOUND" />;
  const [account, detail, domains] = await Promise.all([
    profile(),
    load<{ tunnel: Tunnel }>('/tunnels/' + id),
    load<{ domains: Domain[] }>('/domains?limit=50&tunnelId=' + id),
  ]);
  if (!detail.value)
    return (
      <>
        <Heading
          title="Tunnel unavailable"
          description="This tunnel could not be loaded."
        />
        <Failure code={detail.error!} />
        <Link prefetch={false} href="/dashboard/tunnels">
          ← All tunnels
        </Link>
      </>
    );
  const t = detail.value.data.tunnel,
    command = `PORTWAY_TUNNEL_ID=${t.id} portway ${t.localPort ?? 3000}`;
  return (
    <>
      <Link prefetch={false} className="back-link" href="/dashboard/tunnels">
        ← All tunnels
      </Link>
      <Heading
        eyebrow="TUNNEL DETAILS"
        title={t.name}
        description={
          t.publicHostname ??
          'Connect the agent to assign this tunnel’s hostname.'
        }
        action={<Refresh />}
      />
      <div className="detail-grid">
        <section className="panel">
          <div className="panel-heading">
            <h2>Connection details</h2>
            <Badge status={t.status} />
          </div>
          <dl className="details">
            <div>
              <dt>Tunnel ID</dt>
              <dd className="mono">
                {t.id}
                <Copy text={t.id} label="Copy tunnel ID" />
              </dd>
            </div>
            <div>
              <dt>Local service</dt>
              <dd className="mono">
                {t.localPort
                  ? `http://${t.localHost}:${t.localPort}`
                  : 'Port not configured'}
              </dd>
            </div>
            <div>
              <dt>Type</dt>
              <dd>{t.type.toLowerCase()}</dd>
            </div>
            <div>
              <dt>Relay</dt>
              <dd className="mono">{t.relayId ?? 'Not assigned'}</dd>
            </div>
            <div>
              <dt>Generation</dt>
              <dd className="mono">{t.generation}</dd>
            </div>
            <div>
              <dt>Created</dt>
              <dd>
                <DateText value={t.createdAt} />
              </dd>
            </div>
            <div>
              <dt>Last connected</dt>
              <dd>
                <DateText value={t.lastConnectedAt} />
              </dd>
            </div>
          </dl>
        </section>
        <section className="panel getting-started">
          <p className="eyebrow">CONNECT YOUR LOCAL SERVICE</p>
          <h2>Ready when you are.</h2>
          <p className="muted">
            With your CLI API token file configured, run this on the machine
            serving your application.
          </p>
          <div className="command-box">
            <code>{command}</code>
            <Copy text={command} label="Copy CLI command" />
          </div>
          <p className="small muted">
            The agent chooses the local port and makes an outbound connection.
            Creating or viewing this page does not start an agent.
          </p>
          {t.status === 'REVOKED' && (
            <div className="notice">
              This identity is revoked. Create a new tunnel to reconnect.
            </div>
          )}
        </section>
      </div>
      <section className="panel">
        <div className="panel-heading">
          <h2>Custom domains</h2>
          <Link prefetch={false} href={'/dashboard/domains?tunnelId=' + t.id}>
            Manage domains →
          </Link>
        </div>
        {domains.error ? (
          <Failure code={domains.error} />
        ) : domains.value?.data.domains.length ? (
          <div className="simple-list">
            {domains.value.data.domains.map((d) => (
              <div className="relay-preview" key={d.id}>
                <strong className="mono">{d.hostname}</strong>
                <Badge status={d.status} />
              </div>
            ))}
          </div>
        ) : (
          <Empty title="No custom domains">
            Add and verify a domain to use your own hostname.
          </Empty>
        )}
      </section>
      {account.value?.data.role !== 'VIEWER' &&
        account.value &&
        t.status !== 'REVOKED' && (
          <section className="danger-zone">
            <div>
              <h3>Revoke this tunnel</h3>
              <p className="small muted">
                Invalidate credentials and prevent future connections.
              </p>
            </div>
            <RevokeTunnel tunnel={t} />
          </section>
        )}
    </>
  );
}
