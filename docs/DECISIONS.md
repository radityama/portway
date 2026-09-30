# Architecture Decision Records

## ADR-006: Portway Project Name

Portway is the product name: a memorable path from a local port to the web. The
CLI is `portway`, packages use `@portway/*`, and CLI environment variables use
`PORTWAY_*`. Tunnel remains the technical domain term in APIs and persistence.

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
