# Phase 7 — Graceful shutdown and draining

Scope: negotiated connection draining and deadline-bound agent/relay shutdown.
Keep the existing immediate cancellation APIs for transport failure and forced
cleanup. Signals in the binaries initiate graceful shutdown on a separate
lifetime context. Preserve bounded frames, queues, workers, credential expiry,
owner-aware cleanup and no request replay. No dependency, durable model or REST
endpoint is added.

Implementation plan:

1. Define strict GOAWAY and draining errors, negotiation and shared fixtures.
2. Stop new stream admission, retain active duplex work, and close after worker
   cleanup or the shutdown deadline. Continue flow control and heartbeat.
3. Drain CLI and relay signals with a separate lifetime context; reject new relay
   connections, registrations and public requests while existing work finishes.
4. Test active HTTP and duplex streams, slow peers, forced deadline cleanup,
   cancellation, replacement, old peers, malformed messages and reconnect.
5. Run workspace gates, race repetitions, fuzzing and real binary/bootstrap checks.

WebSocket upgrades remain Phase 8; verify their shared duplex-stream lifecycle
here and add actual WebSocket shutdown integration when upgrades are implemented.

Phase 6 is committed as `6b04d27cc7121fbce07c1cd54b7dc3c4456de168` and its
verified full-history export is `/workspace/portway-phase6.bundle`.

## Changes

- `internal/protocol` defines strict connection-level GOAWAY SHUTDOWN and DRAINED,
  pre-allocation bounds and stable draining errors. Go/TypeScript share the wire
  fixture and error contracts. Malformed frames remain terminal during drain.
- `internal/mux` owns one drain worker, closes OPEN admission and orders SHUTDOWN
  after admitted OPEN writes. DATA, FIN, RESET, heartbeat and byte-credit updates
  continue for existing work. It waits for stream and acceptance-worker cleanup
  and both DRAINED messages before closing, preserving buffered response bytes.
  Deadline/cancellation interrupts blocked writers. Repeated calls never extend
  the first deadline.
- `internal/relay` closes node admission, returns 503 for new HTTP work and tracks
  admitted handlers through upload/response cleanup. Registration completion is
  a barrier for draining too, so GOAWAY cannot precede an acknowledged REGISTER.
  Deadline cleanup closes raw tunnel and public HTTPS sockets, including stalled
  uploads, header readers and response writes, then joins owned workers. Owner
  cleanup retains generation watermarks.
- `internal/agent`, `cmd/portway` and `cmd/relay` separate signal shutdown from
  hard transport lifetime cancellation. Local signal drain emits
  `shutdown_started`/`shutdown_complete`; peer GOAWAY emits `tunnel_draining`.
  Remote drain schedules port-invocation reconnect with `relay_draining`, without
  replay. Diagnostic registrations drain and exit without reconnect.
- Local HTTP sockets still serve one request each, but forwarding no longer asks
  the upstream for an immediate connection close. That lets an early response
  survive pending-upload cleanup; owned cancellation/cleanup closes the socket.
  Worker terminal reasons also survive cancellation racing a heartbeat callback.
- `.env.example`, configuration validation and source-of-truth documentation
  cover the new deadlines. Immediate `Close`/lifetime cancellation APIs remain
  available for failures. No dependency, REST endpoint or database change is added.

| Setting                    | Default | Valid range                     |
| -------------------------- | ------- | ------------------------------- |
| `PORTWAY_SHUTDOWN_TIMEOUT` | `10s`   | Positive duration, at most `1m` |
| `RELAY_SHUTDOWN_TIMEOUT`   | `10s`   | Positive duration, at most `1m` |

## Verification

Commands actually run successfully:

```bash
make fmt
make check
make fuzz
pnpm test:bootstrap
go test -race ./...
go vet ./...
go build -o bin/portway ./cmd/portway
go build -o bin/portway-relay ./cmd/relay
go test -race ./internal/relay ./internal/mux ./cmd/portway \
  -run 'Shutdown|GoAwayState|Draining|Drain|DiagnosticPeer|HeartbeatTimeoutDespite' \
  -count=20 -timeout=90s
go test -race ./cmd/portway \
  -run TestDiagnosticDrainWaitsForAckPublication -count=20 -timeout=30s
go test ./internal/protocol -run '^$' -fuzz '^FuzzHandshake$' \
  -fuzztime=10s -parallel=2
go test -race ./internal/relay \
  -run TestLocalEarlyResponseFinishesBeforeUpload -count=100 -timeout=90s
```

`make check` covers Go/TypeScript tests, race tests, vet, formatting, lint,
typechecking and production builds. The final Go gates/builds were repeated
after preserving protocol-error classification, worker terminal reasons and
early-response cleanup on a draining connection. Four
fuzz targets completed 600,138 executions; additional SHUTDOWN/DRAINED handshake
seeds completed 55,413 more. Docker-backed bootstrap verified healthy startup,
duplicate-start rejection and interrupt cleanup in 18.35s.

Repository tests cover unread buffered response data, 1.2 MB duplex flow during
drain, new admission rejection, callback cleanup after RESET, repeated-deadline
bounds, blocked GOAWAY writes, credential expiry, malformed/unnegotiated/duplicate
GOAWAY, invalid headers during drain, old-peer EOF, pending TLS, registration
publication, generation replacement and stalled public response readers.

The real-binary check (`/tmp/portway-phase7-smoke.py` in this workspace) sends
actual SIGTERM to both binaries during an active 1,703,936-byte HTTP response.
Both preserve the full response and reject new requests with 503. Relay restart
recovers with a higher generation and the same URL. Forced 80ms agent and 100ms
relay deadlines close local sockets and fail interrupted requests; SIGTERM also
interrupts reconnect backoff cleanly. All child processes and temporary state are
removed afterward. Phase 6 bundle verification confirms complete history through
`6b04d27`.

## Risks and next boundary

WebSocket upgrades and their HTTP integration remain Phase 8. Duplex stream
shutdown is covered here; actual upgraded sockets need coverage with that phase.
Legacy peers receive no unsupported frames or new error codes. A legacy accepting
endpoint cannot prove peer consumption, so it waits for peer closure or its
deadline; a legacy origin can close after its own work finishes. Deadline expiry,
credential expiry and transport failure can still interrupt active work, which
is never replayed. Reconnect uses the configured relay; alternative relay
selection remains Phase 11. Production credential issuance and dashboard/API
features retain their later-phase scope.

Phase 7 implementation and acceptance checks are complete.
