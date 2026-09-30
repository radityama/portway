# Portway — Routing and Data-Plane Specification

## 1. Purpose

This document defines how public requests are mapped to an active local tunnel and how streams move between relay and agent.

## 2. Canonical Path

```text
Client
  │
  │ HTTPS
  ▼
Relay
  │
  ├── TLS termination
  ├── host validation
  ├── routing lookup
  ├── access policy
  ├── rate limiting
  └── stream allocation
  │
  │ multiplexed tunnel stream
  ▼
Agent
  │
  │ localhost TCP
  ▼
Local Service
```

## 3. Hostname Routing

Example public host:

```text
abc123.portway.example.com
```

The relay extracts the hostname and resolves:

```text
hostname
  ↓
tunnel ID
  ↓
connection ID
  ↓
active generation
  ↓
stream
```

The public hostname is not an authentication mechanism.

## 4. Routing Registry

The relay maintains an in-memory registry:

```text
map[hostname]*TunnelSession
```

A session includes at least:

```text
tunnelId
connectionId
generation
relayId
state
lastHeartbeat
streamCount
```

## 5. Cache Hierarchy

```text
L1: relay memory
 ↓ miss
L2: Redis ephemeral mapping
 ↓ miss
L3: control-plane lookup
```

Lookup results should be cached for a short configurable TTL.

Cache entries must be invalidated when a tunnel changes ownership or becomes unavailable.

## 6. Tunnel Registration

Registration sequence:

```text
Agent
  │
  │ HELLO
  ▼
Relay
  │
  │ HELLO_ACK
  ▼
Agent
  │
  │ AUTH
  ▼
Relay
  │
  │ AUTH_OK
  ▼
Agent
  │
  │ REGISTER(tunnelId, generation)
  ▼
Relay
  │
  │ REGISTER_OK(publicHostname)
  ▼
Agent
```

If a tunnel has a newer generation already registered, the relay must reject or close the stale session.

## 7. Public Request Routing

For:

```http
GET /api/users
Host: abc123.portway.example.com
```

the relay MUST:

1. parse the request
2. validate the host
3. check domain/tunnel mapping
4. confirm active session
5. apply request limits/policies
6. allocate a stream ID
7. send `OPEN_STREAM`
8. stream request bytes
9. stream response bytes
10. close/reset the stream correctly

## 8. Stream Mapping

Example:

```text
Public request
stream 42
       │
       ▼
Relay
       │
       │ OPEN_STREAM(42)
       ▼
Agent
       │
       │ dial localhost:3000
       ▼
Local service
```

The relay should not need to understand the internal TCP socket identity after the stream is allocated.

## 9. Multiplexing

One tunnel transport carries multiple streams:

```text
Connection 7
├── Stream 41 → GET /
├── Stream 42 → GET /api/users
├── Stream 43 → WebSocket
└── Stream 44 → POST /upload
```

Stream IDs are 64-bit unsigned integers and must be unique within a connection lifetime.

## 10. Frame Types

Minimum protocol frames:

```text
HELLO
HELLO_ACK
AUTH
AUTH_OK
AUTH_ERROR
REGISTER
REGISTER_OK
REGISTER_ERROR
PING
PONG
OPEN_STREAM
OPEN_STREAM_OK
OPEN_STREAM_ERROR
DATA
WINDOW_UPDATE
CLOSE_STREAM
RESET_STREAM
GOAWAY
```

## 11. Frame Validation

Every frame must validate:

- protocol version
- type
- payload length
- stream ID semantics
- state transitions
- maximum frame size

Invalid frames must result in a controlled protocol error or connection termination.

## 12. Flow Control

Every stream needs bounded flow control.

Conceptual model:

```text
sender has window N
        ↓
sends DATA
        ↓
window decreases
        ↓
receiver consumes data
        ↓
WINDOW_UPDATE
        ↓
window increases
```

A sender must stop when its send window reaches zero.

## 13. Backpressure

Backpressure must propagate:

```text
slow localhost service
        ↓
Agent write stalls
        ↓
stream window stops advancing
        ↓
Relay stops sending DATA
        ↓
Client naturally experiences slower throughput
```

