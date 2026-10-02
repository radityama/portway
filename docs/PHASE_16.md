# Phase 16 — Load and Chaos Tests

## Context

Phase 15 is committed as `aa12131`. Phase 16 exercises all nine load/chaos
scenarios from IMPLEMENTATION.md against real relay, CLI, API, PostgreSQL and
Redis processes. Public forwarding remains local; failures must cancel
interrupted requests without replay and recover with higher generations.

## Plan

1. Add bounded reusable fixtures and workload measurements with owned cleanup.
2. Verify sustained concurrency, stream saturation, large streaming bodies and
   restored capacity; record latency/throughput and resource gauges.
3. Inject real TCP packet loss and latency in a dedicated Docker namespace.
4. Crash agents/relay, interrupt API/Redis and exercise simultaneous reconnects;
   verify integrity, no replay, monotonic generations and fail-closed admission.
5. Add executable targets/CI coverage, run quality gates and document limits.

## Changes

Implemented and committed:

- Shared fixtures own disposable PostgreSQL/Redis, production API/relay/CLI
  processes, private credentials, numeric loopback upstreams and cancellable held
  requests. HTTPS workload generation and digest verification use bounded chunks,
  backpressure, deadlines and joined workers, with no automatic request retries.
- The load profile checks 256 mixed requests across 12 workers, 16 MiB known and
  chunked uploads, a 64 MiB streamed response, and saturation at 32 streams. Excess
  work returns 503 before reaching the upstream. Every admission slot returns;
  byte digests and per-request identities agree and the tunnel does not reconnect.
- The chaos profile injects TCP packet loss, a 100 ms delay spike and a 300 ms
  complete-loss burst. A positive kernel drop counter and intact recovered bytes
  prove actual retransmission, independent of random loss in the shorter sample.
  It checks concurrent admitted forwarding during API and Redis outages and
  verifies new Redis-dependent assignments fail without allocating state.
- Agent SIGKILL cancels an already received POST. Offline work fails before the
  upstream; manual restart preserves the hostname and advances the generation.
  Eight agents recover together across two relay SIGKILL cycles using a healthy
  alternative. Each interrupted POST is received exactly once, every generation
  increases, hostnames stay stable and equal-jitter retries retain their bounds.
- A replacement using the crashed relay's ID drains while its old 15-second
  presence lease remains fresh. Serving continues on the standby until the fence
  expires, then the original node returns for the second failover. Neither lease
  duration nor authentication is bypassed by the tests.
- `internal/mux/load_test.go` exercises 512 stream lifetimes with eight workers,
  including 64 resets of stalled consumers and 448 identity-checked 128 KiB
  transfers. Queued bytes, stream/worker admission and connection credit return to
  baseline; retained receive pages stay within the existing budget.
- The test-only TLS pass-through bridge caps accepted sockets at 32, uses bounded
  buffers and deadlines and joins both copy directions on EOF/cancellation.
  The first crash run exposed an EOF cleanup bug in that new fixture; it was fixed
  and regression-tested in both directions and during a delayed write. No
  production agent, relay, control contract, schema or protocol change was needed.
- Linux impairment runs only in an owned Docker namespace. netem is preferred;
  when that kernel module is absent, an explicit packet-filter backend drops TCP
  packets and delays bridge byte delivery. Missing capabilities fail the test.
  Build-time CA trust is supplied as a secret and no session CA is retained.
- `make load-test`, `make chaos-test`, `pnpm test:load` and `pnpm test:chaos` are
  executable and included in the full check/CI. Metadata-only local results are
  written to ignored `.tmp/load/phase16-{load,chaos}.json` after successful profiles.
  [Test operations](../tests/load/README.md) documents profiles and prerequisites.

All nine IMPLEMENTATION.md scenarios are covered by these two process profiles;
the pre-existing two-relay fleet, security, slow-peer and forced-shutdown suites
continue to run alongside them.

## Verification

Passed on the final implementation:

