# Portway — Implementation Plan

## 1. Implementation Strategy

Build from the data plane outward.

Do not begin with a feature-rich dashboard. The core value and highest risk are:

- protocol correctness
- relay behavior
- stream multiplexing
- backpressure
- heartbeat/reconnect
- failure isolation

Implementation must proceed in small, verifiable phases.

## 2. Technology Baseline

### Agent

Go

### Relay

Go

### Control Plane

TypeScript + Hono on Node.js (API), Next.js (dashboard)

### Database

PostgreSQL + Prisma

### Ephemeral distributed state

Redis

### Dashboard

Next.js + Tailwind CSS + shadcn/ui

### Metrics

Prometheus

### Future transport

QUIC

## 3. Repository Layout

```text
portway/
├── apps/
│   ├── dashboard/
│   └── api/
├── cmd/
│   ├── portway/
│   └── relay/
├── internal/
│   ├── agent/
│   ├── relay/
│   ├── protocol/
│   ├── transport/
│   ├── portway/
│   ├── routing/
│   ├── auth/
│   ├── config/
│   ├── health/
│   └── observability/
├── packages/
│   ├── protocol/
│   ├── config/
│   └── sdk/
├── prisma/
├── proto/
├── deploy/
├── docs/
├── tests/
├── AGENTS.md
├── README.md
├── Makefile
├── go.mod
└── package.json
```

## 4. Phase 0 — Bootstrap

Deliver:

- monorepo
- Go modules
- TypeScript workspace
- linting
- formatting
- test runners
- Docker Compose
- PostgreSQL
- Redis
- base docs
- AGENTS.md

Definition of done:

```bash
make dev
```

starts local dependencies and application skeletons.

The Phase 0 implementation uses pinned toolchains, frozen dependency installs,
explicit formatting/linting, meaningful API/bootstrap tests, and supervised
startup/shutdown. Run `make setup` first; see [PHASE_0.md](./PHASE_0.md) for the
acceptance commands and [README](../README.md) for configuration.

## 5. Phase 1 — Protocol Package

Implement:

- frame header
- frame encoder
- frame decoder
- frame size validation
- message types
- protocol version
- capabilities

Tests:

- encode/decode round trip
- malformed input
- truncated frames
- oversized frames
- unknown message type
- fuzz testing

Do not implement public HTTP routing yet.

Phase 1 is implemented with explicit v1 wire constants, synchronous bounded
codecs, strict `HELLO`/`HELLO_ACK` payloads, deterministic capability negotiation,
shared Go/TypeScript fixtures, failure-oriented I/O tests, and three fuzz targets.
See [PHASE_1.md](./PHASE_1.md) for acceptance checks and [PROTOCOL.md](./PROTOCOL.md)
for the contract. Authentication and the live session handshake remain Phase 2.

## 6. Phase 2 — Agent ↔ Relay Connection

Implement:

- TCP listener/dialer
- TLS
- HELLO/HELLO_ACK
- AUTH/AUTH_OK
- connection lifecycle

Success criterion:

```text
Agent connects to local Relay and completes an authenticated handshake.
```

Phase 2 implements verified TLS 1.3, strict HELLO/AUTH sequencing, expiring scoped
credential verification, bounded listener concurrency, and cancellation/shutdown
ownership. Development setup generates private ignored fixtures; readiness uses
the real CLI handshake. See [PHASE_2.md](./PHASE_2.md) for acceptance and limits.

## 7. Phase 3 — Tunnel Registration

Implement:

- tunnel IDs
- generation numbers
- REGISTER/REGISTER_OK
- hostname assignment
- relay-side registry
- stale session replacement

Success criterion:

```text
Agent registers a tunnel and relay can resolve hostname → active session.
```

Phase 3 implements strict REGISTER messages, credential-bound tunnel identity,
relay-assigned stable hostnames, monotonic uint64 generations, atomic stale-owner
replacement, and bounded retained watermarks. CLI generation reservations persist
locally, and development readiness exercises registration. See
[PHASE_3.md](./PHASE_3.md) for acceptance, configuration, and relay-restart limits.

## 8. Phase 4 — HTTP Data Plane

Implement:

- public HTTPS listener
- hostname router
- OPEN_STREAM
- DATA
- CLOSE_STREAM
- local TCP dialing
- streaming response

Success criterion:

```bash
curl https://<tunnel-hostname>/
```

returns the localhost service response.

