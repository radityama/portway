# Portway

A path from your local port to the web. Portway is a self-hosted reverse-tunneling platform with a Go CLI and relay, a TypeScript control plane, and a Next.js dashboard.

## Current milestone

Phases 0–12 provide a reproducible workspace, a validated v1 protocol, authenticated TLS connections, tunnel registration, and public HTTPS forwarding to a local HTTP service. The relay assigns hostnames, resolves active owners locally, and prevents stale generations from reclaiming tunnels. Requests and responses use bounded logical streams with independent byte-credit windows and a shared connection budget. Registered sessions use heartbeat; port invocations recover transient transport failures with backoff and fresh generations. Signals stop new work, let active streams drain, and force cleanup at a configurable shutdown deadline. Negotiated WebSocket upgrades preserve duplex frames, and SSE/chunked HTTP streams flush incrementally under application idle timeouts. The API implements scoped authentication, projects, tunnels, configured relays and short-lived credentials; the CLI can bootstrap through it. PostgreSQL now persists scoped policy, sessions, generation allocations, credentials, idempotency and audit history through transactional writes. Live relay reports now drive health/capacity selection, operator drain commands and CLI failover. Custom domains now use DNS TXT ownership proofs, current-generation relay aliases and reloadable certificates with local issuance and renewal. The dashboard remains a skeleton.

See [Phase 0](./docs/PHASE_0.md), [Phase 1](./docs/PHASE_1.md), [Phase 2](./docs/PHASE_2.md), [Phase 3](./docs/PHASE_3.md), [Phase 4](./docs/PHASE_4.md), [Phase 5](./docs/PHASE_5.md), [Phase 6](./docs/PHASE_6.md), [Phase 7](./docs/PHASE_7.md), [Phase 8](./docs/PHASE_8.md), [Phase 9](./docs/PHASE_9.md), [Phase 10](./docs/PHASE_10.md), [Phase 11](./docs/PHASE_11.md), [Phase 12](./docs/PHASE_12.md), [Starter Status](./docs/STARTER_STATUS.md), and the canonical [implementation phases](./docs/IMPLEMENTATION.md).

## Prerequisites

- Go 1.26+; the verified version is pinned in [.go-version](./.go-version).
- Node.js 24.19+ within the 24.x release line, pinned in [.node-version](./.node-version).
- pnpm 10.12.1, pinned in `package.json`.
- Docker with Compose v2 and a running daemon.
- Make for the convenience commands.

## Start development

```bash
corepack enable
make setup
make dev
```

`make setup` creates `.env` if missing, installs dependencies from the lockfile, generates the Prisma client, and creates local TLS/credential files in ignored `.tmp/dev`. It also creates a separate public CA and wildcard certificate, preserving existing agent credentials. Existing environment and credential files are preserved. Shell variables take precedence.

`make dev` waits for healthy PostgreSQL and Redis, deploys checked-in migrations and imports the hash-only control seed, builds the agent/relay, and starts the API, dashboard, and relay. Readiness requires API/dashboard checks, an actual authenticated TLS handshake and tunnel registration, and verified public HTTPS.

Default addresses:

| Service      | Address                            |
| ------------ | ---------------------------------- |
| Dashboard    | `http://127.0.0.1:3000`            |
| API health   | `http://127.0.0.1:8080/health`     |
| Relay        | `127.0.0.1:8081` (TLS 1.3)         |
| Public HTTPS | `https://<assigned-hostname>:8443` |
| PostgreSQL   | `127.0.0.1:5432`                   |
| Redis        | `127.0.0.1:6379`                   |

Press Ctrl+C to stop the application processes and remove the development containers and network. PostgreSQL data stays in the `portway_portway-postgres` named volume. Startup failures also clean up resources created by that run. Starting a second development session on the same application ports fails before it touches the running stack.

Change service ports in `.env`. When changing PostgreSQL or Redis ports, also update `DATABASE_URL` or `REDIS_URL`. Development services bind to loopback, including the relay. The CLI uses `RELAY_PORT` unless `PORTWAY_RELAY_ADDR` is set.

If Docker Hub rate-limits image pulls, the official public mirror can be selected:

```bash
POSTGRES_IMAGE=public.ecr.aws/docker/library/postgres:17-alpine \
REDIS_IMAGE=public.ecr.aws/docker/library/redis:8-alpine \
make dev
```

With the relay running, verify one authenticated handshake from another terminal:

