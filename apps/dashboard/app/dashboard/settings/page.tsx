import { profile } from '../../../lib/server';
import { DateText, Failure, Heading } from '../../../components/ui';
import { SignOut } from '../../../components/shell';
export default async function Settings() {
  const result = await profile();
  if (!result.value) return <Failure code={result.error!} />;
  const p = result.value.data;
  return (
    <>
      <Heading
        title="Settings"
        description="Your account and workspace context."
      />
      <div className="detail-grid">
        <section className="panel">
          <div className="panel-heading">
            <h2>Your account</h2>
          </div>
          <dl className="details">
            <div>
              <dt>Name</dt>
              <dd>{p.user.displayName ?? 'Not set'}</dd>
            </div>
            <div>
              <dt>Email</dt>
              <dd>{p.user.email}</dd>
            </div>
            <div>
              <dt>Member since</dt>
              <dd>
                <DateText value={p.user.createdAt} />
              </dd>
            </div>
          </dl>
        </section>
        <section className="panel">
          <div className="panel-heading">
            <h2>Workspace</h2>
          </div>
          <dl className="details">
            <div>
              <dt>Organization</dt>
              <dd>{p.organization.name}</dd>
            </div>
            <div>
              <dt>Slug</dt>
              <dd className="mono">{p.organization.slug}</dd>
            </div>
            <div>
              <dt>Your role</dt>
              <dd>{p.role.toLowerCase()}</dd>
            </div>
            <div>
              <dt>Organization ID</dt>
              <dd className="mono">{p.organization.id}</dd>
            </div>
          </dl>
        </section>
      </div>
      <section className="panel session-panel">
        <div>
          <p className="eyebrow">CURRENT BROWSER SESSION</p>
          <h2>A short-lived connection.</h2>
          <p className="muted">
            Sessions expire within one hour, or sooner if the parent API key
            expires. Sign out to revoke this session.
          </p>
          <p className="small muted">
            Account edits, team membership and API key rotation are managed by
            your administrator.
          </p>
        </div>
        <SignOut />
      </section>
    </>
  );
}
