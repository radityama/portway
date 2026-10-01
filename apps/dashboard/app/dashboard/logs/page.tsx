import { load } from '../../../lib/server';
import type { Tunnel, ObservationView } from '../../../lib/contracts';
import { Empty, Failure, Heading, DateText } from '../../../components/ui';
import { Refresh } from '../../../components/actions';
export default async function Logs({
  searchParams,
}: {
  searchParams: Promise<{ tunnelId?: string }>;
}) {
  const tunnels = await load<{ tunnels: Tunnel[] }>('/tunnels?limit=100'),
    selected = (await searchParams).tunnelId;
  const logs =
    selected && /^[A-Za-z0-9_-]{1,128}$/.test(selected)
      ? await load<Pick<ObservationView, 'available' | 'observedAt' | 'logs'>>(
          '/tunnels/' + selected + '/logs',
        )
      : null;
  return (
    <>
      <Heading
        title="Logs"
        description="Inspect available activity for a scoped tunnel."
        action={<Refresh />}
      />
      <section className="panel">
        <form className="filters" action="/dashboard/logs">
          <label>
            Tunnel
            <select
              aria-label="Tunnel"
              name="tunnelId"
              defaultValue={selected ?? ''}
              required
            >
              <option value="">Choose a tunnel</option>
              {tunnels.value?.data.tunnels.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          </label>
          <button className="button secondary">Load logs</button>
        </form>
        {tunnels.error && <Failure code={tunnels.error} />}
        {logs?.error ? (
          <Failure code={logs.error} />
        ) : logs?.value ? (
          !logs.value.data.available ? (
            <Empty title="Request observations unavailable">
              Connect this tunnel and send a request to collect recent metadata.
              Observations expire when relay reports stop.
            </Empty>
          ) : logs.value.data.logs.length ? (
            <>
              <p className="small muted">
                Reported <DateText value={logs.value.data.observedAt} />
              </p>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Time</th>
                      <th>Method</th>
                      <th>Status</th>
                      <th>Duration</th>
                      <th>Received</th>
                      <th>Sent</th>
                      <th>Outcome</th>
                    </tr>
                  </thead>
                  <tbody>
                    {logs.value.data.logs.map((l) => (
                      <tr key={l.id}>
                        <td>
                          <DateText value={l.timestamp} />
                        </td>
                        <td>{l.method}</td>
                        <td>{l.status || '—'}</td>
                        <td>{l.durationMs.toFixed(1)} ms</td>
                        <td>{BigInt(l.bytesIn).toLocaleString()} B</td>
                        <td>{BigInt(l.bytesOut).toLocaleString()} B</td>
                        <td>{l.outcome}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          ) : (
            <Empty title="No recent completed requests">
              This relay has no completed request records within the retention
              window.
            </Empty>
          )
        ) : (
          <Empty title="Choose a tunnel to inspect">
            Select a tunnel to view recent request metadata.
          </Empty>
        )}
      </section>
      <p className="small muted">
        The latest four completed requests are retained for up to one hour.
        URLs, headers and bodies are omitted. These records are not an audit
        archive.
      </p>
    </>
  );
}
