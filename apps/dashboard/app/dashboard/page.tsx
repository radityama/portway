import Link from 'next/link';
import { load, profile } from '../../lib/server';
import type { Domain, Project, Relay, Tunnel } from '../../lib/contracts';
import { Badge, Empty, Failure, Heading, Icon } from '../../components/ui';
import { Refresh } from '../../components/actions';
export default async function Overview() {
  const [account, tunnels, domains, relays, projects] = await Promise.all([
    profile(),
    load<{ tunnels: Tunnel[] }>('/tunnels?limit=100'),
    load<{ domains: Domain[] }>('/domains?limit=100'),
    load<{ relays: Relay[] }>('/relays?limit=100'),
    load<{ projects: Project[] }>('/projects?limit=100'),
  ]);
  const errors = [tunnels, domains, relays, projects].filter((x) => x.error);
  const ts = tunnels.value?.data.tunnels ?? [],
    rs = relays.value?.data.relays ?? [],
    ds = domains.value?.data.domains ?? [];
  return (
    <>
      <Heading
        title="Overview"
        description="Your workspace, at a glance."
        action={<Refresh />}
      />
      <section className="overview-hero">
        <div>
          <p className="eyebrow">A CLEAR PATH TO YOUR WORK</p>
          <h2>
            Make local feel
            <br />a little more connected.
          </h2>
          <p>Stable tunnel identities. Your domains. Your relay fleet.</p>
          <Link
            prefetch={false}
            className="button light"
            href="/dashboard/tunnels"
          >
            Explore tunnels <Icon />
          </Link>
        </div>
        <div
          className="connection-map"
          aria-label="Agent connects outward to relay; public requests reach the local service"
        >
          <div className="map-node">
            <span>
              <Icon name="local" />
            </span>
            Local service<small>Your machine</small>
          </div>
          <div className="map-line">
            <i />
            <span>outbound</span>
            <i />
          </div>
          <div className="map-node edge">
            <span>
              <Icon />
            </span>
            Portway relay<small>Public HTTPS</small>
          </div>
        </div>
      </section>
      {errors.map((e, i) => (
        <Failure key={i} code={e.error!} />
      ))}
      <section className="metrics-grid" aria-label="Workspace metadata">
        {[
          [
            'Tunnels',
            tunnels.value
              ? `${ts.length}${tunnels.value.meta.nextCursor ? '+' : ''}`
              : '—',
            'Stable identities',
            'arrow',
          ],
          [
            'Projects',
            projects.value
              ? `${projects.value.data.projects.length}${projects.value.meta.nextCursor ? '+' : ''}`
              : '—',
            'Organized local work',
            'project',
          ],
          [
            'Active domains in view',
            domains.value
              ? String(ds.filter((d) => d.status === 'ACTIVE').length)
              : '—',
            'Enabled routing policy',
            'domain',
          ],
          [
            'Healthy relays in view',
            relays.value
              ? String(rs.filter((r) => r.status === 'HEALTHY').length)
              : '—',
            'Latest node reports',
            'relay',
          ],
        ].map(([label, value, note, icon]) => (
          <div className="metric-card" key={label}>
            <div>
              <span className="muted small">{label}</span>
              <span className="metric-icon" aria-hidden="true">
                <Icon name={icon} />
              </span>
            </div>
            <strong>{value}</strong>
            <span className="muted small">{note}</span>
          </div>
        ))}
      </section>
      <p className="small muted">
        Summary reflects up to 100 items per resource. Connection state is
        stored policy; live health comes from relay reports.
      </p>
      <div className="overview-grid">
        <section className="panel">
          <div className="panel-heading">
            <h2>Your tunnels</h2>
            <Link prefetch={false} href="/dashboard/tunnels">
              View all →
            </Link>
          </div>
          {ts.length ? (
            <div className="simple-list">
              {ts.slice(0, 5).map((t) => (
                <Link
                  prefetch={false}
                  key={t.id}
                  href={'/dashboard/tunnels/' + t.id}
                  className="tunnel-preview"
                >
                  <span className="resource-icon">
                    <Icon />
                  </span>
                  <span>
                    <strong>{t.name}</strong>
                    <span className="small muted mono">
                      {t.localPort
                        ? `localhost:${t.localPort}`
                        : 'Port not configured'}
                    </span>
                  </span>
                  <Badge status={t.status} />
                  <span className="muted">→</span>
                </Link>
              ))}
            </div>
          ) : (
            !tunnels.error && (
              <Empty title="Your first connection starts here">
                Create a tunnel and run the agent on your machine.
              </Empty>
            )
          )}
        </section>
        <section className="panel">
          <div className="panel-heading">
            <h2>Relay health</h2>
            <Link prefetch={false} href="/dashboard/relays">
              View fleet →
            </Link>
          </div>
          {rs.length ? (
            <div className="simple-list">
              {rs.slice(0, 4).map((r) => (
                <div className="relay-preview" key={r.id}>
                  <div>
                    <strong>{r.name}</strong>
                    <span className="small muted">{r.region}</span>
                  </div>
                  <Badge status={r.status} />
                </div>
              ))}
            </div>
          ) : (
            !relays.error && (
              <Empty title="No relays reported">
                An administrator can provision your relay fleet.
              </Empty>
            )
          )}
          <div className="panel-note">
            {account.value?.data.organization.name ?? 'Your workspace'} ·
            self-hosted infrastructure
          </div>
        </section>
      </div>
    </>
  );
}
