# Portway

A path from your local port to the web. Portway is a self-hosted reverse-tunneling platform with a Go CLI and relay, a TypeScript control plane, and a Next.js dashboard.

## Current milestone

Phases 0–8 provide a reproducible workspace, a validated v1 protocol, authenticated TLS connections, tunnel registration, and public HTTPS forwarding to a local HTTP service. The relay assigns hostnames, resolves active owners locally, and prevents stale generations from reclaiming tunnels. Requests and responses use bounded logical streams with independent byte-credit windows and a shared connection budget. Registered sessions use heartbeat; port invocations recover transient transport failures with backoff and fresh generations. Signals stop new work, let active streams drain, and force cleanup at a configurable shutdown deadline. Negotiated WebSocket upgrades preserve duplex frames, and SSE/chunked HTTP streams flush incrementally under application idle timeouts. The API and dashboard are skeletons.

See [Phase 0](./docs/PHASE_0.md), [Phase 1](./docs/PHASE_1.md), [Phase 2](./docs/PHASE_2.md), [Phase 3](./docs/PHASE_3.md), [Phase 4](./docs/PHASE_4.md), [Phase 5](./docs/PHASE_5.md), [Phase 6](./docs/PHASE_6.md), [Phase 7](./docs/PHASE_7.md), [Phase 8](./docs/PHASE_8.md), [Starter Status](./docs/STARTER_STATUS.md), and the canonical [implementation phases](./docs/IMPLEMENTATION.md).

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

`make dev` waits for healthy PostgreSQL and Redis, builds the agent/relay, and starts the API, dashboard, and relay. Readiness requires API/dashboard checks, an actual authenticated TLS handshake and tunnel registration, and verified public HTTPS.

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
starts; two machines sharing a tunnel need coordinated generations until later
control-plane work. Run one agent per tunnel. An explicit `PORTWAY_GENERATION` recovery override
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

`make check` verifies Go/Prettier formatting, Go and TypeScript tests, Go race detection, Go vet, ESLint, TypeScript types, and production builds. `make fuzz` actively fuzzes decoding, encoding round trips, handshake payloads, stream/window payloads, and WebSocket response headers for 10 seconds each (override with `FUZZTIME=30s`). CI also runs each target for 5 seconds. `pnpm test:bootstrap` requires Docker and checks real development startup, duplicate-start rejection, and interrupt cleanup. Run it when no other Portway development session is using the Compose project.

Stop the development stack before running production builds; Next.js uses the same `.next/` directory for both.

Use `make fmt` to apply formatting. `make doctor` checks development tooling; this is separate from the future `portway doctor` product command.

Build outputs are `bin/portway`, `bin/portway-relay`, API/shared-package `dist/` directories, and the dashboard `.next/` directory. `make docker-up` and `make docker-down` manage just the development dependencies. Database migrations, full data-plane integration tests, and load tests remain scheduled in later phases.

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
