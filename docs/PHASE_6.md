# Phase 6 — Heartbeat and reconnect

Scope: registered-session liveness and automatic recovery for `portway <port>`.
The protocol and CLI contracts are in PROTOCOL.md and API.md. Preserve one socket
reader, bounded writers/queues/workers, credential deadlines, local generation
watermarks, and cleanup tied to the exact routing owner. Add no dependency,
durable entity, REST endpoint, or per-request control-plane call.

Implementation plan:

1. Add strict PING/PONG codecs, Go/TypeScript fixtures, validation and fuzz coverage.
2. Add negotiated probes/replies to the owned multiplexer lifecycle, including
   held diagnostic registrations, with bounded writes and complete cleanup.
3. Add cancellable exponential backoff with equal jitter and terminal failure
   classification, reserving a new generation on every registration attempt.
4. Exercise missing/wrong PONG, live traffic during transport loss, recovery,
   stale-owner cleanup, no replay, terminal errors and backoff cancellation.
5. Run the workspace gates, race tests, fuzzing and real development bootstrap;
   update the project status and record actual results here.

Phase 5 was committed as `9b7751cf53c43ee6fef04edd48b3af3404db058f` and exported
in `/workspace/portway-phase5.bundle` before Phase 6 edits.

## Implementation

- `internal/protocol/heartbeat.go` validates bounded connection-level PING/PONG
  with a 16-character lowercase hexadecimal nonce and canonical UTC timestamp.
  Shared Go/TypeScript fixtures cover the payload and fixed 15s/45s timing.
  Oversized payloads fail before allocation; malformed/duplicate/unknown JSON
  and invalid IDs fail closed. The handshake fuzzer covers both codecs.
- `internal/mux/heartbeat.go` owns one probe, monotonic timeout and liveness
  snapshot per negotiated registered connection. It adds one joined worker;
  replies use the existing bounded queue and serialized writer. DATA credit is
  irrelevant to probes/replies. Wrong, duplicate, unsolicited or overdue PONG
  ends the session; other traffic cannot satisfy the outstanding probe.
- Agent and relay negotiate heartbeat for HTTP and held diagnostic registration.
  Diagnostic mode enables no application streams or HTTPS route. Older peers
  retain idle deadlines and reject unnegotiated heartbeat frames. Authentication
  alone still requires registration within its existing timeout.
- `internal/agent/reconnect.go` supplies cancellable backoff with equal jitter:
  half to all of 1s, 2s, 4s, 8s, 16s and then 30s. Short-lived successes retain
  the increasing delay; 60s connected resets it. Retry counters saturate without
  integer overflow. Unknown errors fail closed; security, protocol, local state
  and registration errors stop. Credential expiry stays terminal even when the
  socket deadline fires before its context timer.
- The port CLI owns the explicit loop and safe JSON events. It reloads the
  private credential file, authenticates and reserves a new local generation
  for each registration. Recovery generations apply once per CLI run. Session
  cleanup joins before retry; no request body is retained, migrated or replayed.
- Failure testing also fixed two HTTP recovery edges. An ACK-completion barrier
  lets immediate public requests wait for route publication outside the registry
  lock, with cancellation and the existing write timeout. Failed ACKs never
  enable forwarding. Failed upstream responses close their public HTTP connection
  before forced body-read interruption, preventing reuse of a cancelled connection
  context. Replacement cleanup still checks the exact owner.

## Verification

Verified on 2026-10-01 using Go 1.26.8, Node 24.19.0 and pnpm 10.12.1:

- `make check` passed after the final recovery fixes: Go/Prettier formatting,
  Go/TypeScript tests, the full Go race suite, vet, lint, type checks and production
  builds. Log: `/tmp/portway-phase6-check.log`.
- `make fuzz` passed all four 10s targets, including heartbeat payloads in the
  handshake target: 625,127 total executions. Log: `/tmp/portway-phase6-fuzz.log`.
- Heartbeat/reconnect tests passed 20 race runs. Credential expiry, blocked
  probe writes and cancellation also passed 20 runs. Final active-request
  recovery, publication and expiry tests passed 20 runs after both HTTP fixes.
  Logs: `/tmp/portway-phase6-repeat.log`, `/tmp/portway-phase6-expiry-race.log`,
  `/tmp/portway-phase6-recovery-final.log`.
- Real TLS tests verify negotiated PING/PONG on both the agent and relay,
  echo validation, diagnostic isolation and cleanup on unsolicited replies.
  Unit tests verify missing PONG despite other traffic, wrong/duplicate PONG,
  unnegotiated frames, exhausted DATA credit, bounded writes and joined workers.
- The route-publication regression reproduced a 503 with Phase 5's HTTP handler
  and passed 20 race runs with the barrier. Pending/failed ACK requests also
  exercise timeout and cancellation without enabling forwarding.
- `pnpm test:bootstrap` passed in 18.6s: healthy Docker dependencies, actual TLS
  auth/registration and public TLS, duplicate-start rejection and interrupt
  cleanup. Log: `/tmp/portway-phase6-bootstrap.log`.
- The built CLI/relay smoke test used verified HTTPS curl, interrupted an active
  POST by stopping the relay, observed local TCP closure, restarted the relay,
  recovered with a higher generation and unchanged URL, then served a fresh GET.
  The mutation executed once. Script/log: `/tmp/portway-phase6-smoke.py` and
  `/tmp/portway-phase6-smoke.log`.
- `git diff --check` passed; private environment/credential/state files remain
  ignored. The Phase 5 full-history bundle verifies through `9b7751c` only.

These are local acceptance results; hosted GitHub CI has not been run. Phase 6
remains uncommitted for review.

## Remaining boundaries

Reconnect uses the configured relay; automatic selection/failover is Phase 11.
Credential renewal and live revocation propagation remain Phase 9. Expired or
rejected credentials stop and require operator action. Relay generation watermarks
remain ephemeral across relay restart; preserve local counters and coordinate
one agent per tunnel until durable ownership policy is available.

With default timing, a silently blackholed transport may take up to 60s to detect
(15s to probe, then 45s for its reply); observed EOF/reset recovers on the initial
0.5–1s jittered delay. Socket/idle/credential deadlines can terminate earlier.
TCP/socket writer stalls still affect the shared transport. Graceful draining and
GOAWAY are Phase 7; cancellation currently aborts and joins active work.