```bash
go run ./cmd/portway connect --once
PORTWAY_JSON=1 go run ./cmd/portway connect --once
```

Register a tunnel and hold its relay connection:

```bash
go run ./cmd/portway register
# Or check registration and immediately disconnect:
PORTWAY_JSON=1 go run ./cmd/portway register --once
# With a local service already listening:
go run ./cmd/portway 3000
```

JSON mode reports `relay_authenticated` followed by `tunnel_registered`, including
the assigned hostname and a decimal-string generation. A numeric port invocation
starts HTTP forwarding and emits `tunnel_connected`, `public_url`, and `ready`.
Use the printed HTTPS URL to reach the local service. Diagnostic `register` does
not enable forwarding; `register --once` closes its route immediately after
the ACK. Held diagnostic connections end on Ctrl+C, replacement, heartbeat failure, idle timeout or expiry. Port invocations reconnect after transient transport failure with jittered delays from a 1s base up to 30s, resetting after 60s connected. JSON emits `tunnel_disconnected` and `reconnect_scheduled`, then repeats readiness after recovery. Interrupted requests fail without replay; authentication, TLS verification, protocol and registration failures stop the CLI.
`connect` without registration is diagnostic and ends on the registration timeout.

For the default development tunnel, with a service listening on port 3000 and
`portway 3000` running, verify the response from another terminal:

```bash
portway_host=p-9cbd6a472b3507c67e259cadf5a9ecc5.portway.localhost
curl --cacert .tmp/dev/public-ca.pem \
  --resolve "$portway_host:8443:127.0.0.1" \
  "https://$portway_host:8443/"
```

For another tunnel or port, use the hostname and port in the printed URL. The
public development listener binds to loopback. Browsers need a trusted public
certificate; the setup does not install a system CA. Host and TLS SNI must agree.

The development credential is scoped to `tnl_local_dev`. Operator credentials
must match `PORTWAY_TUNNEL_ID`. The relay assigns stable hostnames under
`PUBLIC_BASE_DOMAIN` (default `portway.localhost`); peers cannot choose hosts.
Generation counters persist per tunnel in private `PORTWAY_STATE_DIR` (default
`.tmp/agent-state`). Failed attempts consume numbers. Keep this directory across
starts; direct-file mode requires coordinated generations across machines, while API mode
allocates generations transactionally in PostgreSQL. Run one agent per tunnel. An explicit `PORTWAY_GENERATION` recovery override
must exceed the relay watermark and the local counter, and is persisted locally.
The override applies to the first reservation in each CLI run; automatic reconnect increments from it. Remove it before the next manual start.

`make dev` loads `.env`; standalone Go commands read shell environment. With a
custom relay port, set the destination explicitly, for example
`PORTWAY_RELAY_ADDR=127.0.0.1:9443 go run ./cmd/portway connect --once`.

Development credentials expire after 24 hours; the certificate expires after
seven days. Stop development and run `make dev-credentials` to rotate both, then
restart. Private files use mode `0600`; their directory uses `0700`. The generator
does not install a system CA and does not print a credential or private key.

For an operator-managed relay, configure `RELAY_TLS_CERT_FILE`,
`RELAY_TLS_KEY_FILE`, and `RELAY_CREDENTIALS_FILE`; set `RELAY_BIND_HOST` explicitly
to expose its listener. The agent uses `PORTWAY_RELAY_ADDR`,
`PORTWAY_RELAY_CA_FILE`, optional `PORTWAY_RELAY_SERVER_NAME`, and
`PORTWAY_TOKEN_FILE`. An empty CA-file value uses system roots. See
[Phase 2](./docs/PHASE_2.md) for credential format and [Phase 3](./docs/PHASE_3.md)
for registration settings and relay-restart limits.

