import { load } from '../../../lib/server';
import type { Tunnel } from '../../../lib/contracts';
import { Empty, Failure, Heading } from '../../../components/ui';
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
      ? await load<Record<string, unknown>>('/tunnels/' + selected + '/logs')
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
        {logs?.error === 'NOT_IMPLEMENTED' ? (
          <Empty title="Traffic logs are coming in Phase 14">
            The control API does not expose request logs yet. No traffic has
            been recorded or simulated for this view.
          </Empty>
        ) : logs?.error ? (
          <Failure code={logs.error} />
        ) : logs?.value ? (
          <pre className="log-output">
            {JSON.stringify(logs.value.data, null, 2)}
          </pre>
        ) : (
          <Empty title="Choose a tunnel to inspect">
            Only activity exposed by the control API appears here.
          </Empty>
        )}
      </section>
      <p className="small muted">
        Request metrics, bandwidth and audit browsing await their API contracts.
        Raw request bodies and credentials are never recorded by this dashboard.
      </p>
    </>
  );
}
