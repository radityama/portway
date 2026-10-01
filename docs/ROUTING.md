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

Phase 2 implements verified TLS and HELLO/AUTH. Phase 3 binds the requested tunnel
ID to the verified credential, assigns a deterministic hostname, and registers
only a strictly newer generation. REGISTER/REGISTER_OK/REGISTER_ERROR schemas and
bounds are in [PROTOCOL.md](./PROTOCOL.md). Authentication alone grants no route.

`relay.Server.Lookup` resolves a canonical lowercase DNS hostname to an immutable
snapshot of the active, unexpired owner. It rejects ports, URLs and trailing dots.
Phase 4's HTTPS ingress validates Host authority, permits only its configured
numeric port, and requires matching TLS SNI before looking up that canonical
hostname. Unknown hostnames return 404; known offline or diagnostic-only owners
return 503. HTTP registration requires negotiated multiplexing and a public
listener, and its ACK includes the assigned HTTPS URL.

Phase 6 also closes the ACK publication race: a public request arriving while
an HTTP registration is pending waits outside the registry lock for at most the
write timeout, observes cancellation, then rechecks the current owner. No stream
opens before ACK success; a failed ACK leaves forwarding unavailable.

Hostnames use `p-` plus the first 128 bits of SHA-256(tunnel ID), under operator
configured PUBLIC_BASE_DOMAIN. They are stable per tunnel/base domain. Peers may
not choose a hostname; collisions fail closed. Custom domain policy is Phase 12.

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

Phase 4 preserves the method and origin-form path/query. The local Host is the
assigned canonical hostname without its public port. `X-Forwarded-Host` carries
the public authority including the port, `X-Forwarded-Proto` is `https`, and
`X-Forwarded-For` is the immediate client's IP. Incoming `Forwarded` and
`X-Forwarded-*` values are discarded before rebuilding these three headers.
Hop-by-hop headers, Connection-nominated headers, and proxy credentials are
removed in both directions. Application Authorization/Cookie headers pass to the
selected service and are never logged. Repeated response headers, including
Set-Cookie, are preserved. Redirects are returned without being followed.

The stream carries HTTP request metadata and request body bytes to a fixed
loopback TCP endpoint; public metadata never chooses that endpoint. The agent
returns a serialized HTTP/1.1 response and streams its body. FIN closes only the
sender's direction; RESET aborts both directions and closes local TCP work.
CONNECT, generic upgrades and trailers fail before forwarding. Phase 8 permits
negotiated WebSocket upgrades as specified below. HTTP/2 and configurable Host
rewriting remain later work.

Phase 4 limits request bodies to 16 MiB, responses to 64 MiB, HTTP metadata to
32 KiB and 128 header pairs, DATA to 16 KiB, and concurrent streams to 32 by
default. Legacy whole-stream deadlines default to 30 seconds; negotiated
Phase 8 streaming uses application idle timeouts instead. Public TLS/header reads
have a 5-second deadline. Stream saturation returns 503, local dial/invalid
upstream failures 502, deadlines 504, oversized requests 413, and oversized
metadata 431. SNI mismatches return 421. A body failure after response headers
have been sent aborts the response rather than replacing its status.

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

The v1 wire values, zero flags/reserved bytes, connection/stream ID rules, and
bounded capability negotiation are specified in [PROTOCOL.md](./PROTOCOL.md).
Framing rejects invalid headers before payload allocation; session-level state
transitions are enforced by the later handshake and stream implementations.

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

Phase 5 implements this model with fixed 64 KiB stream windows and a 1 MiB
connection window in each direction. DATA consumes both credits. Positive
four-byte network-order WINDOW_UPDATE increments restore stream credit for a
nonzero ID or connection credit for ID 0. Updates cannot exceed the original
window; malformed increments, overflow, or over-credit DATA close the connection.
Both peers must negotiate `flow_control` as well as `multiplexing` for HTTP.

The shared reader enqueues validated DATA and continues processing other frames.
Application consumption returns credit; FIN drains queued bytes before EOF.
Reset/disconnect release buffered bytes and restore connection credit, including
late DATA for a released ID. Sending waits observe context/deadline outside the
writer lock. A single stalled consumer cannot stop other streams while connection
credit remains. Many stalled streams can exhaust the shared budget; TCP loss and
a blocked socket writer still affect the common transport.

Receive storage coalesces tiny frames into reusable 4 KiB pages. Queued bytes are
capped at 64 KiB per stream and 1 MiB per connection; allocated pages are capped
at `256 + 2 * MaxStreams`, or 1.25 MiB at 32 streams, including page slack. One
owned control worker coalesces returned-credit counters and drains a bounded
reset/rejection queue. Stream worker admission is held until cleanup finishes.
Exact wire rules and failure handling are in [PROTOCOL.md](./PROTOCOL.md).

Response FIN precedes waiting for the request direction so early replies can
complete and cancel pending uploads. Agent cleanup waits for request FIN/reset
before releasing the response stream. After completing a public response, the
relay may drain remaining request input within its body cap for at most one
second, bounded by the stream deadline, to avoid truncating the reply with a TCP
reset. Failed/cancelled requests interrupt body reads immediately.

Phase 6 closes the public HTTP connection on upstream response failure, before
interrupting unread input. A forced read deadline can cancel the connection's
HTTP context; that connection must not carry a subsequent request after recovery.

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