Public ingress has its own `PUBLIC_BIND_HOST`, `PUBLIC_PORT`,
`PUBLIC_TLS_CERT_FILE`, and `PUBLIC_TLS_KEY_FILE`. Exposing it requires DNS and
a certificate covering the assigned hostnames. Phase 4 supports ordinary HTTP
with 32 streams per tunnel, 16 MiB request bodies, 64 MiB response bodies, and
bounded headers. Phase 8 supports WebSocket version 13, SSE and chunked streaming.
Negotiated `streaming` uses a 30-second application idle timeout; older peers keep
the whole-request deadline. Set `RELAY_STREAM_TIMEOUT` and `PORTWAY_STREAM_TIMEOUT`
(positive durations up to 5m) for application heartbeat/event intervals. WebSocket
compression, HTTP/2, CONNECT, generic upgrades and trailers remain unavailable. Phase 5 queues at most 64 KiB per stream and 1 MiB per
connection. A stalled consumer no longer blocks the shared frame reader; many
stalled streams can still fill the shared budget. Update agent and relay together:
HTTP requires negotiated `multiplexing` and `flow_control`. See
[Phase 4](./docs/PHASE_4.md) for HTTP settings and [Phase 5](./docs/PHASE_5.md)
for flow-control bounds. See [Phase 8](./docs/PHASE_8.md) for WebSocket and streaming semantics. Negotiated heartbeat probes every 15s and requires a matching reply within 45s; see [Phase 6](./docs/PHASE_6.md) for recovery policy and terminal errors. Graceful shutdown defaults to 10s; set `PORTWAY_SHUTDOWN_TIMEOUT` or `RELAY_SHUTDOWN_TIMEOUT` to a positive duration up to 1m. See [Phase 7](./docs/PHASE_7.md).

## Quality gates

```bash
make check
make fuzz
pnpm db:validate
pnpm test:bootstrap
```

`make check` verifies Go/Prettier formatting, Go and TypeScript tests, Go race detection, Go vet, ESLint, TypeScript types, production builds, and real API/CLI/relay tests in memory and PostgreSQL modes, plus two-relay failover and Redis presence tests. PostgreSQL integration uses isolated temporary Docker containers to verify migrations, constraints, concurrent writes, rollback, seed safety and outage isolation. Docker must be available. `make fuzz` actively fuzzes decoding, encoding round trips, handshake payloads, stream/window payloads, and WebSocket response headers for 10 seconds each (override with `FUZZTIME=30s`). CI also runs each target for 5 seconds. `pnpm test:bootstrap` requires Docker and checks real development startup, duplicate-start rejection, and interrupt cleanup. Run it when no other Portway development session is using the Compose project.

Stop the development stack before running production builds; Next.js uses the same `.next/` directory for both.

Use `make fmt` to apply formatting. `make doctor` checks development tooling; this is separate from the future `portway doctor` product command.

Build outputs are `bin/portway`, `bin/portway-relay`, API/shared-package `dist/` directories, and the dashboard `.next/` directory. `make docker-up` and `make docker-down` manage just the development dependencies. Checked-in migrations and database integration tests are implemented; broader fleet/load testing remains later work.

## Repository and contracts

- `cmd/portway` — CLI / agent binary
- `cmd/relay` — relay binary
- `apps/api` — Hono control-plane API
- `apps/dashboard` — Next.js dashboard
- `packages` — shared TypeScript contracts
- `internal` — Go data-plane packages
- `prisma` — PostgreSQL schema representation
- `scripts` — development tooling and bootstrap tests
- `docs` — product and engineering contracts

The agent connects outbound to the relay. Public application bytes stay in the data plane; no API or PostgreSQL call is added per public request.

The source-of-truth hierarchy is PRD → architecture → database/API/routing contracts → implementation. `docs/DATABASE.md` defines the logical data model; `prisma/schema.prisma` represents it. `docs/API.md` defines REST semantics; `docs/openapi.yaml` represents them. See the [documentation index](./docs/INDEX.md).

## Control-plane development (Phases 9–10)

`make setup` also creates private `.tmp/dev/api-token`, `relay-api-token`, and
`control-seed.json`. The seed contains hashes and a local organization, project,
`tnl_local_dev` tunnel and configured relay. Development API/relay keys expire
after seven days. Setup preserves them; to reprovision after expiry, stop services,
move those three control files aside, and rerun setup. Existing database policy is preserved by seed reruns. If you change `RELAY_PORT`,
update both the seed and the provisioned relay row; seed import does not overwrite
operator changes. Rotated control keys receive new IDs and do not revive revoked keys.

Enable API credential issuance and verification in the same shell:

```bash
export PORTWAY_API_URL=http://localhost:8080/api/v1
export PORTWAY_API_TOKEN_FILE=.tmp/dev/api-token
export RELAY_API_URL=http://localhost:8080/api/v1
export RELAY_API_TOKEN_FILE=.tmp/dev/relay-api-token
export RELAY_ID=rel_local
make dev
```

