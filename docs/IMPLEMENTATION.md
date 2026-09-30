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

## 9. Phase 5 — Flow Control

Implement:

- receive windows
- send windows
- WINDOW_UPDATE
- per-stream buffer limits
- connection-wide limits

Tests must demonstrate that a slow local service cannot cause unbounded memory use.

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

## 11. Phase 7 — Graceful Shutdown

Implement:

- GOAWAY
- drain mode
- active stream tracking
- shutdown deadline
- cleanup

Verify with active HTTP and WebSocket connections.

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

## 14. Phase 10 — Database

Add Prisma models from DATABASE.md.

Implement migrations and seed data.

Verify indexes and transactions with integration tests.

## 15. Phase 11 — Multi-Relay

Implement:

- relay registration
- health reporting
- capacity reporting
- relay selection
- relay draining
- failover

The control plane should stop assigning a relay when it is degraded past configured limits.

## 16. Phase 12 — Domains and TLS

Implement:

- wildcard domain
- hostname generation
- custom domain registration
- domain verification
- certificate provisioning
- renewal lifecycle

For local development, support a local certificate workflow using a trusted development CA such as mkcert.

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
