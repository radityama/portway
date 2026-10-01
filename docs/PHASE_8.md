# Phase 8 — WebSocket and Streaming

Context: extend the HTTP data plane from Phases 4–7. Protocol and routing
contracts are in PROTOCOL.md and ROUTING.md; no REST or persistence change.

Plan:

1. Negotiate optional `streaming` and `websocket` capabilities. Keep ordinary
   OPEN_STREAM metadata compatible with older peers.
2. Validate WebSocket requests and 101 responses, preserve buffered bytes at
   both upgrade boundaries, and bridge both directions using bounded streams.
3. Use application idle timeouts for streaming peers; flush SSE headers/events
   promptly and preserve incremental chunked uploads/responses.
4. Test malformed handshakes, text/binary/control/fragmented frames, large frames,
   idle timeout, cancellation, backpressure, supersession and shutdown; run the
   Go and workspace quality gates and real CLI acceptance checks.

Invariants: only the configured numeric loopback service is dialed; no control
plane or database call enters the traffic path; stream credit, worker/socket
admission, credential expiry and forced shutdown remain bounded. Failed streams
are never replayed after reconnect.

Phase 7 is committed as `ff98985b261fb5dec89d52dfde3b54205ecc8422`. Its verified
full-history export is `/workspace/portway-phase7.bundle`. Phase 8 is implemented
and verified below. Phase 8 implementation and acceptance checks are complete.

## Changes

- `internal/protocol`, TypeScript contracts and shared wire fixtures define
  optional `streaming`/`websocket` capabilities and the strict optional upgrade
  marker. Ordinary OPEN_STREAM bytes remain unchanged. Upgrade method/body,
  canonical key, version, duplicate fields and subprotocol tokens are validated.
- `internal/httpwire` retains the exact reader owning post-header bytes and
  validates bounded 101 responses. Ordinary HTTP still rejects unsolicited
  upgrades. Two joined pumps transport frames with 16 KiB buffers, directional
  FIN, reset/cancellation and socket idle deadlines. Extension offers are removed.
- `internal/agent` synthesizes transport upgrade headers and dials only the
  configured numeric loopback service. Accepted upgrades carry raw frames;
  rejected upgrades retain their HTTP status/body. Local socket operations
  refresh an idle deadline without extending credentials or parent lifetimes.
- `internal/relay` validates upgrades before opening a stream, preserves
  pipelined client frames, and hijacks public TLS only after a valid upstream
  handshake. Upgrades retain normal stream/socket/handler admission and ownership
  until cleanup, including forced shutdown of blocked public writers.
- `internal/mux` uses one owned idle worker per streaming connection, bounded by
  MaxStreams. Application DATA writes, receipts and consumption refresh activity;
  heartbeat and credit updates do not. Expiration is claimed under Conn.mu before
  RESET so later data cannot revive it. Legacy streams keep whole-request deadlines.
- Ordinary HTTP flushes headers before the first body bytes and flushes every
  body read. Chunked uploads and responses remain incremental. Completed uploads
  clear their read deadline so net/http disconnect monitoring cannot cancel active
  SSE. A synchronized cutoff prevents upload reads from extending the existing
  one-second early-response cleanup deadline.
- Documentation describes negotiation, idle policy, supported upgrades and
  body/memory bounds. The Makefile/CI fuzz sequence includes WebSocket response
  parsing. No dependency, REST endpoint or durable model is introduced.

| Setting                  | Default | Phase 8 behavior                                      |
| ------------------------ | ------- | ----------------------------------------------------- |
| `RELAY_STREAM_TIMEOUT`   | `30s`   | Public/tunnel application idle timeout with streaming |
| `PORTWAY_STREAM_TIMEOUT` | `30s`   | Local/tunnel application idle timeout with streaming  |

Both settings accept positive durations up to 5m. Older peers retain whole-request
timeouts. Applications should emit SSE events or WebSocket ping/pong within the
configured idle interval. Credential expiry and shutdown deadlines still apply.

## Verification

Commands actually run successfully on the final implementation:

```bash
make fmt
make check
make fuzz
pnpm test:bootstrap
go test -race ./internal/relay ./internal/mux ./internal/httpwire \
  -run 'WebSocket|SSE|Chunked|Streaming|LegacyStreams|EarlyResponseCleanup' \
  -count=10 -timeout=90s
python3 /tmp/portway-phase8-smoke.py
```

`make check` covers Go tests, race tests, vet, Go/Prettier formatting, TypeScript
workspace tests, ESLint, typechecks and production builds. Five fuzz targets
completed 765,982 executions, including 177,396 WebSocket header cases. The ten
race repetitions passed (relay 31.32s). Docker bootstrap verified healthy startup,
duplicate-start rejection and interrupt cleanup in 18.41s.

Repository tests cover text/binary payloads, fragmentation, ping/pong and close,
400 KB frames exceeding stream credit, buffered server/client first frames,
application metadata/subprotocol preservation, extension stripping, invalid keys,
versions, methods, bodies, nominated headers and upstream 101 responses. They
verify ordinary HTTP rejection responses, actual legacy peer HTTP/501 behavior,
idle versus active streams, heartbeats not extending idle streams, credential
expiry, cancellation, capacity release, generation supersession, independent HTTP
under WebSocket backpressure, and graceful/forced shutdown of hijacked sockets.
SSE tests verify headers before events and active streams spanning several idle
intervals; chunked tests verify uploads before EOF and responses before completion.
The stalled-upload regression verifies that early-response cleanup finishes within
its one-second bound instead of being extended by streaming read deadlines.

The real binary smoke check in this workspace verifies TLS trust/SNI, WSS with
400,000 binary bytes and buffered/fragmented/control frames, 16 SSE events over
1.6s with a 600ms idle timeout, incremental chunked upload/response, agent and
relay SIGTERM close exchanges, 503 for new work during drain, 100ms forced relay
cleanup, local socket release and higher-generation recovery at the same URL.
No failed stream is replayed. Test processes and development services are stopped.

## Risks and boundaries

- HTTP/1.1 WebSocket version 13 is supported. Compression/extensions, generic
  upgrades, HTTP/2, CONNECT and HTTP trailers remain unsupported.
- Portway transparently transports frames; application endpoints validate RFC6455
  frame/message semantics. It does not assemble whole messages in memory.
- WebSocket sessions use frame/credit/admission/idle/credential limits rather than
  ordinary HTTP body byte caps. SSE and other HTTP still have 16 MiB request and
  64 MiB response limits; reaching a limit after headers aborts the response.
- Healthy applications that emit no data or ping/pong within their configured
  idle interval are closed. Tunnel heartbeat does not substitute for application
  activity. Lower idle durations require enough margin for scheduling/network delay.
- Phase 9 control-plane APIs, durable credential issuance, cross-relay coordination
  and dashboard features remain later work. Local token verification and ownership
  watermarks retain the documented development/relay-local boundary.
