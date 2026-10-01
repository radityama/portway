# Phase 5 — Flow Control

Phase 5 removes the Phase 4 application's synchronous receive-pipe coupling.
A slow stream now consumes bounded credit and queue space while other streams
and control messages can progress. Agent outbound connections, credential-bound
generation ownership, fixed loopback destinations, and the relay-local request
path are preserved. There are no database, REST/OpenAPI, or dependency changes.

## Plan and delivery

1. Define stream/connection byte windows and a validated WINDOW_UPDATE contract,
   with Go/TypeScript constants and shared wire fixtures.
2. Gate DATA sends on both credits before acquiring the shared frame writer.
3. Replace synchronous receive pipes with independently bounded, reusable queues
   and return credit on consumption/discard through one owned control worker.
4. Preserve ordering, FIN/reset, cancellation, deadlines, expiry, replacement,
   and worker/buffer cleanup under concurrent traffic.
5. Test stalled readers, shared-budget exhaustion, malicious credit, and HTTPS
   behavior; update contracts and run repository quality gates.

These steps are implemented in `internal/protocol/window.go`,
`internal/mux/flow.go`, the multiplexer lifecycle, and agent HTTP forwarding.
Contracts are documented in [PROTOCOL.md](./PROTOCOL.md),
[ROUTING.md](./ROUTING.md), and [ARCHITECTURE.md](./ARCHITECTURE.md).

## Credit and memory bounds

| Bound                                                    | Value                                                |
| -------------------------------------------------------- | ---------------------------------------------------- |
| Initial stream send/receive credit in each direction     | 64 KiB                                               |
| Initial connection send/receive credit in each direction | 1 MiB                                                |
| DATA payload                                             | At most 16 KiB or the smaller negotiated frame limit |
| WINDOW_UPDATE payload                                    | Exactly 4 bytes, positive uint32 in network order    |
| Queued bytes per stream                                  | At most 64 KiB                                       |
| Queued bytes per connection                              | At most 1 MiB                                        |
| Receive storage page                                     | 4 KiB, allocated lazily and reused                   |
| Allocated pages per connection                           | At most `256 + 2 * MaxStreams`                       |
| Receive page allocation at 32 streams                    | At most 1.25 MiB including page slack                |
| Pending reset/rejection control frames                   | At most `2 * MaxStreams + 4`                         |

Receive queues coalesce tiny frames into pages. This prevents a one-byte DATA
flood from retaining one allocation and queue entry per frame. The page cap
includes partially consumed head/tail pages; bounded metadata, HTTP parsing
buffers, application copy buffers, and one decoded frame are additional. Raising
the existing stream/connection admission settings raises their respective total
resource budgets. No new environment settings are introduced; v1 windows are
fixed wire constants shared by both peers.

## Wire and lifecycle behavior

Each DATA payload consumes stream and connection credit. Positive WINDOW_UPDATE
increments restore stream credit for a nonzero ID, or connection credit for ID 0.
Increments cannot exceed their initial window, and additions cannot exceed its
remaining capacity. Over-credit DATA, zero increments, malformed lengths,
unknown future IDs, or invalid stream state terminate the connection. Header
validation rejects wrong update sizes before allocation.

Credit waits occur outside the shared writer lock and observe stream/parent
cancellation and deadlines. DATA releases that writer after each bounded frame.
The shared reader queues incoming bytes and keeps reading other frames. Reads
return credit; a single owned worker coalesces credit counters and sends the
bounded reset/rejection queue, with the existing write deadline. Control queue
saturation closes the connection rather than growing memory.

FIN drains queued bytes before EOF. RESET/disconnect discard queued bytes and
return their connection credit. In-flight DATA for a released old stream is
discarded but still consumes/returns connection credit; no tombstone grows with
stream count. Senders refund only reserved bytes that were never written, not
already-sent bytes when a stream resets. All queued data/pages release on parent
closure and owned workers are joined. Worker admission remains held until its
cleanup actually finishes.