Phase 4 implements the HTTPS listener, validated Host/SNI routing, HTTP stream
metadata, bounded multiplexing, fixed loopback dialing, and streaming responses.
The CLI emits a public URL and forwarding readiness; the development supervisor
also verifies public TLS. Request cancellation, deadlines, replacement, and
shutdown close streams and local sockets. See [PHASE_4.md](./PHASE_4.md) for
acceptance checks and limits. Synchronous bounded delivery currently couples
slow streams on a shared connection; independent credit windows remain Phase 5.

## 9. Phase 5 — Flow Control

Implement:

- receive windows
- send windows
- WINDOW_UPDATE
- per-stream buffer limits
- connection-wide limits

Tests must demonstrate that a slow local service cannot cause unbounded memory use.

Phase 5 implements fixed stream/connection credit windows, validated binary
WINDOW_UPDATE, bounded reusable receive pages, and a joined control worker.
Tests fill the shared budget, stall one stream while another progresses, reject
invalid credit/over-window DATA, and verify reset/cancellation/cleanup credit
recovery. HTTP requires flow-control negotiation. See [PHASE_5.md](./PHASE_5.md)
for the allocation bound, acceptance results, and upgrade requirements.

## 10. Phase 6 — Heartbeat and Reconnect

Implement:

- PING/PONG
- liveness state
- exponential backoff
- jitter
- reconnect loop
- generation increment
- stale session cleanup

Test by killing the network connection while traffic is active.

Phase 6 implements strict negotiated PING/PONG, one bounded probe per endpoint,
credential-bounded liveness cleanup, and cancellable port-invocation retries.
Equal jitter and capped exponential backoff avoid tight loops; only transport
failure and missing PONG retry. Each registration uses a new persisted generation.
Tests interrupt an active mutation, verify cleanup without replay, then serve
fresh traffic at the stable URL. See [PHASE_6.md](./PHASE_6.md) for verification
and the remaining relay-selection/credential-renewal boundary.

## 11. Phase 7 — Graceful Shutdown

Implement:

- GOAWAY
- drain mode
- active stream tracking
- shutdown deadline
- cleanup

Phase 7 implements optional `graceful_shutdown`, strict GOAWAY SHUTDOWN/DRAINED,
closed stream/node admission, active HTTP and acceptance-worker tracking, and a
configurable 10s shutdown deadline. Signals drain on a separate lifetime context;
hard cancellation, expiry and transport failure still abort immediately. Relay
drain rejects new public requests with 503 and new tunnel sockets before worker
creation. Shutdown joins owned workers and preserves generation watermarks.
See [PHASE_7.md](./PHASE_7.md) for verification and compatibility boundaries.

Verify with active HTTP and duplex streams. Actual WebSocket upgrade/shutdown
coverage follows in Phase 8, when upgrades are implemented.

## 12. Phase 8 — WebSocket and Streaming

Implement:

- HTTP upgrade
- bidirectional binary/text frame handling
- SSE
- chunked request/response streaming

Tests:

- WebSocket echo
- long-lived SSE
- streaming response

Phase 8 implements negotiated, validated WebSocket version 13 upgrades over the
existing bounded DATA/credit streams. Binary/text, fragmented and control frames
remain transparent. SSE headers/events and chunked uploads/responses forward
incrementally; streaming peers use application idle lifetimes. Legacy HTTP
metadata/deadlines remain compatible. See [PHASE_8.md](./PHASE_8.md) for the
verification boundary and exclusions (compression, generic upgrades, trailers).

## 13. Phase 9 — Control Plane

Implement API service:

- auth
- projects
- tunnels
- relay registry
- credentials

Then integrate the agent bootstrap flow:

```text
CLI
 ↓
Control API
 ↓
relay assignment
 ↓
Agent → Relay
```

Phase 9 implements these endpoints with a bounded in-memory store, private
hash-only provisioning, organization/role authorization, strict bounded JSON,
caller-bound pagination and bounded idempotency. The API returns a short-lived
relay/tunnel/generation-bound credential; relay verification happens during AUTH
only. Opt-in CLI bootstrap obtains fresh credentials and higher generations on
reconnect, including lease expiry, while admitted traffic survives API outages
until expiry. Database durability and dynamic relay presence remain the next
phases. See [PHASE_9.md](./PHASE_9.md).

## 14. Phase 10 — Database

Add Prisma models from DATABASE.md.

Implement migrations and seed data.

Verify indexes and transactions with integration tests.

