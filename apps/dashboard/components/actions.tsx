'use client';
import { createContext, useContext, useRef, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { request, DashboardError } from '../lib/browser';
import type { Domain, Project, Proof, Tunnel } from '../lib/contracts';
import { Failure } from './ui';

const MutationContext = createContext<{
  busy: boolean;
  working: (value: boolean) => void;
  refresh: () => void;
} | null>(null);
export function MutationProvider({ children }: { children: ReactNode }) {
  const [working, setWorking] = useState(false),
    [navigating, setNavigating] = useState(false),
    navigation = useRef(false);
  return (
    <MutationContext.Provider
      value={{
        busy: working || navigating,
        working: setWorking,
        refresh: () => {
          if (navigation.current) return;
          navigation.current = true;
          setNavigating(true);
          window.location.reload();
        },
      }}
    >
      {children}
    </MutationContext.Provider>
  );
}
function useMutationContext() {
  const context = useContext(MutationContext);
  if (!context) throw new Error('Dashboard mutation context is missing');
  return context;
}

function useMutation() {
  const context = useMutationContext(),
    [error, setError] = useState('');
  const busy = context.busy;
  const attempt = useRef<{ fingerprint: string; key: string } | null>(null),
    lock = useRef(false);
  const run = async <T,>(
    path: string,
    method: string,
    body: unknown,
    refresh = true,
  ): Promise<T | null> => {
    if (busy || lock.current) return null;
    lock.current = true;
    const fingerprint = method + path + JSON.stringify(body);
    if (attempt.current?.fingerprint !== fingerprint)
      attempt.current = { fingerprint, key: crypto.randomUUID() };
    context.working(true);
    setError('');
    try {
      const data = await request<T>(
        '/api/control' + path,
        method,
        body,
        attempt.current.key,
      );
      attempt.current = null;
      if (refresh) context.refresh();
      return data ?? ({} as T);
    } catch (e) {
      setError(e instanceof DashboardError ? e.code : 'CONTROL_UNAVAILABLE');
      return null;
    } finally {
      context.working(false);
      lock.current = false;
    }
  };
  return { busy, error, run };
}
export function Copy({
  text,
  label = 'Copy',
}: {
  text: string;
  label?: string;
}) {
  const [state, setState] = useState('');
  return (
    <button
      type="button"
      className="copy-button"
      aria-label={label}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setState('Copied');
        } catch {
          setState('Select and copy');
        }
      }}
    >
      {state || label}
    </button>
  );
}
export function Refresh() {
  const context = useMutationContext();
  return (
    <button
      className="button secondary"
      disabled={context.busy}
      onClick={context.refresh}
    >
      {context.busy ? 'Refreshing…' : '↻ Refresh'}
    </button>
  );
}
function Dialog({
  title,
  trigger,
  children,
  closeRef,
  onClose,
}: {
  title: string;
  trigger: string;
  children: ReactNode;
  closeRef: React.RefObject<HTMLDialogElement | null>;
  onClose?: () => void;
}) {
  const context = useMutationContext();
  return (
    <>
      <button
        className="button primary"
        disabled={context.busy}
        onClick={() => closeRef.current?.showModal()}
      >
        {trigger}
      </button>
      <dialog ref={closeRef} aria-label={title} onClose={onClose}>
        <div className="dialog-heading">
          <h2>{title}</h2>
          <button
            type="button"
            className="icon-button"
            aria-label="Close dialog"
            onClick={() => closeRef.current?.close()}
          >
            ×
          </button>
        </div>
        {children}
      </dialog>
    </>
  );
}
function values(event: FormEvent<HTMLFormElement>) {
  event.preventDefault();
  return new FormData(event.currentTarget);
}
export function CreateProject() {
  const dialog = useRef<HTMLDialogElement>(null),
    action = useMutation();
  return (
    <Dialog title="New project" trigger="New project" closeRef={dialog}>
      <form
        onSubmit={async (e) => {
          const form = e.currentTarget;
          const data = values(e);
          const result = await action.run('/projects', 'POST', {
            name: data.get('name'),
            slug: data.get('slug'),
          });
          if (result) {
            form.reset();
            dialog.current?.close();
          }
        }}
      >
        <label>
          Project name
          <input
            name="name"
            required
            maxLength={100}
            placeholder="My application"
          />
        </label>
        <label>
          Project slug
          <input
            name="slug"
            required
            maxLength={63}
            pattern="[a-z0-9]+(-[a-z0-9]+)*"
            placeholder="my-application"
          />
        </label>
        {action.error && <Failure code={action.error} />}
        <button className="button primary" disabled={action.busy}>
          {action.busy ? 'Creating…' : 'Create project'}
        </button>
      </form>
    </Dialog>
  );
}
export function CreateTunnel({ projects }: { projects: Project[] }) {
  const dialog = useRef<HTMLDialogElement>(null),
    action = useMutation();
  return (
    <Dialog title="New tunnel" trigger="New tunnel" closeRef={dialog}>
      <form
        onSubmit={async (e) => {
          const data = values(e);
          const result = await action.run('/tunnels', 'POST', {
            name: data.get('name'),
            projectId: data.get('projectId'),
            type: data.get('type'),
            protocol: 'http',
            localHost: '127.0.0.1',
            localPort: Number(data.get('localPort')),
          });
          if (result) dialog.current?.close();
        }}
      >
        <label>
          Tunnel name
          <input
            name="name"
            required
            maxLength={100}
            placeholder="Webhook development"
          />
        </label>
        <label>
          Project
          <select aria-label="Project" name="projectId" required>
            <option value="">Select a project</option>
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </label>
        <div className="form-row">
          <label>
            Local port
            <input
              name="localPort"
              type="number"
              min={1}
              max={65535}
              defaultValue={3000}
              required
            />
          </label>
          <label>
            Type
            <select aria-label="Type" name="type">
              <option value="PERSISTENT">Persistent</option>
              <option value="EPHEMERAL">Ephemeral</option>
            </select>
          </label>
        </div>
        <p className="muted small">
          Start the agent on your machine after creating this tunnel.
        </p>
        {action.error && <Failure code={action.error} />}
        <button
          className="button primary"
          disabled={action.busy || !projects.length}
        >
          {action.busy ? 'Creating…' : 'Create tunnel'}
        </button>
      </form>
    </Dialog>
  );
}
export function RevokeTunnel({ tunnel }: { tunnel: Tunnel }) {
  const dialog = useRef<HTMLDialogElement>(null),
    action = useMutation(),
    [confirmation, setConfirmation] = useState('');
  if (tunnel.status === 'REVOKED') return null;
  return (
    <Dialog title="Revoke tunnel" trigger="Revoke tunnel" closeRef={dialog}>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          if (confirmation !== tunnel.name) return;
          if (await action.run('/tunnels/' + tunnel.id + '/revoke', 'POST', {}))
            dialog.current?.close();
        }}
      >
        <p>
          Revoke credentials and prevent future connections for{' '}
          <strong>{tunnel.name}</strong>. Existing connections retain their
          current short lease.
        </p>
        <label>
          Type tunnel name
          <input
            value={confirmation}
            onChange={(e) => setConfirmation(e.target.value)}
            autoComplete="off"
          />
        </label>
        {action.error && <Failure code={action.error} />}
        <button
          className="button danger"
          disabled={action.busy || confirmation !== tunnel.name}
        >
          {action.busy ? 'Revoking…' : 'Confirm revocation'}
        </button>
      </form>
    </Dialog>
  );
}
export function ProofPanel({ proof }: { proof: Proof }) {
  return (
    <div className="proof-panel" role="status">
      <strong>Publish this DNS TXT record</strong>
      <p className="small muted">
        Save this one-time value now. It expires after 24 hours. Verification
        does not configure traffic routing or certificates.
      </p>
      <label>
        Record name
        <div className="copy-field">
          <code>{proof.name}</code>
          <Copy text={proof.name} label="Copy record name" />
        </div>
      </label>
      <label>
        Record value
        <div className="copy-field">
          <code>{proof.value}</code>
          <Copy text={proof.value} label="Copy record value" />
        </div>
      </label>
    </div>
  );
}
export function AddDomain({ tunnels }: { tunnels: Tunnel[] }) {
  const dialog = useRef<HTMLDialogElement>(null),
    action = useMutation(),
    context = useMutationContext(),
    [proof, setProof] = useState<Proof | null>(null);
  return (
    <Dialog
      title="Add domain"
      trigger="Add domain"
      closeRef={dialog}
      onClose={() => {
        if (proof) context.refresh();
      }}
    >
      {proof ? (
        <>
          <ProofPanel proof={proof} />
          <button
            className="button primary"
            disabled={action.busy}
            onClick={() => {
              context.refresh();
            }}
          >
            I saved the TXT record
          </button>
        </>
      ) : (
        <form
          onSubmit={async (e) => {
            const data = values(e);
            const result = await action.run<{
              domain: Domain;
              verification: Proof;
            }>(
              '/domains',
              'POST',
              {
                hostname: data.get('hostname'),
                tunnelId: data.get('tunnelId'),
              },
              false,
            );
            if (result?.verification) setProof(result.verification);
          }}
        >
          <label>
            Hostname
            <input
              name="hostname"
              required
              maxLength={220}
              placeholder="app.example.com"
              autoCapitalize="none"
            />
          </label>
          <label>
            Tunnel
            <select aria-label="Tunnel" name="tunnelId" required>
              <option value="">Select a tunnel</option>
              {tunnels
                .filter((t) => t.status !== 'REVOKED')
                .map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
            </select>
          </label>
          <p className="small muted">
            You’ll receive a DNS TXT record to prove ownership.
          </p>
          {action.error && <Failure code={action.error} />}
          <button
            className="button primary"
            disabled={
              action.busy || !tunnels.some((t) => t.status !== 'REVOKED')
            }
          >
            {action.busy ? 'Adding…' : 'Generate ownership record'}
          </button>
        </form>
      )}
    </Dialog>
  );
}
export function DomainActions({ domain }: { domain: Domain }) {
  const action = useMutation(),
    context = useMutationContext(),
    proofDialog = useRef<HTMLDialogElement>(null),
    [proof, setProof] = useState<Proof | null>(null),
    [disable, setDisable] = useState(false),
    [confirmation, setConfirmation] = useState('');
  return (
    <div className="domain-actions">
      <div className="button-row">
        {domain.status === 'PENDING_VERIFICATION' && (
          <button
            className="button secondary"
            disabled={action.busy}
            onClick={async () => {
              if (
                await action.run(
                  '/domains/' + domain.id + '/verify',
                  'POST',
                  {},
                )
              )
                setProof(null);
            }}
          >
            Verify DNS
          </button>
        )}
        {domain.status === 'VERIFIED' && (
          <button
            className="button primary"
            disabled={action.busy}
            onClick={() =>
              action.run('/domains/' + domain.id + '/activate', 'POST', {})
            }
          >
            Activate domain
          </button>
        )}
        <button
          className="button secondary"
          disabled={action.busy}
          onClick={async () => {
            if (
              domain.status === 'ACTIVE' &&
              !window.confirm(
                'A new challenge removes the active alias until you verify and activate it again. Continue?',
              )
            )
              return;
            const result = await action.run<{ verification: Proof }>(
              '/domains/' + domain.id + '/challenge',
              'POST',
              {},
              false,
            );
            if (result?.verification) {
              setProof(result.verification);
              proofDialog.current?.showModal();
            }
          }}
        >
          {domain.status === 'DISABLED'
            ? 'Re-enable with new proof'
            : 'New challenge'}
        </button>
        {domain.status !== 'DISABLED' && (
          <button
            className="button text-danger"
            disabled={action.busy}
            onClick={() => setDisable(true)}
          >
            Disable
          </button>
        )}
      </div>
      {disable && (
        <form
          className="confirmation"
          onSubmit={async (e) => {
            e.preventDefault();
            if (
              confirmation === domain.hostname &&
              (await action.run('/domains/' + domain.id, 'DELETE', {}))
            ) {
              setDisable(false);
              setProof(null);
            }
          }}
        >
          <label>
            Type hostname to disable
            <input
              value={confirmation}
              onChange={(e) => setConfirmation(e.target.value)}
              autoComplete="off"
            />
          </label>
          <div className="button-row">
            <button
              className="button danger"
              disabled={action.busy || confirmation !== domain.hostname}
            >
              Confirm disable
            </button>
            <button
              type="button"
              className="button secondary"
              onClick={() => setDisable(false)}
            >
              Cancel
            </button>
          </div>
        </form>
      )}
      {action.error && <Failure code={action.error} />}
      <dialog
        ref={proofDialog}
        aria-label="Domain ownership record"
        onClose={() => {
          if (proof) context.refresh();
        }}
      >
        <div className="dialog-heading">
          <h2>Domain ownership record</h2>
          <button
            type="button"
            className="icon-button"
            aria-label="Close ownership record"
            onClick={() => proofDialog.current?.close()}
          >
            ×
          </button>
        </div>
        {proof && <ProofPanel proof={proof} />}
        <button
          className="button primary"
          disabled={context.busy}
          onClick={context.refresh}
        >
          I saved the TXT record
        </button>
      </dialog>
    </div>
  );
}
