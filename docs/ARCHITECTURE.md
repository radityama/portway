# Portway — Architecture

## 1. System Context

The platform contains four primary subsystems:

```text
                         INTERNET
                            │
                            ▼
                    ┌───────────────┐
                    │ Load Balancer │
                    └───────┬───────┘
                            │
                 ┌──────────┼──────────┐
                 ▼          ▼          ▼
             Relay JKT   Relay SG   Relay JP
                 │          │          │
                 └──────────┼──────────┘
                            │
                     Control Plane
                       │       │
                  PostgreSQL   Redis
                       │
                    Dashboard

LOCAL MACHINE

┌───────────────────────────────────────┐
│               Agent                   │
│ protocol / streams / reconnect / auth│
└──────────────────┬────────────────────┘
                   │ outbound TLS/QUIC
                   ▼
                Relay
                   │
                   ▼
             localhost:3000
```

## 2. Control Plane vs Data Plane

### Control plane

Owns metadata and orchestration:

- identity
- projects
- tunnels
- credentials
- relay registry
- domain verification
- access policies
- quotas
- audit events

Typical technologies:

- TypeScript
- Next.js/API
- PostgreSQL
- Prisma
- Redis

### Data plane

Moves application bytes:

- relay
- agent
- transport
- framing
- multiplexed streams

Data-plane traffic MUST NOT synchronously depend on PostgreSQL.

## 3. Agent Architecture

```text
CLI
 │
 ├── Config
 ├── Dev Server Manager
 ├── Health Checker
 ├── Auth Client
 └── Tunnel Client
          │
          ├── Transport
          ├── Protocol
          ├── Multiplexer
          ├── Stream Manager
          ├── Heartbeat
          └── Reconnect Controller
```

The agent should be a single Go binary.

## 4. Relay Architecture

```text
Public TLS Listener
       │
       ▼
HTTP Request Parser
       │
       ▼
Hostname Router
       │
       ▼
Tunnel Registry
       │
       ▼
Stream Allocator
       │
       ▼
Multiplexed Tunnel Connection
```

Supporting components:

```text
Relay
├── ingress
├── tls
├── router
├── registry
├── session
├── stream
├── limits
├── health
├── metrics
└── drain
```

## 5. Communication Paths

### Control path

```text
Agent → HTTPS → API → PostgreSQL/Redis
```

### Tunnel registration path

```text
Agent → HTTPS/TLS → Relay
```

### Public application path

```text
Browser → Relay → Agent → localhost service
```

The public application path should avoid the API and database.

## 6. Connection Lifecycle

```text
CREATED
  ↓
CONNECTING
  ↓
AUTHENTICATING
  ↓
AUTHENTICATED
  ↓
REGISTERING
  ↓
CONNECTED
  ↓
DRAINING
  ↓
DISCONNECTED
  ↓
RECONNECTING
```

`REVOKED` is terminal until a new tunnel is created or explicitly re-enabled by policy.

## 7. Transport Abstraction

MVP transport:

```text
TCP + TLS
```

Future transport:

```text
QUIC
```

The protocol must depend on a generic transport abstraction rather than concrete TCP APIs.

Conceptual interface:

```go
type Transport interface {
    Connect(ctx context.Context) error
    OpenStream(ctx context.Context) (Stream, error)
    AcceptStream(ctx context.Context) (Stream, error)
    Close() error
}
```

## 8. Protocol Stack

```text
Application semantics
       ↓
Tunnel protocol
       ↓
Multiplexing
       ↓
Transport
       ↓
TLS / network
```

The protocol defines framing, stream IDs, flow control, lifecycle messages, capabilities, and versioning.

Phase 1 provides synchronous, bounded framing and capability negotiation as
specified in [PROTOCOL.md](./PROTOCOL.md). Transport/session callers own socket
deadlines, cancellation, serialized writes, and connection closure; the codec
creates no background goroutines. Phase 2 adds verified TLS and scoped credential
authentication. Its relay bounds accepted connections before starting goroutines;
socket deadlines cover TLS/HELLO/AUTH, idle reads, writes, and credential expiry.
Context cancellation closes the listener/connections and joins their workers.
Phase 3 grants routing ownership only after credential-bound registration.
Relay-local generation watermarks are bounded and retained across disconnects;
replacement closes the old socket, and old cleanup cannot remove the new route.
Lookup returns immutable active-owner metadata with no API/database call. Agent
generation reservations persist locally; relay restart loses ephemeral ownership.

Phase 4 attaches a bounded stream multiplexer to HTTP registrations and serves
public HTTPS through the local owner registry. Host and TLS SNI must agree.
Each request carries validated HTTP metadata and body frames to the agent's
fixed loopback TCP destination; application responses stream back without an
API/database call. Cancellation, expiry, replacement, and shutdown close the
parent connection's streams and local sockets. Per-stream deadlines and bounded
synchronous pipes prevent growing receive queues. A blocked consumer can stall
other streams on that connection until it reads or is cancelled; independent
receive/send credit windows are implemented in Phase 5.