Phase 10 implements checked-in baseline/additive migrations, create-only hash-only
provisioning and a default PostgreSQL backend. Scoped queries and keyset indexes
avoid whole-table hydration; sessions, generations, credential policy, metadata
idempotency and audit writes are durable. Bounded interactive transactions use a
shared advisory writer lock for cross-instance admission and atomic allocation.
Integration verifies rollback, exact uint64 storage, concurrent retries, capacity,
restart persistence and database outage isolation with the actual API/CLI/relay.
Explicit memory mode remains for tests/development. Dynamic relay presence and
selection remain Phase 11. See [PHASE_10.md](./PHASE_10.md).

## 15. Phase 11 — Multi-Relay

Implement:

- relay registration
- health reporting
- capacity reporting
- relay selection
- relay draining
- failover

The control plane should stop assigning a relay when it is degraded past configured limits.

Phase 11 implements relay-key-authenticated registration, fenced 15s Redis presence,
health/capacity snapshots, durable operator drain/activation and capacity-aware
sticky assignment with alternative preference after transport failure. One bounded
reporter per node publishes local counters and observes drain commands. CLI recovery
uses another eligible node and higher generations without request replay. Real
Redis/PostgreSQL and two-relay E2E cover outages, report expiry/replacement,
concurrent capacity, graceful drain and failover. Public ingress/DNS must follow the
assigned node; application proxying across relays is not introduced. See
[PHASE_11.md](./PHASE_11.md).

## 16. Phase 12 — Domains and TLS

Implement:

- wildcard domain
- hostname generation
- custom domain registration
- domain verification
- certificate provisioning
- renewal lifecycle

For local development, support a local certificate workflow using a trusted development CA such as mkcert.

Phase 12 implements scoped, hash-only DNS TXT ownership challenges, additive
domain migration and relay-local current-generation aliases delivered through
authenticated report snapshots. Optional `custom_domains` stream metadata
preserves the application Host while retaining the registered tunnel binding.
Wildcard/custom PEM certificates reload atomically outside TLS callbacks; failed
renewals preserve the last valid cache and expired certificates reject new
handshakes. Local CA tooling issues and renews immutable bundles under a stable
root, with explicit mkcert/external-issuer deployment instructions. Real DNS,
PostgreSQL and HTTPS tests cover ownership races, outages, rotation, policy
removal and renewal without changing trust. External ACME automation and ingress
installation remain operator/future work. See [PHASE_12.md](./PHASE_12.md).

## 17. Phase 13 — Dashboard

Implement pages in this order:

```text
/login
/dashboard
/dashboard/tunnels
/dashboard/tunnels/:id
/dashboard/domains
/dashboard/relays
/dashboard/logs
/dashboard/settings
```

Dashboard should consume the same stable API contract used by CLI tooling.

Phase 13 implements all eight pages against the existing scoped control API. A
fixed-destination Next.js adapter exchanges a provisioned user key for an expiring
session in an authenticated encrypted HttpOnly cookie. Exact Origin checks,
bounded bodies/replies/concurrency, complete deadlines and cancellation protect
the browser boundary. Session tokens and relay credentials never enter browser
storage; dashboard reads never allocate generations or connect credentials.

Role-aware forms create projects/tunnels and manage one-time domain proofs,
verification, activation and disabling. Revocation requires confirmation. Lists
use signed API cursors; overview counts describe bounded metadata. Relays are
read-only; settings shows scoped membership and logout. Responsive layouts, API outage/restart recovery, CSRF, tenant/viewer
rules and session tampering are covered by real Chromium/PostgreSQL/DNS tests.
Phase 14 adds traffic metrics and recent request metadata. Durable audit browsing
remains future work.
See [PHASE_13.md](./PHASE_13.md) for verification and operating boundaries.

## 18. Phase 14 — Observability

Add:

- structured logs
- Prometheus metrics
- request counters
- latency histograms
- connection metrics
- stream metrics
- relay capacity metrics

Optional later:

- OpenTelemetry traces

Phase 14 implements local finite-label scrapes, structured metadata logs, connection
and stream lifecycles, request counters/histograms, runtime/capacity gauges, and
bounded tunnel observations in existing fenced presence reports. Scoped API reads
and the dashboard expose real observations, including unavailable/expired states.
No durable schema, application payload archive or protocol frame changes. See
[PHASE_14.md](./PHASE_14.md) for verification and operating limits.

## 19. Phase 15 — Security Hardening

Test:

- expired credentials
- revoked credentials
- malformed frames
- hostname injection
- oversized headers
- oversized bodies
- connection exhaustion
- stream exhaustion
- slow clients
- slow upstream
- host-header edge cases
- authorization boundary violations