Then run `PORTWAY_API_TOKEN_FILE=.tmp/dev/api-token .tmp/portway 3000` in another
shell with a local service on port 3000. `register --once` supports API readiness;
`connect` remains a direct authentication diagnostic. Unset the API settings to
use the previous private-file development mode. Never put raw API tokens in CLI
arguments or URLs. Remote API connections require verified HTTPS; optional
`PORTWAY_API_CA_FILE` and `RELAY_API_CA_FILE` configure private API trust separately
from relay trust. The API binds loopback; a trusted HTTPS proxy provides remote access.

The credential lease defaults to five minutes (`API_CREDENTIAL_TTL_SECONDS`,
1–900 seconds). Expiry closes the session and port invocations obtain a new
credential with a higher generation. Existing sessions serve public traffic
during API outages until their lease expires. Revocation blocks future
authentication; it does not push a close to an already admitted session.

`API_STORAGE=postgres` is the default and requires `DATABASE_URL`. Before starting
a standalone API, explicitly provision the database:

```bash
pnpm db:deploy
pnpm db:seed
```

`db:seed` reads the hash-only `API_SEED_FILE`; migrations and imports never run
inside API startup. Imports create missing rows in one transaction and preserve
existing generations, roles, revocations and audit history. In production, provide
operator-owned identities and hashed keys instead of development seed policy.
Existing databases created outside Prisma migrations must be inspected and
baselined before deployment; the scripts never reset or drop their tables.

Sessions, metadata, generation allocation, credential bindings, idempotency and
revocations survive API restarts. Database failures return `503 STORAGE_UNAVAILABLE`;
`/health` reports process liveness and `/ready` requires usable database policy.
Admitted public traffic continues until its lease expires even during database
outages. Rate buckets and cursor signing keys remain process-local; cursors expire
on API restart and cannot move between instances. `CONNECTING` represents issuance,
not confirmed live presence. Live Redis reports now drive relay health/capacity selection and failover.

Explicit `API_STORAGE=memory` retains the bounded Phase 9 test/development backend;
its mutations disappear on restart. `pnpm test:control` exercises that backend.
`pnpm test:database` checks PostgreSQL contracts, and `pnpm test:database-control`
tests actual API/CLI/relay restart, database outage isolation and durable revocation
after `make build`. See [Phase 10](./docs/PHASE_10.md) for verification and boundaries.

## Relay fleet (Phase 11)

Each node has an operator-provisioned `Relay` row and its own relay-scoped hashed
API key. Configured endpoint metadata stays in PostgreSQL; nodes cannot register
arbitrary destinations. Control-mode relays automatically report through their
`RELAY_API_URL`, token file and relay ID. Reports run every 2s by default
(`RELAY_API_REPORT_INTERVAL`, 100ms–5s) and expire after 15s. Redis is required for
the PostgreSQL backend, with finite connection and complete round-trip deadlines.
No fresh report means OFFLINE; seed status alone cannot admit a new assignment.

`make dev` starts reporting independently of credential verification, preserving
private-file diagnostics. Standalone file-mode nodes can set
`RELAY_REPORT_API_URL`, `RELAY_REPORT_API_TOKEN_FILE`, `RELAY_REPORT_API_CA_FILE`
and `RELAY_REPORT_ID`; these default to the verification API settings. Reporting
does not change the private-file authenticator. Every relay uses a distinct ID/key.
Existing seed/database relay ports must match the configured node listener.

The selector uses live health, durable policy, observed capacity and current
credential reservations. It keeps a usable assigned node and prefers another
healthy node after retryable transport failure. New sessions use higher durable
generations and new credentials. Interrupted application requests fail without
replay. Existing admitted HTTPS/WebSocket/SSE continue through API/database/Redis
outages until their local lease expires or transport fails.

Use relay-key-authenticated `POST /api/v1/internal/relays/:id/drain` to persist
operator drain policy. The reporter observes the command and the node stops
admission, lets active streams finish and exits within its shutdown deadline.
`/activate` clears durable policy; restart the drained process to resume. User
organization keys cannot operate the shared fleet. Ordinary process shutdown
publishes temporary DRAINING state without disabling the durable node identity.
A fresh process can replace a DRAINING report; replacing a live HEALTHY process
with the same ID requires its report to expire, preventing competing reporters.

The hostname stays stable across failover. Local nodes use separate public ports,
so use the latest emitted URL. Production needs ingress/DNS that follows the
assigned relay for each hostname. A random load balancer across nodes cannot
find another node's local mux; this phase does not install an ingress controller
or introduce relay-to-relay application proxying. Capacity snapshots are advisory
and local socket/stream/registry limits remain authoritative.