Phase 5 replaces synchronous DATA pipes with independently queued stream bytes.
Fixed 64 KiB stream and 1 MiB connection windows gate sends before taking the
shared writer lock. Reads return credit through validated binary WINDOW_UPDATE;
reset/disconnect discard queues and return connection credit. Reusable 4 KiB
pages cap allocation, including partial-page slack, and one owned control worker
coalesces updates without an unbounded queue. Stream admission counts workers
until cleanup finishes. HTTP requires both multiplexing and flow-control
capabilities. No control-plane, database, or durable-state dependency is added.

Phase 6 adds one owned heartbeat worker per negotiated registered connection.
The existing frame reader validates probes and matching replies; the bounded
control writer sends PONG even when DATA credit is exhausted. Missing PONG,
credential expiry and cancellation close and join the session. Port invocations
recover transport failures with cancellable exponential backoff and equal jitter,
authenticate again, and persist a fresh generation before registration. Terminal
security/protocol/registration errors stop; requests never migrate or replay.

## 9. Multiplexing

One agent-to-relay connection should support many concurrent logical streams:

```text
                 Agent
                   │
            persistent conn
                   │
       ┌───────────┼───────────┐
       ▼           ▼           ▼
    Stream 1    Stream 2    Stream 3
```

This minimizes socket overhead and allows a tunnel to handle concurrent browser requests and WebSockets.

## 10. Flow Control and Backpressure

The implementation MUST bound:

- per-stream buffers
- per-connection buffers
- active stream count
- request body size
- response buffering
- idle streams

Do not create any queue that can grow without a hard or policy-derived limit.

## 11. Stream Model

Each incoming public request creates a logical stream.

Conceptual state:

```text
NEW
 ↓
OPEN
 ↓
HALF_CLOSED_LOCAL / HALF_CLOSED_REMOTE
 ↓
CLOSED
```

A stream reset must be explicit and safely propagate to both sides.

## 12. Tunnel Identity

Each persistent tunnel has:

- `tunnel_id`
- `connection_id`
- `generation`
- assigned relay
- hostname

Generation numbers protect against stale sessions:

```text
connection A → generation 10
connection B → generation 11
```

Once B registers successfully, A must no longer be eligible for routing.

## 13. Relay State

Each relay maintains local ephemeral state:

```text
hostname → tunnel ID → active connection
```

The control plane remains the durable source of metadata.

## 14. Routing Cache

Recommended hierarchy:

```text
L1 in-memory cache
        ↓ miss
L2 Redis
        ↓ miss
L3 control plane lookup
```

Never query PostgreSQL directly from every HTTP request.

## 15. Multi-Relay

Production deployments may run:

```text
Relay JKT
Relay SG
Relay JP
```

A relay reports:

- health
- capacity
- active tunnels
- active streams
- bandwidth
- connection errors
- latency

Status:

```text
HEALTHY
DEGRADED
DRAINING
OFFLINE
```

## 16. Relay Assignment

The control plane selects a relay based on:

- health
- capacity
- supported protocol
- region/latency preference
- account policy

Do not select solely from geography.

## 17. Relay Draining

When a relay enters `DRAINING`:

- no new tunnels should be assigned
- existing sessions remain active
- new tunnel creation is redirected to another relay
- deployment waits for connection count to reach zero where practical

## 18. Control Plane Failure Isolation

Once an agent is authenticated and connected to a relay, existing data-plane forwarding should continue during a temporary API/database outage.

The relay needs enough local state to continue active sessions.

## 19. Graceful Shutdown

Agent shutdown:

1. stop accepting new streams
2. send `GOAWAY`
3. wait for active streams
4. close local sockets
5. unregister tunnel
6. close transport
7. exit after deadline

Relay shutdown follows the same draining principle at the node level.

Phase 7 negotiates `graceful_shutdown` on registered HTTP and diagnostic sessions.
Each direction sends GOAWAY SHUTDOWN after any admitted OPEN is written, then
GOAWAY DRAINED after its streams and acceptance workers finish. Both DRAINED
messages are required before graceful transport closure; a local empty stream
map alone cannot prove a peer consumed its buffered response. Flow control and
heartbeat continue during drain. Repeated calls cannot extend the deadline.