Phase 15 implements raw HTTPS and production API adversarial suites, hardens Go
credential file checks and independently rejects unsupported parent roles during
relay AUTH. See [PHASE_15.md](./PHASE_15.md) for delivery and verification, and
[SECURITY.md](./SECURITY.md) for the threat model and twelve-item coverage matrix.
The focused `make security-integration` target also runs through `make check` and
CI; no data-plane control lookup, durable migration or protocol frame is added.

## 20. Phase 16 — Load and Chaos Tests

Simulate:

```text
relay crash
agent crash
control-plane outage
Redis outage
packet loss
latency spikes
large bodies
many concurrent streams
reconnect storms
```

The system should fail closed for security and recover for transient infrastructure problems.

Phase 16 implements bounded real-process load and chaos profiles for all nine
scenarios. Stream churn also runs under Go's race detector. Network impairment
uses an owned Docker namespace with netem or explicit packet-filter/bridge-delay
fallback. Tests verify byte integrity, reusable stream credit/admission, outage
isolation, canceled mutations without replay, fenced incarnations and simultaneous
failover with higher generations. `make load-test` and `make chaos-test` run through
`make check` and CI. Results are local correctness measurements, not a production
capacity claim. See [PHASE_16.md](./PHASE_16.md) and
[load test operations](../tests/load/README.md).

## Phase 17 — CLI Completion

Complete the developer command surface from PRD.md with private saved sessions
and settings, scoped API management, local start/stop/status, safe local service
detection, ephemeral bootstrap, diagnostics and version output. Preserve existing
protocol, forwarding, reconnect, generation and shutdown invariants. See
[CLI.md](./CLI.md) for the pre-implementation contract and
[PHASE_17.md](./PHASE_17.md) for the plan and verification. Release packaging and
production deployment follow in Phase 18.

## Phase 18 — Release and Self-Hosting

Phase 18 implements the release/distribution sections below with six-platform
versioned binaries, checksums/provenance, controlled installation and a concrete
Linux self-hosting baseline. See [RELEASE.md](./RELEASE.md),
[SELF_HOSTING.md](./SELF_HOSTING.md) and [PHASE_18.md](./PHASE_18.md).

## Phase 19 — Deployment Acceptance and Release Validation

Validate portable Go fixtures under normal/private umasks, native installers on
Linux/macOS/Windows, clean release workflow execution and isolated deployment
restart/backup restore/sustained traffic. Hosted CI must pass for the pushed
revision; real DNS/TLS, service activation, capacity and upgrade/rollback require
operator staging evidence. See [PHASE_19.md](./PHASE_19.md) and
[ACCEPTANCE.md](./ACCEPTANCE.md).

## 21. Go Quality Gates

For every networking change:

```bash
go test ./...
go test -race ./...
go vet ./...
```

Where practical, also run fuzz tests.

## 22. TypeScript Quality Gates

```bash
pnpm lint
pnpm typecheck
pnpm test
```

## 23. Integration Test Environment

Use Docker Compose to run:

```text
api
relay
postgres
redis
sample-app
agent
```

A single E2E test should exercise:

```text
sample-app
 ↑
agent
 ↑
relay
 ↑
HTTP client
```

## 24. Release Strategy

Publish signed/versioned binaries for:

```text
linux-amd64
linux-arm64
darwin-amd64
darwin-arm64
windows-amd64
windows-arm64
```

At minimum publish checksums. Prefer signed artifacts.

## 25. CLI Distribution

Support a controlled installer that:

- uses HTTPS
- pins a release version
- verifies checksum/signature
- installs with appropriate permissions

Do not execute unverified remote shell content.

## 26. Development Commands

Suggested Makefile:

```text
make dev
make test
make test-race
make lint
make typecheck
make build
make integration
make e2e
make load-test
make chaos-test
make docker-up
make docker-down
```

## 27. Definition of Done per Phase

A phase is complete only when:

1. implementation exists
2. tests exist
3. failure behavior is covered
4. logs/metrics are adequate
5. docs reflect implementation
6. CI passes
7. no known resource leak remains

## 28. Implementation Rules

- Prefer simple code over premature abstractions.
- Keep data plane and control plane separate.
- Do not put database access in the relay request path.
- Stream bytes instead of buffering whole payloads.
- Bound every queue and buffer.
- Make timeouts explicit.
- Validate all untrusted network input.
- Avoid hidden background goroutines without clear lifecycle ownership.
- Ensure every goroutine has a cancellation path.
- Never silently discard errors that impact tunnel health.
