# Portway Repository Status

## Phase 0

Phase 0 bootstrap is implemented. Toolchains, dependency installs, formatting,
linting, tests, builds, environment initialization, development supervision, and
CI are configured. See [PHASE_0.md](./PHASE_0.md) for the delivery boundary and
acceptance commands.

## Phase 1

The protocol package now implements v1 framing, explicit message values, header
validation before payload allocation, configurable frame limits, complete
partial writes, and bounded version/capability negotiation. Shared fixtures
verify Go and TypeScript compatibility. Unit, pipe I/O, race, and active fuzz
checks cover failure behavior. See [PHASE_1.md](./PHASE_1.md).

## Phase 2

Authenticated agent-to-relay TLS is implemented. Both sides require TLS 1.3 and
Portway ALPN, follow HELLO/AUTH ordering, bound network operations, and close on
cancellation or credential expiry. The relay caps unauthenticated and active
connections and joins them on shutdown. The file verifier uses token hashes,
connect scope, expiration, and revocation. The CLI reports the actual
`relay_authenticated` milestone; development readiness verifies it end to end.
See [PHASE_2.md](./PHASE_2.md).

## Phase 3

Credential-bound registration, relay-assigned hostnames, hostname lookup, strict
generation replacement, bounded retained watermarks, and owner-aware cleanup are
implemented. The CLI persists generation reservations and emits `tunnel_registered`;
development readiness verifies registration. See [PHASE_3.md](./PHASE_3.md).

## Phase 4

Public HTTPS forwards ordinary HTTP requests to the agent's fixed loopback port.
Host/SNI validation, bounded logical streams, body/header limits, full duplex
streaming, half-close/reset, cancellation, timeouts, and session replacement are
implemented. The CLI emits its HTTPS URL and forwarding readiness. Development
readiness now verifies both agent registration and public TLS. See
[PHASE_4.md](./PHASE_4.md).

## Phase 5

Independent 64 KiB stream and 1 MiB connection credit windows, binary
WINDOW_UPDATE, bounded reusable receive pages, and asynchronous credit return
are implemented. A slow consumer no longer blocks the shared frame reader.
Cancellation/reset return connection credit and wake waiting writers; stream
workers retain admission until cleanup completes. HTTP requires negotiation of
both stream capabilities. See [PHASE_5.md](./PHASE_5.md).

## Phase 6

Negotiated PING/PONG, bounded liveness workers and automatic port-invocation
reconnect with capped exponential backoff/equal jitter are implemented. Fresh
sessions authenticate and reserve higher generations. Transport loss cancels
active HTTP work without replay; terminal auth/TLS/protocol/registration errors
stop. See [PHASE_6.md](./PHASE_6.md).

## Phase 7

Negotiated GOAWAY closes admission, preserves active streams and exchanges
DRAINED before transport closure so buffered responses are consumed. Agent and
relay signals drain within a configurable 10s deadline, then cancel sockets and
join workers. Relay drain returns 503 for new public requests, rejects new tunnel
connections, and retains ownership watermarks. Remote draining schedules normal
port-invocation reconnect; diagnostics exit without reconnect. See [PHASE_7.md](./PHASE_7.md).

## Phase 8

Negotiated WebSocket version 13 upgrades validate both handshakes and preserve
buffered, masked, text/binary, fragmented and control frames over bounded duplex
streams. SSE headers/events and chunked request/response bodies forward
incrementally. Application idle supervision supports active long-lived streams;
legacy peers retain ordinary HTTP metadata and whole-request deadlines. Socket
admission, cancellation, supersession and graceful/forced shutdown include
upgraded connections. Compression, generic upgrades, HTTP/2 and trailers remain
unsupported. See [PHASE_8.md](./PHASE_8.md).

Phases 9–10 add the scoped control API and durable PostgreSQL policy described
below. The next phase is Phase 11: live relay health, capacity and selection.

## What this starter contains

- monorepo layout for agent, relay, API, dashboard, shared packages, and docs
- PostgreSQL schema implementation matching the Prisma baseline in `docs/DATABASE.md`
- OpenAPI contract derived from `docs/API.md`
- implemented versioned protocol framing and capability-negotiation package
- agent/relay TLS handshake, connection lifecycle, and private development setup
- tunnel registration, hostname resolution, generation ownership, and local counters
- public HTTPS routing, bounded HTTP streams, and fixed loopback forwarding
- stream/connection credit windows, bounded receive pages, and backpressure
- registered-session heartbeat and cancellable CLI reconnect with fresh generations
- negotiated drain completion and deadline-bound agent/relay signal shutdown
- validated WebSocket upgrades, incremental SSE/chunked HTTP and streaming idle lifetimes
- scoped control-plane HTTP API with PostgreSQL persistence and explicit memory mode
- baseline/additive migrations, safe provisioning and real database integration tests
- Docker Compose for PostgreSQL and Redis
- CI quality gates and Docker-backed bootstrap verification
- Codex/Claude Code instructions and master prompt

## Important source-of-truth rule

`docs/DATABASE.md` is authoritative for the logical data model. The Prisma file is an implementation representation. It should not acquire extra domain entities, fields, or enums without first updating the database documentation.

`docs/API.md` is authoritative for API semantics and `docs/openapi.yaml` is its machine-readable representation.

## Verification

Run the following locally when the required toolchains/dependencies are installed:

```bash
make setup
make check
make fuzz
pnpm db:validate
pnpm test:bootstrap
```

## Phase 9

The API implements provisioned-key/session authentication, organization/role
authorization, projects/tunnels, configured relay metadata and short-lived
credentials in bounded in-memory state. Strict JSON, cursor binding, bounded
idempotency/admission and private hash-only seeds protect this boundary. Relay
AUTH verification binds tunnel/relay/generation/expiry; public routing stays
local. Opt-in CLI bootstrap and lease refresh obtain new credentials and higher
generations, including recovery after API outage/restart. Durability, dynamic
relay presence and the dashboard remain later phases. See [PHASE_9.md](./PHASE_9.md).

## Phase 10

PostgreSQL is the default API backend. Checked-in Prisma migrations enforce
relational, scope, hash, role, port and exact uint64 constraints. Create-only
hash-only seed import preserves runtime policy. Scoped reads and atomic writes
persist sessions, projects/tunnels, generation allocations, credential bindings,
idempotency and audit history. Concurrent API instances share bounded transaction
admission; no database call enters the public request hot path. Database/API
outages preserve admitted HTTPS/SSE until lease expiry, and restart retains
revocations. Cursor keys/rate limits remain process-local, writer serialization is
an initial throughput boundary, and live relay presence remains Phase 11. See
[PHASE_10.md](./PHASE_10.md).