Phase 8 implements version 13 upgrades with optional `websocket` and `streaming`
capabilities. Request method, key, version, body absence and Connection tokens
are validated before forwarding. A bounded 101 response must prove the key,
Upgrade and offered subprotocol before the relay hijacks its public TLS socket.
Buffered frames at either HTTP boundary are preserved. Extension offers are
removed; WebSocket compression is not negotiated. Failed upgrades remain HTTP.

Existing DATA and credit windows forward masked, binary/text, fragmented,
ping/pong and close frames transparently. Application endpoints own RFC6455
frame semantics; Portway does not assemble complete messages. Both pumps use
16 KiB buffers and join on cancellation/EOF. Upgrades retain a stream and public
socket admission slot until cleanup, participate in graceful drain and close
at the forced deadline. Supersession, credential expiry and transport loss abort
without replay. See [PROTOCOL.md](./PROTOCOL.md) and [PHASE_8.md](./PHASE_8.md).

## 17. SSE and Streaming

Server-Sent Events and streaming APIs must flush data promptly.

Do not wait for an entire response before forwarding it.

Phase 8 flushes response headers before waiting for the first body bytes, then
flushes each read chunk. Chunked uploads reach the local service before EOF;
chunked responses reach clients before completion. The existing 16 MiB request
and 64 MiB response limits remain, including SSE. Trailers are unsupported.

Negotiated `streaming` refreshes an application idle deadline on DATA progress,
including reads/writes; heartbeats and credit updates cannot extend it. The
RELAY_STREAM_TIMEOUT and PORTWAY_STREAM_TIMEOUT defaults are 30s (positive, up
to 5m). Streaming sockets refresh operation deadlines, and a completed public
upload clears its read deadline so HTTP disconnect monitoring cannot cancel an
active SSE response. Parent cancellation, credential expiry and shutdown still
bound every lifetime. Old peers retain whole-request deadlines. Applications
should emit events or WebSocket ping/pong within their configured idle interval.

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

Phase 6 implements negotiated PING/PONG on registered HTTP and diagnostic
sessions. Each endpoint tracks one outstanding probe and the last matching PONG.
It probes every 15 seconds and allows 45 seconds for a reply; traffic cannot
extend that deadline. Timeout closes the socket, cancels streams/local TCP work,
joins workers and removes only that owner's route. Strict nonce/timestamp JSON,
compatibility and queue bounds are specified in [PROTOCOL.md](./PROTOCOL.md).

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

Phase 6 port invocations reconnect to the configured relay with equal jitter
between half and all of each exponential delay. The maximum base is 30s; 60s of
healthy forwarding resets it. Cancellation interrupts backoff and network I/O.
Every fresh registration reserves a higher local generation; the explicit
recovery override is consumed once. Transport failure and heartbeat timeout are
retryable, while authentication, TLS verification, protocol and registration
errors stop. Diagnostic commands retain their single-session behavior. New
readiness events describe the recovered session; failed requests are not replayed.

### Phase 7 draining

Phase 7 adds negotiated GOAWAY SHUTDOWN/DRAINED. Receipt closes new stream
admission; existing DATA, FIN, RESET, credit updates and heartbeat remain valid.
Both sides finish stream and worker cleanup and exchange DRAINED before closing
the transport, preserving queued response bytes. OPEN racing admission receives
STREAM_DRAINING; new public requests return 503 without opening a stream.

Relay shutdown closes node admission before starting per-session drains.
Pending handshakes close promptly, registrations cannot advance ownership while
draining, and deadline expiry closes raw tunnel and public HTTPS sockets to
interrupt slow readers, uploads and blocked writes. Admitted HTTP handlers stay
counted until their owned cleanup has joined. The default deadline is 10s, with
positive overrides up to 1m. Credential expiry and hard cancellation remain
earlier bounds. Public listeners may remain bound to return 503 during draining;
the binary closes them after the drain or at its deadline.

Port invocations finish the current drained session before scheduling recovery
with `relay_draining`; REGISTER_DRAINING is retryable too. Other registration
errors stay terminal. Diagnostics drain once and exit. Generation replacement
and owner-aware unregister remain unchanged. Phase 8 verifies these same drain
semantics on actual upgraded WebSocket connections, including blocked public writes.
See [PHASE_7.md](./PHASE_7.md) and [PROTOCOL.md](./PROTOCOL.md).

Local HTTP requests use one owned socket per stream. The agent closes it during
cleanup but does not request an immediate upstream close with unread upload
bytes; this lets early responses finish before upload cancellation can cause a
TCP reset. Stream and shutdown deadlines still bound socket lifetime.

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

Equal generations also fail. Cleanup checks the exact owner entry; a replaced
session's late cleanup cannot remove its successor. Disconnected owners retain
their highest generation and hostname allocation until relay restart. Retained
entries are capped by RELAY_MAX_TUNNELS; capacity rejects new IDs rather than
evicting stale-session protection. Higher generations of known IDs remain allowed.
Failed ACK writes remove active routing but retain the accepted generation.

The CLI reserves generations before sending REGISTER, with private per-tunnel
files and an exclusive lock. Concurrent starts fail without a retry loop. Failed
attempts may leave gaps. Phase 6 automatic reconnect reserves a fresh generation
for each new registration. Multi-relay coordination remains later work; local
counter loss needs an operator-selected higher generation. Run one agent per
tunnel: machines sharing its identity must coordinate generation ownership.

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
