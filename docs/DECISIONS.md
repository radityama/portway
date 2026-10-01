# Architecture Decision Records

## ADR-006: Portway Project Name

Portway is the product name: a memorable path from a local port to the web. The
CLI is `portway`, packages use `@portway/*`, and CLI environment variables use
`PORTWAY_*`. Tunnel remains the technical domain term in APIs and persistence.

## ADR-007: Phase 2 Credential Verifier and TLS

Agent-to-relay connections use verified TLS 1.3 with ALPN `portway/1`. A verifier
interface authenticates expiring, scoped credentials without coupling the data
plane to the future control API. The Phase 2 file implementation stores SHA-256
hashes of random credentials using the existing TunnelCredential field meanings;
it adds no durable model. Revocation/expiry block new handshakes; credential
expiry also ends an active connection. Live control-plane revocation propagation
arrives in Phase 9. Development tooling generates a local CA, relay certificate,
and one 24-hour credential under ignored `.tmp/dev`; it never prints a secret.

## ADR-008: Relay-Local Registration Ownership

The relay binds REGISTER to the verified credential's tunnel ID. Canonical uint64
generation strings preserve precision in Go/TypeScript. Higher generations replace
older owners atomically; equal/lower generations fail even after disconnect. Retained
watermarks have a hard capacity and no eviction until relay restart, avoiding stale
reuse after tombstone expiry. Hostnames derive from the tunnel ID under an operator
base domain; collisions fail closed. This is local ephemeral state, with no new
durable entity or hot-path API/database dependency. Client counters persist in
private local files; cross-relay ownership and automatic reconnect are later work.

## ADR-009: Initial HTTP Stream Transport

HTTP registrations negotiate multiplexing and receive a relay-assigned HTTPS
URL. Public Host/SNI validation selects a local active owner; stream metadata
cannot select an upstream address. The agent only dials its configured loopback
port. Frames, headers, bodies, stream counts, sockets, and deadlines are bounded.
Synchronous pipe delivery uses no growing receive queue but lets a slow stream
block the shared reader; independent credit windows are Phase 5. FIN preserves
half-close and RESET cancels both directions, without transparent replay.
Development public TLS uses a separate local CA/certificate set so upgrading
from Phase 3 preserves existing agent credentials. No dependency or durable
schema change is needed.

## ADR-010: Fixed Negotiated Byte-Credit Windows

The `flow_control` capability enables fixed 64 KiB stream and 1 MiB connection
windows in each direction. A four-byte unsigned network-order WINDOW_UPDATE
payload defines the previously reserved frame; ID 0 addresses connection credit.
Fixed limits avoid a second configurable wire negotiation. HTTP requires both
stream capabilities, so agents and relays must upgrade together; diagnostic
registration remains compatible with peers that do not offer flow control.

Receive DATA copies into pooled 4 KiB pages and does not wait for an application
reader. Queue byte limits and an allocation cap including per-stream page slack
bound memory even for tiny frames. One owned worker coalesces credit counters and
drains bounded reset/rejection controls. Reads return credit; reset discards return
connection credit; already-sent bytes are never refunded by the sender. Admission
counts acceptance workers until cleanup completes. Shared budget saturation and
TCP/socket backpressure still apply. No dependency or durable model is added.

## ADR-011: Negotiated Liveness and Explicit CLI Recovery

Registered sessions negotiate `heartbeat` with strict JSON PING/PONG, a random
64-bit nonce and an echoed UTC timestamp. Local monotonic deadlines, one
outstanding probe and one owned worker per connection bound liveness work.
The existing reader and bounded control writer handle replies independent of
DATA credit; old peers keep idle deadlines without heartbeat frames.

Port invocations own a visible reconnect loop with equal jitter, 1s–30s base
delays and a reset only after 60s healthy. Transport failures retry; security,
protocol, local-state and registration failures stop. Each new registration
reserves a higher persisted generation; no HTTP request migrates or replays.
Credential expiry remains terminal. Multi-relay selection, credential renewal
and graceful draining retain their later-phase boundaries.

A bounded registration-completion barrier lets a request arriving immediately
after the agent receives its ACK wait for relay routing publication. It holds
no registry lock during I/O and never forwards before ACK success.

## ADR-012: Negotiated Drain Completion and Separate Signal Lifetimes

Registered peers optionally negotiate `graceful_shutdown`. GOAWAY SHUTDOWN closes
admission; GOAWAY DRAINED proves stream and acceptance-worker cleanup completed.
An empty local map does not prove that a peer consumed buffered responses, so
graceful close requires completion in both directions. Both messages are strict,
bounded and limited to one each per direction, with existing frame-reader and
writer ownership. Admitted OPEN writes precede SHUTDOWN.

Signals request a 10s default drain on a separate lifetime context. Hard
cancellation, expiry, replacement and transport failures still abort. The relay
counts HTTP handlers through cleanup and closes raw public/tunnel sockets at the
deadline, so a client blocked on response reading cannot delay shutdown until a
longer HTTP write timeout. Legacy peers receive no unsupported frames; accepting
endpoints wait for peer close/deadline without claiming completion proof.
Remote drain remains visible and retryable in the CLI's existing backoff loop.
No request replay, database call, durable entity or dependency is introduced.

## ADR-001: Go for Agent and Relay

Go is the baseline implementation language for the data plane because it provides a strong networking standard library, straightforward concurrency, and easy cross-platform distribution.

## ADR-002: Control/Data Plane Separation

The API/database layer manages identity and metadata. Public application bytes flow through relays to agents and do not synchronously depend on the control plane.

## ADR-003: TCP/TLS Before QUIC

The MVP uses TLS over TCP for simplicity and debuggability. Transport is abstracted so QUIC can be added later without redesigning protocol semantics.

## ADR-004: Bounded Multiplexed Streams

One persistent agent↔relay connection carries multiple streams, with explicit stream limits, frame limits, buffer limits, and cancellation.

## ADR-005: No Transparent Replay on Reconnect

Requests that are interrupted by a broken tunnel connection fail rather than being replayed automatically. Transparent replay requires an explicit idempotency and request-body policy that is outside the MVP.
