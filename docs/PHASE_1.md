# Phase 1 — Protocol Package

## Boundary and plan

Implement the data-plane wire contract from [PROTOCOL.md](./PROTOCOL.md), within
the architecture/routing invariants. Correct the starter's frame IDs and partial
writes, validate headers before allocation, define bounded version/capability
negotiation, verify shared wire fixtures, then run failure, race, and fuzz checks.

The synchronous package owns no connections, goroutines, or timers. Callers
serialize writes, set deadlines, and close connections on cancellation or codec
errors. This phase adds no API/database access to the data plane and changes no
durable schema or REST behavior.

## Delivery

- Explicit message IDs match the documented gap between `PONG` (`0x0A`) and
  `OPEN_STREAM` (`0x10`). All 18 types have byte-level compatibility fixtures.
- The 16-byte network-order header validates version, type, flags, reserved byte,
  stream ID, and payload length before allocating or reading the payload.
- Connection messages require stream ID zero; stream messages require a nonzero
  unsigned 64-bit ID. All v1 flags are reserved and rejected when nonzero.
- Payload limits can be configured from one byte through 4 MiB. Handshake payloads
  have an additional 4096-byte cap. Invalid limits perform no I/O.
- The encoder finishes partial writes and rejects zero-progress/invalid writers.
  Read/write errors remain inspectable with `errors.Is`/`errors.As`.
- Strict `HELLO`/`HELLO_ACK` JSON codecs reject missing/unknown/duplicate fields,
  null arrays, trailing JSON, incorrect types, and invalid values without exposing
  raw payloads in errors.
- Negotiation uses v1, sorted shared capabilities, requirements from both peers,
  and the smaller payload limit. ACK validation prevents unoffered capability or
  limit selection. Defined capabilities are contracts, not enabled features.
- TypeScript exports the same protocol constants and handshake interfaces, with
  tests against the shared fixtures and documentation. Stream IDs use `bigint`
  in the fixture reader to preserve the full 64-bit range.
- The relay skeleton uses its configured `MaxFrame` for decoding. Its live
  authenticated handshake is Phase 2 work. A pipe test verifies that its limit
  rejects oversized frames before accepting the body.
- Three fuzz targets cover arbitrary wire decoding, encoder round trips, and
  handshake payloads. CI runs bounded active fuzzing in addition to seed tests.

## Acceptance commands

```bash
make check
make fuzz
go test ./internal/protocol -cover
pnpm test:bootstrap
```

`make fuzz` runs each target for ten seconds with two workers. Increase the
duration with `make fuzz FUZZTIME=30s`. Failed fuzz inputs are retained by Go as
regression corpus entries. The fixtures are in `tests/fixtures/protocol-v1.json`.

Failure tests cover every truncated frame boundary, all 256 possible type bytes,
oversized headers without body reads, configured-limit boundaries, partial and
broken writers, invalid negotiation from either peer, caller cancellation,
blocked read/write deadlines, and a peer disappearing mid-payload. `net.Pipe`
exercises the codecs together without introducing a live listener.

## Verified locally

Verified on 2026-09-30 with the pinned toolchains:

- `make check` passed: formatting, Go and TypeScript tests, race detection, vet,
  lint, type checks, and production builds.
- `make fuzz` passed all three ten-second targets, with 623,296 inputs exercised
  across decoding, frame round trips, and handshake payloads.
- `go test ./internal/protocol -cover` passed with 95.9% statement coverage.
- `go test -race ./internal/relay` passed the configured frame-limit pipe test.
- `pnpm test:bootstrap` passed real development startup, duplicate-start rejection,
  and interrupt cleanup with PostgreSQL and Redis.

CI is configured to run the same quality gates and five-second active fuzz runs.
These are local verification results; the hosted workflow has not run here.

## Compatibility and next phase

The corrected Go message IDs intentionally reject the starter's incorrect
sequential IDs after `PONG`. The starter had no authenticated deployed peer;
compatibility is with the documented v1 contract and TypeScript values.

Phase 2 implements outbound agent dialing, relay listening, TLS, the live
`HELLO`/`HELLO_ACK` and `AUTH`/`AUTH_OK` sequence, bounded connection concurrency,
and connection lifecycle ownership. Stateful stream validation, registration,
flow control, heartbeat, and public HTTP forwarding remain later phases.
