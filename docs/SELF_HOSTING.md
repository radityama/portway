# Self-hosting baseline

The supported baseline uses a Linux control host (API/dashboard, PostgreSQL 17,
Redis 8 and HTTPS ingress) and a separate Linux relay host. Review all examples
before copying them. No script here installs services, opens firewalls, generates
production credentials or changes DNS automatically. Pin release/image versions
and keep the previous release available for rollback.

## Control host

Create a dedicated `portway` account and private `/etc/portway` directory. Check
out a reviewed release under `/opt/portway/releases/<version>`; point
`/opt/portway/current` at that directory. Use the pinned Node/pnpm versions and run
`pnpm install --frozen-lockfile`, `pnpm db:generate` and `pnpm build` before service
activation. Set `/usr/local/bin/node` to the pinned installation. API and dashboard
bind loopback; expose them only through the reviewed HTTPS ingress example in
`deploy/nginx/control.conf`. Use real API/console hostnames and certificates.
The service account must read the source/dependencies and own the dashboard's
existing `.next/cache` directory. The dashboard unit permits writes only there;
create/check that path before activation. Root-owned release code should remain
readable/executable by the service account without being writable by it.

Supply a private root-owned 0600 `/etc/portway/control.env` to systemd with:

```text
API_PORT=8080
API_STORAGE=postgres
DATABASE_URL=postgresql://<account>:<encoded-secret>@127.0.0.1:5432/portway?schema=public
REDIS_URL=redis://127.0.0.1:6379
PUBLIC_BASE_DOMAIN=tunnels.example.com
API_CREDENTIAL_TTL_SECONDS=300
DASHBOARD_API_URL=https://api.example.com/api/v1
DASHBOARD_ORIGIN=https://console.example.com
DASHBOARD_SESSION_KEY_FILE=/etc/portway/dashboard-session-key
```

Database URLs/passwords are not argv or source code. Use authenticated private
Redis/database networking in production. systemd reads the environment file as
root; the dashboard key and provisioning seed are private regular files owned by
the service account. Provision the dashboard key through a secret manager; use a
32-byte base64url key. API startup does not automatically seed or migrate.

Before activation, take a database backup and run `pnpm db:deploy` in the reviewed
source with the private connection environment supplied by your service manager.
Provision a reviewed private seed with `pnpm db:seed` and `API_SEED_FILE` pointing
to it. Existing provisioning is create-only and does not reset runtime policy.
Use operator-issued user and per-relay keys; development seed/certificates are
not production provisioning. The relay inventory must advertise the correct
reachable relay hostname, TLS port and capacity.

Copy/review the API/dashboard units from `deploy/systemd`, then explicitly enable
them through your service manager. Run migrations as an operator step, never in
every API process start. Verify `/health`, `/ready`, HTTPS identity and dashboard
login/logout before allowing users. Keep `/internal/*` and `/metrics` outside
public ingress; the example restricts them to loopback or your reviewed relay
source networks. Authenticated relay HTTPS calls need that network access.

## Relay host

Install verified `portway-relay`/`portway-cert` binaries under a versioned
`/opt/portway/releases/<version>/bin` and use the reviewed relay unit. Provision a
private root-owned `/etc/portway/relay.env`:

```text
RELAY_ID=rel_example
RELAY_PORT=8081
RELAY_BIND_HOST=0.0.0.0
RELAY_API_URL=https://api.example.com/api/v1
RELAY_API_TOKEN_FILE=/etc/portway/relay-api-token
RELAY_TLS_CERT_FILE=/etc/portway/relay-cert.pem
RELAY_TLS_KEY_FILE=/etc/portway/relay-key.pem
PUBLIC_BASE_DOMAIN=tunnels.example.com
PUBLIC_PORT=443
PUBLIC_BIND_HOST=0.0.0.0
PUBLIC_TLS_CERT_FILE=/etc/portway/public-cert.pem
PUBLIC_TLS_KEY_FILE=/etc/portway/public-key.pem
PUBLIC_TLS_MANIFEST_FILE=/etc/portway/certificate.json
RELAY_METRICS_PORT=9091
RELAY_SHUTDOWN_TIMEOUT=10s
```

Set `RELAY_API_CA_FILE` when the API has a private CA. Keys/tokens/manifests must be private files readable by
the service account. Agent TLS and public TLS have separate certificates; both
need correct SAN coverage and renewable production issuers. The relay validates
and atomically reloads complete public certificate manifests. Deliver certificate
changes through same-directory atomic replacement and verify logs/expiry after
renewal. ACME issuer/account automation remains external.

Open TCP 8081 for outbound agent connections and TCP 443 for public applications.
Expose local 9091 metrics only to the operator's scrape path. Point the tunnel
wildcard and custom-domain DNS at the serving relay. For multiple relays, ingress
or DNS must follow assignment; this baseline does not install an assignment-aware
ingress controller. Public hostname entropy does not authenticate applications.

The relay unit grants only `CAP_NET_BIND_SERVICE` for native HTTPS on 443. The
API/dashboard grant no capabilities. The optional `deploy/docker/relay.Dockerfile`
packages already-built static release assets using a non-root CA-bearing runtime.
Build with explicit `VERSION` and `TARGETARCH`; review/pin the base-image digest
for deployment. Mount private secrets read-only and run with a read-only root,
bounded PIDs/memory, dropped capabilities and only the selected bind capability
when using 443. Match mounted-file UID/ACLs to the runtime user (65532 by default).
Use explicit published ports and verified API TLS; never bake keys into images.

Build the native architecture image from verified release assets:

```bash
docker build -f deploy/docker/relay.Dockerfile --build-arg VERSION=v0.1.0 --build-arg TARGETARCH=amd64 --build-arg REVISION=<release-commit> -t portway-relay:v0.1.0 .
```

For arm64 on a different host, select `--platform linux/arm64` and `TARGETARCH=arm64`.
The default runtime is pinned to a reviewed multi-architecture digest; refresh it
through a reviewed update rather than using an unpinned production base.

## Validation, upgrade and rollback

1. Verify artifact hashes/provenance and private-file ownership. Confirm DNS,
   certificate SANs/expiry, readiness, operator metrics and `portway doctor`.
2. Exercise HTTP/SSE/WebSocket forwarding and a managed-session lifecycle from
   a disposable developer project. Verify scope and logs contain no credentials.
3. Before upgrades, back up PostgreSQL to independent storage and test restore
   into an isolated instance. Redis contains ephemeral presence, not durable policy.
   Record the deployed revision and current generation/presence state.
4. Apply additive migrations once, start a healthy alternative relay, and drain
   the old relay through the authenticated operator API before stopping it.
   Wait for active streams to finish where practical; SIGTERM has a bounded drain.
   Never use database edits to bypass generation or relay-incarnation fencing.
5. Switch the versioned `current` symlink atomically on that host and restart its
   reviewed units. Validate readiness, new assignments and existing public URLs.
6. Roll back the binary/source symlink to the previous compatible release if
   checks fail. Database rollback requires reviewed migration compatibility;
   restoring a backup is a separate planned operation and is not automatic.

Single-relay restarts interrupt service and same-ID replacement can wait for the
15-second presence fence plus retry delay. Existing admitted application sessions
can continue through API/database outages only within their documented leases.
The local checks are release/runtime regression evidence, not an Internet-facing
deployment certification or a long capacity soak.

Run the isolated Phase 19 checks and record the actual operator deployment evidence
in [ACCEPTANCE.md](./ACCEPTANCE.md) before inviting users.
