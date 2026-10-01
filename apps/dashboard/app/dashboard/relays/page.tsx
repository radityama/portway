import { load, queryString } from '../../../lib/server';
import type { Relay } from '../../../lib/contracts';
import {
  Badge,
  DateText,
  Empty,
  Failure,
  Heading,
  Pagination,
} from '../../../components/ui';
import { Refresh } from '../../../components/actions';
export default async function Relays({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const query = queryString(await searchParams, ['cursor']),
    result = await load<{ relays: Relay[] }>('/relays?' + query);
  return (
    <>
      <Heading
        title="Relays"
        description="The infrastructure connecting local work to the web."
        action={<Refresh />}
      />
      <div className="notice">
        Health and capacity come from the latest node reports. Fleet drain and
        activation require an operator’s relay key.
      </div>
      {result.error ? (
        <Failure code={result.error} />
      ) : result.value?.data.relays.length ? (
        <div className="relay-grid">
          {result.value.data.relays.map((r) => (
            <section className="panel relay-card" key={r.id}>
              <div className="domain-heading">
                <span className="resource-icon">⬡</span>
                <div>
                  <h2>{r.name}</h2>
                  <span className="small muted">{r.region}</span>
                </div>
                <Badge status={r.status} />
              </div>
              <p className="mono small">
                {r.hostname}:{r.port}
              </p>
              {r.capacity ? (
                <>
                  <div className="capacity-label">
                    <span>Connections</span>
                    <strong>
                      {r.capacity.activeConnections} /{' '}
                      {r.capacity.maxConnections}
                    </strong>
                  </div>
                  <progress
                    aria-label={r.name + ' connection capacity'}
                    max={r.capacity.maxConnections}
                    value={r.capacity.activeConnections}
                  />
                  <div className="relay-counters">
                    <div>
                      <strong>{r.capacity.activeTunnels}</strong>
                      <span>Active tunnels</span>
                    </div>
                    <div>
                      <strong>{r.capacity.activeStreams}</strong>
                      <span>Active streams</span>
                    </div>
                  </div>
                </>
              ) : (
                <p className="muted">No fresh capacity report</p>
              )}
              <div className="small muted relay-updated">
                Last report · <DateText value={r.lastSeenAt} />
              </div>
            </section>
          ))}
        </div>
      ) : (
        <section className="panel">
          <Empty title="No relay nodes">
            An operator can provision the relay fleet.
          </Empty>
        </section>
      )}
      <Pagination
        path="/dashboard/relays"
        query={query}
        cursor={result.value?.meta.nextCursor}
      />
    </>
  );
}
