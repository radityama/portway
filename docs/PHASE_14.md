# Phase 14 — Observability

## Context

Phase 13 was committed as `bca532a`. Phase 14 adds operational measurements
and bounded, scoped request metadata without putting the control plane on the
application traffic path. Phase 14 is committed before starting Phase 15.

## Plan

1. Define metrics, metadata, privacy and retention contracts.
2. Instrument relay/agent connection and stream lifecycles and relay HTTP traffic.
3. Expose bounded local Prometheus scrapes and authenticated API metrics.
4. Carry bounded tunnel observations in existing fenced relay presence reports;
   expose scoped metrics and recent logs in the API and dashboard.
5. Verify streaming, WebSockets, isolation, expiry, counters and lifecycle cleanup;
   run the repository quality gates.

## Changes

Implemented:

- `internal/observability` collects local connection/stream counters, failures,
  actual byte I/O, request outcomes and fixed cumulative latency bins. Prometheus
  labels contain normalized methods/status groups and runtime classes only.
  Relay gauges expose active public sockets, admitted sessions, retained/active
  tunnels, configured limits and drain state. Go runtime metrics describe heap,
  goroutines and estimated CPU classes; API runtime metrics describe process CPU
  seconds and resident memory.
- Relay request wrappers preserve response-controller deadlines, full duplex,
  flushing, informational responses and hijacking. WebSocket buffered frames and
  subsequent socket I/O are counted without recording their contents. Failed or
  canceled responses have stable outcomes; unusual HTTP statuses cannot invalidate
  observation reports. API requests emit normalized JSON metadata. CLI JSON output
  remains one event per line.
- Optional `RELAY_METRICS_PORT` and `PORTWAY_METRICS_PORT` default to 0, bind only
  loopback and enforce connection admission, deadlines and joined shutdown. The
  API's root `/metrics` requires a relay operator key; it is outside the BFF.
  [Prometheus configuration](../deploy/observability/prometheus.yml) provides
  host-local examples with file-based operator authorization.
- Existing fenced relay presence optionally carries 32 tunnel/generation snapshots
  with four recent metadata records each. Collection uses local memory, evicts
  idle entries and prunes expired records. API counter strings preserve all uint64
  bits. Strict validation rejects unknown/nested secret fields, duplicate keys,
  unsafe counters, inconsistent histograms and oversized snapshots. Redis preserves
  empty arrays and decimal strings while setting receipt/expiry times atomically.
- Scoped GET logs/metrics recheck policy, current relay/generation and expiry.
  Missing, evicted, expired, superseded and revoked observations are unavailable;
  backend outages remain 503. Dashboard tunnel details show real traffic counters
  and average whole-request duration; Logs renders the rolling metadata window.
  BFF queries allow only bounded log limits, and viewers retain read-only access.

No durable schema, dependency or protocol frame changes were needed. API/OpenAPI,
architecture, routing, database-state documentation and operator instructions were
updated before the affected implementation boundaries.

## Verification

Passed on the final implementation:

- `DASHBOARD_CHROMIUM_PATH=/usr/bin/chromium make check`: formatting, Go tests and
  race checks, vet, lint, types, production builds, all Node unit tests, and real
  control, database, fleet, domain, dashboard and observability integrations.
- Chromium reported 10/10 passing tests, including known fenced metadata through
  PostgreSQL/Redis/API/BFF, exact display of `9007199254740993` bytes, mobile overflow
  checks, generation mismatch, existing tenant/viewer and outage/restart flows.
  Real relay/agent traffic is verified separately by the observability integration;
  browser rendering uses an explicit report fixture.
- Real observability integration verified reports with zero completed requests
  while SSE remains active, HTTP upload/response bytes, 500 outcomes, WebSocket
  frames, latency/counter consistency, both local scrapes, operator API authorization,
  secret omission, cancellation and local accumulation through Redis/API outages.
- `pnpm test:bootstrap`: real randomized-port development startup, dashboard
  authentication, duplicate-start rejection and interrupt cleanup.
- `pnpm db:validate` and `make fuzz FUZZTIME=2s`: Prisma validated; all six existing
  protocol/control/HTTP fuzz targets passed. No schema migration was needed.
- OpenAPI and Prometheus YAML parsed; all 192 local references resolved.
  `git diff --check` passed. Phase 13's complete-history bundle was verified through
  `bca532a`; Phase 14 is committed before starting Phase 15; no push/deployment was performed.

Logs: `/tmp/portway-phase14-{check,bootstrap,observability,fuzz,prisma}.log`.
Desktop traffic and mobile logs screenshots were visually inspected; ignored
artifacts are `.tmp/screenshots/phase14-traffic.png` and
`.tmp/screenshots/phase14-logs-mobile.png`. Existing development credentials and
PostgreSQL data volumes were preserved; disposable integration containers were
removed.

## Risks

- Counters reset on process restart; scoped counters also reset on generation
  replacement or cache eviction. Collection is capped at 32 observed identities;
  saturated scoped collection increments an operator drop counter while global
  request measurements continue. Recent records are a four-entry rolling window
  with one-hour expiry, rather than durable audit/billing history.
- Observations reflect the latest fenced report, expire after 15 seconds without
  reports and refresh explicitly in the dashboard. Backend outages can prevent
  scoped reads while local metrics and admitted forwarding continue within their
  existing credential lifetimes. Absent observations do not imply zero traffic.
- Long-lived HTTP/WebSocket requests contribute latency only when they finish.
  Relay and agent byte definitions differ as documented; they must not be summed
  as independent application traffic. CPU runtime classes are estimates.
- Operator scrapes require unique free local ports; the API scrape needs a private
  relay operator key. Public ingress must exclude operator routes. No external
  metrics collector, durable audit browsing or OpenTelemetry exporter is deployed.
- GitHub push and deployment were outside this turn; equivalent CI gates passed
  locally. Phase 15 security hardening is the next implementation milestone.
