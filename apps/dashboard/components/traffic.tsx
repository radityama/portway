import Link from 'next/link';
import type { ObservationView } from '../lib/contracts';
import { DateText, Empty, Failure } from './ui';

export function Traffic({
  data,
  error,
}: {
  data?: Pick<ObservationView, 'available' | 'observedAt' | 'metrics'>;
  error?: string | null;
}) {
  const m = data?.metrics;
  return (
    <section className="panel">
      <div className="panel-heading">
        <h2>Traffic</h2>
        {m && (
          <Link
            prefetch={false}
            href={'/dashboard/logs?tunnelId=' + m.tunnelId}
          >
            Recent logs →
          </Link>
        )}
      </div>
      {error ? (
        <Failure code={error} />
      ) : !data?.available || !m ? (
        <Empty title="Request observations unavailable">
          Connect this tunnel and send a request to collect traffic
          measurements.
        </Empty>
      ) : (
        <>
          <p className="small muted">
            Reported <DateText value={data.observedAt} />. Counters cover this
            relay observation window and reset after reconnects or eviction.
          </p>
          <dl className="details">
            <div>
              <dt>Completed requests</dt>
              <dd>{BigInt(m.requests).toLocaleString()}</dd>
            </div>
            <div>
              <dt>Errors and cancellations</dt>
              <dd>{BigInt(m.errors).toLocaleString()}</dd>
            </div>
            <div>
              <dt>Active requests</dt>
              <dd>{m.activeRequests}</dd>
            </div>
            <div>
              <dt>Received</dt>
              <dd>{BigInt(m.bytesIn).toLocaleString()} B</dd>
            </div>
            <div>
              <dt>Sent</dt>
              <dd>{BigInt(m.bytesOut).toLocaleString()} B</dd>
            </div>
            <div>
              <dt>Mean duration</dt>
              <dd>
                {m.requests === '0'
                  ? '—'
                  : ((1000 * m.latencySumSeconds) / Number(m.requests)).toFixed(
                      1,
                    ) + ' ms'}
              </dd>
            </div>
          </dl>
          <p className="small muted">
            Duration includes the full lifetime of streaming requests. HTTP
            bytes measure bodies; WebSocket bytes include frames and the upgrade
            response.
          </p>
        </>
      )}
    </section>
  );
}