CLI/relay signal contexts initiate draining separately from hard transport
lifetimes. The relay closes stream, registration and tunnel-socket admission,
returns 503 for new public HTTP requests, and waits for admitted handlers through
upload/response cleanup. At the deadline it closes raw tunnel and public sockets,
including blocked uploads/writes, before joining workers. Credential expiry,
protocol failures, supersession and explicit lifetime cancellation still abort.
Owner-aware unregister retains generation watermarks. Legacy peers receive no
new messages. No database, API or durable model participates in shutdown.
See [PHASE_7.md](./PHASE_7.md) for the compatibility and verification boundary.

Phase 8 negotiates `streaming` and `websocket` for HTTP registrations. A validated
WebSocket marker changes only the upgrade stream's handshake; DATA/credit and
FIN/RESET preserve duplex frames without complete-message buffering. Both HTTP
boundaries retain pipelined frames. SSE headers and chunks flush promptly.
Streaming sessions replace the whole-request timeout with application idle
supervision by one owned mux worker. Application progress refreshes the timer;
heartbeat/credit messages do not. Socket deadlines and cancellation interrupt
blocked I/O. Upgraded public sockets remain tracked by listener admission and
request-handler ownership through graceful or forced shutdown. Credential,
generation, body/header and memory bounds remain in force; upgraded frames use
the stream/credit bounds rather than ordinary HTTP body byte caps. No dependency,
REST or durable model is introduced. See [PHASE_8.md](./PHASE_8.md).

## 20. Timeouts

Every network operation needs explicit timeout behavior, including:

- TCP dial
- TLS handshake
- HTTP header read
- request body idle
- response body idle
- heartbeat
- stream idle
- shutdown

## 21. Resource Isolation

A single tunnel must not be able to consume unbounded relay resources.

Apply quotas at:

- account
- tunnel
- connection
- stream
- IP
- relay

## 22. Observability

All services should emit structured logs and metrics.

Core metrics:

```text
tunnel_connections_total
tunnel_connections_active
tunnel_streams_total
tunnel_streams_active
tunnel_bytes_rx_total
tunnel_bytes_tx_total
tunnel_errors_total
relay_connections_active
relay_cpu_usage
relay_memory_usage
request_duration_seconds
```

OpenTelemetry may be introduced after basic metrics/logging are stable.

## 23. Deployment Topology

### Local development

```text
API
Dashboard
Relay
PostgreSQL
Redis
```

### Production

```text
Internet
  ↓
Load Balancer
  ↓
Relay cluster
  ↘
   Control Plane
    ↘
     PostgreSQL + Redis
```

## 24. Architecture Invariants

1. Public traffic does not depend on a DB query.
2. Agents make outbound connections.
3. Tunnel protocol is versioned.
4. Network input is validated.
5. Buffers are bounded.
6. Secrets are never logged.
7. Existing data-plane traffic can survive control-plane outages.
8. Stale sessions cannot regain ownership of a tunnel.

## 25. Phase 9 control-plane boundary

The API loads a private hash-only seed into bounded process-local maps. It
authenticates provisioned organization API keys and expiring sessions, authorizes
project/tunnel operations by membership, and atomically allocates connection
credentials and uint64 generations. Relay assignment uses configured HEALTHY
metadata, not live health reports. Assignment is CONNECTING; live presence is
later work. No API mutation writes the seed. Restart loses mutations and issued
credentials; Phase 10 introduces database durability.

An opted-in CLI reserves its local generation minimum, calls the API before
connecting, validates the returned lease/relay/hostname and uses verified relay
TLS. A separately authenticated relay sends only the credential hash to the API
during AUTH, receives a tunnel/generation/expiry lease, and verifies registration
against it. Public HTTP/WebSocket/SSE continue using the existing local registry
and mux. API outages leave active sessions usable until their bounded lease
expires. Revocation applies to future authentication; there is no active push
revocation yet. Lease refresh uses a new session, cancels interrupted streams, and
never replays application requests. Persisted client minima prevent generation
rollback after an API restart with the same seed; lost client state still requires
the existing explicit recovery override. See API.md and PHASE_9.md.

## 26. Phase 10 persistence boundary

The default API uses PostgreSQL through Prisma with scoped indexed queries and
bounded interactive transactions. API sessions, credential parent/relay/generation
bindings, exact uint64 counters, resource policy, idempotency and audits survive
restart. A shared transaction-scoped advisory lock initially serializes writers
across API instances; mutation, admission and audit commit together or roll back.
Provisioning is explicit and create-only; API startup never imports or migrates.
No database fallback admits policy after a failed commit. Cursor signing and rate
buckets remain local to each API process.

Relay verification queries durable policy during AUTH only. Admitted sessions
keep their bounded lease and local registry/mux while the API or database is
unavailable. Expired sessions must bootstrap again; revoked policy blocks future
admission across API restarts. Public HTTP/WebSocket/SSE have no database/API query.
Durable issuance remains CONNECTING metadata; live relay reporting, selection,
drain coordination and failover are Phase 11. See PHASE_10.md and DATABASE.md.
