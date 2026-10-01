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