Do not continuously buffer bytes in memory while the local destination is blocked.

## 14. HTTP Semantics

Preserve:

- method
- path
- query string
- request headers where safe
- status code
- response headers where safe
- body streaming

Strip or manage hop-by-hop headers appropriately.

## 15. Host Header Policy

Support policies such as:

```text
preserve
rewrite
custom
```

Example:

```bash
portway 3000 --host-header localhost
```

The forwarded Host header must be deterministic and documented.

## 16. WebSocket Routing

For WebSocket requests:

```text
Client
 ↓ Upgrade
Relay
 ↓ stream
Agent
 ↓ Upgrade
Local service
```

The implementation must preserve bidirectional semantics and avoid buffering one side indefinitely.

## 17. SSE and Streaming

Server-Sent Events and streaming APIs must flush data promptly.

Do not wait for an entire response before forwarding it.

This is particularly important for AI/LLM applications that emit incremental tokens.

## 18. Large Requests

Request bodies must be streamed.

Configurable limits should include:

```text
max_request_body
max_stream_duration
max_stream_buffer
max_concurrent_streams
```

The relay should reject oversize requests before excessive buffering occurs where size information is available.

## 19. Timeouts

Recommended defaults are policy values, not protocol guarantees:

```text
connect timeout      10s
heartbeat interval   15s
heartbeat deadline   45s
idle stream timeout  120s
shutdown deadline    10s
```

WebSocket and long-lived streaming tunnels may require explicit overrides.

## 20. Heartbeat

```text
PING(nonce, timestamp)
        ↓
PONG(nonce, timestamp)
```

A missed heartbeat should move the session into a degraded/disconnected state rather than immediately destroying every local resource without cleanup.

## 21. Reconnect

Backoff:

```text
1s
2s
4s
8s
16s
30s
30s...
```

Add random jitter to avoid synchronized reconnect storms.

After reconnect:

```text
connect
 ↓
authenticate
 ↓
register tunnel
 ↓
replace stale session
 ↓
healthy
```

Existing streams should normally fail rather than being replayed automatically.

## 22. Stale Session Protection

Use generation numbers:

```text
old session: generation 10
new session: generation 11
```

Once 11 is accepted:

- routing points to 11
- session 10 is closed
- session 10 cannot overwrite mapping back to itself

## 23. Relay Failure

Expected sequence:

```text
Agent
  X Relay A
  ↓
control-plane relay selection
  ↓
Relay B
  ↓
re-register tunnel
```

Existing streams may receive 502/504-like failures depending on where the connection failed.

## 24. Control Plane Failure

Existing relay sessions SHOULD continue serving traffic during a temporary control-plane outage.

New relay selection, new credentials, or new tunnel creation may fail until the control plane returns.

## 25. Access Policy

Access control occurs before stream creation whenever practical.

Possible policies:

```text
none
basic_auth
bearer_token
oidc
ip_allowlist
```

## 26. Rate Limiting

Apply limits at multiple dimensions:

```text
IP
account
project
tunnel
relay
```

Avoid coupling every packet to a remote Redis call. Use local token buckets with periodic/shared enforcement where practical.

## 27. Error Mapping

Examples:

```text
no route             → 404
route exists but down→ 502/503
upstream timeout     → 504
rate limited         → 429
request too large    → 413
access denied        → 401/403
```

Exact codes should remain stable and documented.

## 28. Security-Sensitive Headers

Do not blindly proxy:

```text
Authorization
Cookie
Proxy-Authorization
```

These may be forwarded to the local application when user intent requires it, but must never be logged by default.

## 29. Public TCP Routing — Future

Future TCP tunneling may use:

```text
public TCP listener
 ↓
tunnel ID
 ↓
stream
 ↓
localhost TCP service
```

TCP routing should be implemented as a separate protocol surface rather than weakening the HTTP router.

## 30. Routing Invariants

1. Every public request maps to one known tunnel.
2. A stale session cannot regain routing ownership.
3. No public request chooses an arbitrary destination.
4. Stream buffers are bounded.
5. Streaming is end-to-end.
6. WebSocket upgrades preserve bidirectional behavior.
7. Control-plane outages do not necessarily terminate active sessions.
