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
creates no background goroutines. Live authenticated TLS begins in Phase 2.

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
