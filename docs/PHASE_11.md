# Phase 11 — Multi-Relay

## Context

Phase 10 is committed locally as `928ef94cfcac1e394d9767cb2926b354e20d0afc`
(`feat: persist Portway control state in PostgreSQL`). The verified
`/workspace/portway-phase10.bundle` contains complete history through that commit.
Phase 11 was committed before starting Phase 12.

This phase implements relay registration, health, capacity, selection, draining
and agent failover. API, DATABASE, ARCHITECTURE and ROUTING remain authoritative;
public application traffic stays in the relay's local mux. No durable schema or
binary protocol change is required.

## Plan

1. Document ephemeral report leases, durable operator policy, report/auth routes,
   capacity selection, failover and public-ingress deployment requirements.
2. Add bounded Redis presence with TTL, incarnation/sequence fencing and an
   explicit in-memory test backend; retain existing logical durable entities.
3. Implement authenticated registration/health reports and drain/activation,
   effective relay metadata and atomic credential-capacity admission.
4. Add one cancellable reporter per node, local capacity snapshots, drain
   coordination and CLI retry preference for an alternative healthy relay.
5. Test stale/fenced/expired reports, multi-instance selection, capacity, malformed
   requests, Redis/API outages, draining and real two-relay failover; run all gates.

Invariants: outbound agent; fixed local service; no API/PostgreSQL/Redis dependency
for an admitted public request; exact increasing generations; bounded work and
network deadlines; credential redaction; no failed application request replay.

## Changes

- `apps/api/src/presence.ts` adds Redis-backed 15s reports, atomic Lua fencing,
  idempotent duplicates and expiry. Report receipt time comes from Redis. One
  bounded client has a complete 1s operation deadline, a 128-command limit and
  socket destruction on timeout; failures return `PRESENCE_UNAVAILABLE` without
  substituting successful local state. Explicit memory mode has separate bounded
  process-local presence.
- API/backend/store changes add relay-key-scoped register/report/drain/activate
  endpoints. Nodes report only provisioned identity and cannot change destinations.
  Reads expose effective live status and capacity; readiness requires a fresh
  healthy node. Operator drain persists policy and an audit in PostgreSQL;
  health refresh and seed reruns cannot undo it. Drain/activation require database
  and presence availability. Internal report traffic uses separate bounded
  per-IP/per-node quotas.
- Assignment selection combines fresh health, operator policy, observed local
  limits and latest unexpired credential reservations. Existing writer transactions
  serialize admission across API instances. Renewal excludes the requesting
  tunnel's own reservation, while observed saturation still blocks admission.
  Rejections consume no generation or credential. Usable assignments remain sticky;
  a healthy alternative to the CLI's failed relay is preferred.
- `internal/control/reporting.go`, `internal/relay/reporting.go` and relay startup
  add strict bounded report clients and one joined reporter per node. Snapshots
  read local socket/tunnel/stream/registry counters. Transient reporting failures
  preserve admitted local leases. Expiry re-registers; fenced or revoked reporters
  drain. Ambiguous failures, including canceled requests, skip sequence numbers so
  a later snapshot cannot conflict with a potentially committed observation.
- Operator drain stops local admission, joins reporting, allows existing streams
  to finish and enforces the existing shutdown deadline. Ordinary shutdown reports
  ephemeral DRAINING without permanently disabling the node. Development reporting
  has independent API settings and preserves private-file credential verification.
- CLI control mode remembers its assigned node and requests another eligible node
  after retryable transport failure. Recovery obtains a higher durable generation
  and a new credential; interrupted application requests fail without replay.
- Tests, Makefile and CI add real Redis/PostgreSQL policy tests and two real relay
  processes. API/OpenAPI, DATABASE, ARCHITECTURE, ROUTING, README, implementation
  status and development configuration document these behaviors and boundaries.

## Verification

Commands actually run:

- `make check`: formatting; root, protocol and API tests; all Go tests, race
  detection and vet; ESLint; TypeScript typechecking; production builds; real
  memory/PostgreSQL control flows; PostgreSQL contracts; both fleet suites.
- `pnpm --filter @portway/api test`: 21 tests passed, including stale leases,
  authorization, renewal at reservation capacity and observed socket saturation.
- `pnpm test:fleet-api`: 5 checks passed with real Redis/PostgreSQL and independent
  API/database clients. Four of six concurrent tunnels reserve two slots per node;
  renewal succeeds without admitting an extra tunnel. Expiry, replacement,
  duplicate fencing, durable drain/seed preservation and corrupt Redis state fail
  safely.
- `pnpm test:fleet`: actual HTTPS/SSE continue during a paused Redis service;
  assignment fails within its deadline without partial writes. Active SSE finishes
  during operator drain; the agent moves to the backup. A forced backup crash
  causes recovery to the primary while the failed node's report is still fresh,
  with higher generations and exactly one interrupted mutation attempt.
- `pnpm test:bootstrap`: real development startup, PostgreSQL readiness, dashboard,
  duplicate-start rejection and interrupt cleanup passed while preserving the
  existing development volume.
- `go test -race ./internal/relay -run 'TestReporter' -count=1 -timeout=20s`:
  expiry recovery, ambiguous responses, canceled-report sequencing and joined
  shutdown passed. The final `make check` also passed after these fixes.
- `make fuzz FUZZTIME=5s`: all six protocol/control/WebSocket fuzz targets passed.
- `pnpm db:validate`: Prisma schema validation passed. OpenAPI YAML parsed and all
  165 local references resolved; all four relay routes are present.
- `git bundle verify /workspace/portway-phase10.bundle`: complete history through
  Phase 10 verified.

The full gate also verifies API/database outage isolation and durable restart from
Phase 10. Fixtures own and clean up temporary PostgreSQL/Redis containers; no
existing database is reset. Detailed traffic metrics remain Phase 14.

## Risks and deployment boundaries

- Public hostname remains stable, but local nodes use distinct public ports and
  recovery emits the new URL. Production ingress/DNS must route each hostname to
  its assigned relay. This phase does not install an ingress controller or add
  relay-to-relay application proxying; random node load balancing cannot resolve a
  remote mux. Assignment metadata is the operator integration point.
- Capacity snapshots are advisory; relay hard limits remain authoritative. A full
  retained registry is conservatively excluded, even for a reconnect. Reports may
  lag by their interval; transport preference provides earlier agent recovery.
- A fresh different process using the same node ID cannot replace a healthy report
  until expiry or DRAINING. Every node needs a distinct scoped key/ID and configured
  listener metadata. Report TTL/deadline validation requires synchronized clocks.
- Redis and PostgreSQL are required for new assignments. An admitted session remains
  local until its lease expires, its transport fails or the process shuts down.
  A failed final drain report relies on TTL expiry; drain policy can take one report
  interval to reach the node. An old partitioned admitted lease is not global active
  ownership consensus, although stale credentials cannot authenticate after a
  newer durable assignment.
- Existing global writer serialization, process-local cursor/rate state, bounded
  4096-node candidate reads and report quotas need sizing before larger deployments.
  Persistent DEGRADED/OFFLINE policy remains an operator exclusion, while reporters
  may also report DEGRADED health. Automatic ingress coordination and measured
  production fleet/load tuning are follow-up deployment work.