`pnpm test:fleet-api` verifies policy and fencing across real Redis/PostgreSQL API
instances. `pnpm test:fleet` checks actual two-node graceful drain, Redis outage
isolation, fresh-report transport failover, increasing generations and no replay.
Both run in `make check` and CI. See [Phase 11](./docs/PHASE_11.md).

## Domains and certificates (Phase 12)

Create a domain with authenticated `POST /api/v1/domains` and
`{"hostname":"app.example.test","tunnelId":"tnl_local_dev"}`. Save the returned
one-time verification value, publish it at the returned TXT name, then call
`POST /domains/:id/verify` and `/activate` under the same `/api/v1` prefix.
Ownership DNS and the A/AAAA/CNAME records that send traffic to the assigned relay
are separate steps. `ACTIVE` enables routing policy; the relay also needs a valid
certificate. VIEWER can read; MEMBER/ADMIN/OWNER can change domains within their
organization. Delete disables the association and retains its unique reservation.
`/challenge` rotates proof and can re-enable a disabled association after fresh
verification. Hashes are stored; raw TXT values are returned once and never logged.

Relay reports distribute complete alias snapshots every 2s by default. Public
requests use local policy bound to the current tunnel generation and session
expiry. Disable/revoke/reassignment removes an alias on the next successful report;
an API outage preserves its last snapshot for at most 15 minutes, shortened by
session expiry. A certificate alone cannot authorize a hostname. Agents negotiate
`custom_domains` to preserve the application Host header; older agents return 501
for aliases and continue serving their generated hostname.

For an explicit local CA and renewable wildcard/custom certificate, run:

```bash
make build
bin/portway-cert init --dir .tmp/certificates
bin/portway-cert issue --dir .tmp/certificates \
  --hosts '*.portway.localhost,app.example.test' --days 30 --renew-before 168h
```

Set `PUBLIC_TLS_MANIFEST_FILE` to the absolute path of
`.tmp/certificates/certificate.json` on the relay. Its optional `default` entry
redirects the wildcard pair, and `domains` supplies exact custom-host pairs.
Enable authenticated relay reporting as described above; it supplies domain
policy. Every participating relay needs the appropriate certificate deployment.
The tool publishes immutable PEM bundles and atomically replaces the manifest.
Run the same issue command periodically: a valid matching pair outside its renewal
window is reused; a near-expiry pair gets a new leaf/key under the existing CA.
It preserves agent credentials and does not install system trust. After publishing
ownership proof and activating the alias, test with:

```bash
curl --cacert .tmp/certificates/ca.pem \
  --resolve app.example.test:8443:127.0.0.1 https://app.example.test:8443/
```

Browser clients must explicitly trust this development root. When using this
manifest with `make dev`, also set `PUBLIC_TLS_CA_FILE` to its `ca.pem` so development
readiness trusts the renewed wildcard pair.

Alternatively, mkcert can manage local trust and supply PEM files:

```bash
mkdir -p .tmp/mkcert
mkcert -install
mkcert -cert-file .tmp/mkcert/cert.pem -key-file .tmp/mkcert/key.pem \
  '*.portway.localhost' app.example.test
chmod 600 .tmp/mkcert/key.pem
```

Point `PUBLIC_TLS_CERT_FILE` and `PUBLIC_TLS_KEY_FILE` to those wildcard files.
Write a private (0600), regular manifest alongside them, then set
`PUBLIC_TLS_MANIFEST_FILE` to it:

```json
{
  "domains": [
    {
      "hostname": "app.example.test",
      "certFile": "cert.pem",
      "keyFile": "key.pem"
    }
  ]
}
```

Relative PEM paths use the manifest directory. `PUBLIC_TLS_RELOAD_INTERVAL`
defaults to 30s (100ms–5m). The loader validates coverage, validity, server usage
and matching private keys before swapping its cache. Failed renewal keeps the
prior valid cache; expired certificates reject new TLS handshakes. Private keys
and manifests must be private regular files, and symlinks are rejected. The
request/TLS path reads neither files nor the API, database or DNS.

Production can deploy PEM bundles from an external ACME issuer using the same
atomic manifest workflow. Operators configure public DNS and ingress to follow
the assigned node, client trust and issuer scheduling. ACME account automation
remains future work. Retired local bundles are retained for rollback; remove them
only after confirming that no deployed manifest references them. Domain records
are initially capped at 128, including disabled reservations. See
[Phase 12](./docs/PHASE_12.md) for verification and operational boundaries.
