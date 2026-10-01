import Link from 'next/link';
import { load, profile, queryString } from '../../../lib/server';
import type { Project, Tunnel } from '../../../lib/contracts';
import {
  Badge,
  Empty,
  Failure,
  Heading,
  Icon,
  Pagination,
} from '../../../components/ui';
import {
  CreateProject,
  CreateTunnel,
  Refresh,
} from '../../../components/actions';
export default async function Tunnels({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const query = queryString(await searchParams, [
    'cursor',
    'projectId',
    'status',
    'relayId',
  ]);
  const [account, tunnels, projects] = await Promise.all([
    profile(),
    load<{ tunnels: Tunnel[] }>('/tunnels?' + query),
    load<{ projects: Project[] }>('/projects?limit=100'),
  ]);
  const ps = projects.value?.data.projects ?? [],
    ts = tunnels.value?.data.tunnels ?? [],
    canEdit = !!account.value && account.value.data.role !== 'VIEWER';
  return (
    <>
      <Heading
        title="Tunnels"
        description="Give your local services a stable place on the web."
        action={
          <div className="button-row">
            <Refresh />
            {canEdit && <CreateTunnel projects={ps} />}
          </div>
        }
      />
      <section className="panel">
        <form className="filters" action="/dashboard/tunnels">
          <label>
            Project
            <select
              aria-label="Project"
              name="projectId"
              defaultValue={query.get('projectId') ?? ''}
            >
              <option value="">All projects</option>
              {ps.map((p) => (
                <option value={p.id} key={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            State
            <select
              aria-label="State"
              name="status"
              defaultValue={query.get('status') ?? ''}
            >
              <option value="">All states</option>
              {[
                'CREATED',
                'CONNECTING',
                'CONNECTED',
                'DISCONNECTED',
                'DRAINING',
                'REVOKED',
              ].map((s) => (
                <option key={s}>{s}</option>
              ))}
            </select>
          </label>
          <button className="button secondary">Apply filters</button>
        </form>
        {tunnels.error ? (
          <Failure code={tunnels.error} />
        ) : ts.length ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Tunnel</th>
                  <th>Project</th>
                  <th>Local service</th>
                  <th>State</th>
                  <th>
                    <span className="sr-only">Details</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {ts.map((t) => (
                  <tr key={t.id}>
                    <td>
                      <Link
                        prefetch={false}
                        className="table-title"
                        href={'/dashboard/tunnels/' + t.id}
                      >
                        {t.name}
                      </Link>
                      <span className="small muted mono">
                        {t.publicHostname ?? 'Hostname assigned on connection'}
                      </span>
                    </td>
                    <td>
                      {ps.find((p) => p.id === t.projectId)?.name ??
                        t.projectId}
                    </td>
                    <td className="mono small">
                      {t.localPort ? `localhost:${t.localPort}` : '—'}
                    </td>
                    <td>
                      <Badge status={t.status} />
                    </td>
                    <td>
                      <Link
                        prefetch={false}
                        aria-label={'View ' + t.name}
                        href={'/dashboard/tunnels/' + t.id}
                      >
                        <Icon />
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty
            title={
              query.has('status') || query.has('projectId')
                ? 'No tunnels match these filters'
                : 'No tunnels yet'
            }
          >
            Create a tunnel, then connect your local service with the CLI.
          </Empty>
        )}
        <Pagination
          path="/dashboard/tunnels"
          query={query}
          cursor={tunnels.value?.meta.nextCursor}
        />
      </section>
      <section className="project-strip">
        <div>
          <h3>Keep your work organized</h3>
          <p className="muted small">
            Projects group tunnels within your workspace.{' '}
            {projects.value?.meta.nextCursor &&
              'Only the first 100 projects are shown in this selector.'}
          </p>
          {projects.error && <Failure code={projects.error} />}
        </div>
        {canEdit && <CreateProject />}
      </section>
      <p className="muted small">
        Connecting means a relay assignment was requested. It does not confirm
        an active agent connection.
      </p>
    </>
  );
}
