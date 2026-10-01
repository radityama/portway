# Phase 10 — Database

Context: replace Phase 9's process-local policy with PostgreSQL/Prisma durability.
DATABASE.md is the logical schema source; API.md/OpenAPI retain Phase 9 JSON and
scoping. Phase 9 is committed as `06b827b4c18c2f4cbc5e815c584a0364dcf21c8c`.
Terminal and connector pushes both returned HTTP 403; no remote ref was changed.
The verified `/workspace/portway-phase9.bundle` includes Phases 8 and 9.

Plan:

1. Document durable session/credential bindings and idempotency records, uint64
   storage, seed safety and transaction boundaries before implementation.
2. Check in baseline and additive Prisma migrations with constraints and justified
   auth/scoping/expiration indexes; preserve existing logical entities.
3. Implement a PostgreSQL backend with scoped queries, bounded pools/deadlines,
   atomic generation/credential/audit mutations and durable idempotency.
4. Add create-only, transactional hash-only seed import and development migration
   commands. Use explicit memory mode only for isolated tests/development.
5. Verify real PostgreSQL constraints, concurrent API instances, transaction
   rollback, restart persistence, revoked policy, seed reruns, database outages and
   data-plane isolation, then run all quality gates.

Invariants: outbound agent; no API/database query for a public request; fixed local
service; uint64 generations; tenant isolation; hashed credentials; bounded API work
and database deadlines; no fallback to an empty memory store on database failure.

Changes:

- `prisma/schema.prisma` and two checked-in SQL migrations preserve the logical
  entities and add durable sessions, idempotency and credential parent/relay
  bindings. Exact Decimal(20,0) generations cover the full uint64 range. Unique
  hashes, tenant slug keys, scoped keyset/expiry indexes, CHECK constraints and
  foreign keys enforce the documented boundaries. Migrations are transactional;
  incompatible baseline data aborts the upgrade without partial schema changes.
- `apps/api/src/prisma-store.ts`, `database.ts`, `backend.ts` and `cursor.ts` provide
  scoped database reads and atomic mutation/admission/audit/idempotency writes.
  PostgreSQL is the default; memory requires explicit configuration. The pool has
  eight connections, a 2s pool wait, 3s connection/socket bounds, 1s transaction
  admission/lock wait, 1.5s statement limit and 3s interactive transaction limit.
  Storage errors return a safe 503. No raw issued secret is stored or replayed.
- AUTH checks current durable parent/session/member policy and the latest assigned
  relay/generation. Superseded credentials fail future AUTH even after relay
  restart; admitted sessions keep their local lease until replacement/expiry.
- `provision.ts` and `seed-database.ts` implement explicit create-only hash-only
  imports. Conflicts roll back the entire import. Existing generations, status,
  roles, revocations and history survive reruns. API startup never migrates/seeds.
  Development deploys migrations and provisions after PostgreSQL is healthy.
- Database and real API/CLI/relay tests use owned temporary PostgreSQL containers.
  The existing memory scenario shares the binary fixture. `make check` and CI now
  include both modes and database contracts; bootstrap checks PostgreSQL readiness.
  README, API/OpenAPI, DATABASE, architecture and phase/status indexes are updated.

Verification (all passed):

- `make check`: formatting, Go tests/race/vet, TypeScript tests/lint/typechecking,
  all production builds, memory API/CLI/relay E2E, 11 PostgreSQL contract checks,
  and real PostgreSQL/API/CLI/relay E2E.
- Database checks cover concurrent instance idempotency, exact generation
  allocation, stale credential rejection, tenant/role scope, per-tunnel admission,
  atomic rollback, seed conflict/rerun safety, hash-only storage, constraints,
  indexed auth, uint64 exhaustion, durable revocation and bounded lock failure.
- Upgrade tests import the Phase 9 baseline with a signed-64-bit maximum and an
  unbound historical credential. A failed constraint upgrade preserves BIGINT
  and leaves new tables absent; the valid additive migration preserves metadata
  and audit history while historical unbound credentials still fail AUTH.
- Real binaries retain a login session across API restart, reconnect with higher
  generations, serve HTTPS/SSE while the API is stopped and PostgreSQL is paused,
  return bounded `STORAGE_UNAVAILABLE`, recover after database availability, and
  retain terminal tunnel revocation after another API restart. No failed request
  is replayed or raw credential logged.
- `pnpm test:bootstrap`: actual PostgreSQL migrations/seed, API readiness,
  development startup, duplicate-start rejection and interrupt cleanup (20.6s).
- `pnpm db:generate`, `pnpm db:validate`, OpenAPI parse/146 local reference checks,
  `git diff --check`, and full-history Phase 9 bundle verification.

Risks and next boundary:

- A global advisory lock initially serializes writers across API instances.
  Fleet-scale throughput needs measured admission and finer lock granularity.
- Cursor signing and rate buckets remain process-local; cursors cannot move
  between instances or survive restart. Fleet-wide rate limiting remains later.
- Audit history is durable without automatic retention; operators must plan
  archival. Historical databases require inspection/baselining before deployment;
  invalid/duplicate policy must be corrected explicitly, never reset silently.
- Relay HEALTHY metadata is operator configuration. CONNECTING records issuance,
  not live presence. Health/capacity reporting, selection/drain/failover are Phase 11.
- Revocation blocks future admission without pushing a close to an active lease.
  An outage that lasts past lease expiry prevents reconnect until policy is usable.
- Phase 10 was committed before starting Phase 11. GitHub
  publication of Phases 8–9 remains blocked by terminal and integration HTTP 403.