Concurrent OPEN allocation and writes are serialized together so increasing IDs
also arrive in increasing order; ACK waits remain concurrent. Agent response FIN
is sent before waiting for the request direction. Agent cleanup waits for request
FIN/cancellation so it does not reset response bytes still queued at the relay.
Early local responses can finish and cause the relay to cancel a pending upload.
After a complete public response, the remaining request body is drained only
within its existing size cap and at most one second (also bounded by the stream
deadline). This avoids closing a socket with unread input before the client gets
its early reply. Failed/cancelled responses interrupt body reads immediately.

## Compatibility and run path

HTTP forwarding requires both `multiplexing` and `flow_control` in HELLO_ACK.
Update agent and relay together. Older peers can authenticate and perform
diagnostic registration but cannot start HTTP forwarding with new peers. This
uses the reserved v1 WINDOW_UPDATE frame with capability-gated binary semantics;
there is no silent fallback to Phase 4's synchronous behavior.

The CLI workflow and JSON events are unchanged:

```bash
make setup
make dev
# In another terminal, with an HTTP service on port 3000:
go run ./cmd/portway 3000
```

Use the printed URL and the trusted development curl command in
[Phase 4](./PHASE_4.md). Public TLS, HTTP header/body bounds, 32 default streams,
30-second stream deadlines, and authentication/idle/write limits still apply.

## Failure-oriented tests

Tests verify:

- a full slow-stream window leaves another stream operational
- the 1 MiB shared budget stops further DATA while OPEN/ACK still progress
- reset/discard and late DATA correctly restore connection credit
- tiny frames coalesce into bounded pages and connection close frees storage
- full-duplex bodies larger than the window preserve bytes across updates/FIN
- credit waits terminate on cancellation, deadline, and parent closure
- unsent reservations refund credit, and worker cleanup retains admission
- 64 simultaneous OPEN calls preserve monotonically increasing wire IDs
- malformed/overflowing windows, unacknowledged/future IDs, and excess stream or
  connection DATA terminate malicious sessions
- HTTPS supports a fast request alongside a stalled local upload, concurrent
  requests, early replies before upload completion, owner replacement,
  body/header limits, and existing CLI readiness
- peers missing flow control cannot enable HTTP forwarding

WINDOW_UPDATE is covered by shared binary fixtures, malformed header/payload
tests, and the existing stream-payload fuzz target in `make fuzz`.

## Verification

Local verification on 2026-10-01 used Go 1.26.8, Node.js 24.19.0, pnpm 10.12.1,
and Docker 28.4.0:

- Final `make check` passed formatting, Go and TypeScript tests, Go race
  detection/vet, lint, type checks, and production builds.
- `make fuzz` passed all four targets at 10 seconds each: 1,031,982 executions
  across frame decoding, round trips, handshakes, and stream/window payloads.
- `pnpm test:bootstrap` passed real Docker-backed startup, authenticated
  registration/public TLS readiness, duplicate-start rejection, and cleanup.
- Concurrent HTTP forwarding and 64 simultaneous OPEN calls each passed
  20 race-test runs after enforcing allocation/write ordering.
- Early replies, stalled local uploads, and concurrent owner replacement passed
  20 further race-test runs after the response/cleanup corrections.
- A standalone CLI/relay/local-service curl check verified the CA and returned
  the exact local response using the CLI's ready URL. All smoke-test processes
  and development containers were stopped afterward.

These are local results. GitHub-hosted CI has not been run for the uncommitted
Phase 5 changes. The Phase 4 commit is `262a4f2`; its bundle contains complete
history through that commit and excludes Phase 5's working-tree changes.

## Limits and next phase

Many stalled streams can exhaust connection credit and apply shared backpressure.
TCP packet loss and a blocked socket writer still affect the common transport.
Scheduling is bounded but does not promise strict fairness or throughput targets.
The existing whole-stream deadline also limits long-lived HTTP streams.

Phase 6 adds heartbeat, liveness, automatic reconnect with backoff/jitter, generation
increments, and interrupted-request failure without transparent replay. HTTP/2,
WebSocket upgrades, trailers, custom domains, distributed ownership, and control
plane completion remain in their later phases.