- `DASHBOARD_CHROMIUM_PATH=/usr/bin/chromium make check`: Go/Prettier formatting,
  Go tests/race/vet, Node unit tests/lint/types, production builds and all control,
  PostgreSQL, fleet, domain, browser, observability, security, load and chaos
  suites. Browser: 10/10, security: 6/6, load: 4/4, chaos: 6/6; no skipped scenarios.
- `go test -race ./internal/mux ./tests/load/netem -run 'TestLoad|TestBridge'`
  passed before the full gate. Stream churn, credit reuse, EOF propagation and
  delayed-write cancellation passed under the race detector.
- `make fuzz FUZZTIME=3s`: all six protocol/control/HTTP fuzz targets passed.
  `pnpm db:validate` passed. OpenAPI 3.1 parsed and all 192 local references
  resolved; no schema or API contract change was introduced.
- Phase 15 committed as `aa12131`; `git bundle verify` confirms the complete
  history in `/workspace/portway-phase15.bundle` through that commit.
- Original-workspace `pnpm test:bootstrap` reached API/dashboard readiness but
  failed relay AUTH with `AUTH_EXPIRED`: existing private development credentials
  had expired. They were preserved rather than rotated. The same bootstrap test
  passed (1/1) in an owned copy of the final source with fresh private credentials,
  shared installed dependencies and a separate disposable Compose project. It
  verified authenticated readiness, dashboard login/logout, duplicate-start
  rejection and interrupt cleanup. Its workspace and volume were removed.
- Final `make fmt-check` and `git diff --check` passed. All 13 original private/
  config files matched their preservation snapshot; the original PostgreSQL
  volume remained. No test containers or supervised application processes were
  retained.

The full gate produced these local measurements (rounded; not a capacity SLA):

| Check            | Observed result                                                                                                              |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| Mixed load       | 256 requests / 12 workers, 367 requests/s; p95 58 ms, p99 63 ms                                                              |
| Large bodies     | Known/chunked 16 MiB uploads: 104/104 ms; 64 MiB response: 534 ms                                                            |
| Admission        | 32 held streams; excess 503; all slots returned and final streams zero                                                       |
| Relay retention  | Heap 0.61 to 2.33 MiB; goroutines 20 to 20                                                                                   |
| Impairment       | Packet-filter backend; complete-loss burst dropped 2 packets and recovered in 656 ms; 100 ms bridge delay observed at 308 ms |
| Reconnect storms | Eight agents, two crashes: 1.00 / 1.98 s recovery; 16 interrupted POSTs each received once                                   |

Logs: `/tmp/portway-phase16-{check,load,chaos,race,lint,fuzz,prisma}.log`,
`/tmp/portway-phase16-bootstrap{,-isolated}.log` and
`/tmp/portway-phase16-final-format.log`.
CI includes the new gates; equivalent gates were run locally. Phase 16 is
committed; no remote push or deployment was performed.

## Risks

- These are bounded local regression profiles, not hardware-sized capacity
  certification or a long soak. Throughput/latency figures vary with host load;
  resource checks catch gross retention but do not prove absence of every leak.
- This managed kernel lacks netem. Packet-filter loss and bridge delivery delay
  were exercised here; the kernel netem path remains available for Linux runners
  with that module. Backend identity is recorded so packet delay and delivery
  delay are not presented as equivalent measurements. No host link is modified.
- Under-ten-second recovery requires a healthy alternative and reachable
  API/Redis for admission. Same-ID cold replacement remains fenced for up to the
  presence TTL, with additional retry delay possible. A single-relay deployment
  has that availability boundary; the tests do not claim otherwise.
- Hostnames remain stable, but local fixture relay public ports change on
  failover. Production still requires assignment-aware ingress/DNS and configured
  public certificates. Public application authentication and bounded admitted
  lease revocation retain their existing semantics.
- No GitHub push, deployment, credential rotation or destructive volume cleanup
  is part of this work. Phase 15's committed history is available in the verified
  `/workspace/portway-phase15.bundle` through `aa12131`.
- Existing workspace development tunnel credentials have expired. A future
  development session needs the documented `make dev-credentials` rotation;
  this task verified fresh startup without replacing those private files.
