# Phase 12 — Domains and TLS

## Context

Phase 11 is committed as `d737a37500cdc2205b660b33fc40845d746472c0`.
The verified `/workspace/portway-phase11.bundle` contains complete history through
that commit. Phase 12 was committed before starting Phase 13.
This phase implements scoped custom domains and DNS TXT ownership verification,
relay-local alias routing, negotiated alias Host metadata and wildcard/custom
certificate provisioning and renewal.
API, DATABASE, ARCHITECTURE and ROUTING define the affected boundaries.

## Plan

1. Define domain authorization, challenge lifecycle, alias leases and certificate
   ownership in source-of-truth docs and OpenAPI before implementation.
2. Add an additive Domain migration and bounded memory/PostgreSQL domain APIs.
3. Include authenticated current-generation alias leases in relay report replies;
   route aliases entirely from local state.
4. Load wildcard/custom PEM certificates in a bounded reload worker, preserve the
   last valid pair during failed renewal and reject expired new TLS handshakes.
5. Provide local CA issuance/renewal and mkcert/external-issuer instructions; test
   authorization, DNS failure, stale assignment, renewal, outages and real HTTPS.

Invariants: fixed loopback upstream, outbound agent, exact generation fencing,
no per-public-request control/database/DNS/file access, bounded work and deadlines,
no raw credentials or private-key logs and no request replay.

## Changes

- `apps/api/src/domains.ts`, backend/stores and HTTP routes add organization-scoped
  registration, pagination, one-time challenge rotation, DNS verification,
  activation and disable. Hostnames are globally reserved, including disabled
  records. Only challenge hashes persist. DNS has finite concurrency, byte limits
  and a complete deadline; lookup runs outside writer transactions and commit
  rechecks current proof, authorization, tunnel revocation and expiry. Metadata
  idempotency and audits remain atomic; proof retries never store or replay secrets.
- Prisma adds nullable proof hash/expiry fields and indexed domain query paths.
  `20261001002000_domain_verification` preserves legacy hostname records and disables
  associations without a verifiable proof. Coupled-field and verified-state CHECK
  constraints reject invalid proof state, including SQL NULL bypass attempts.
- Authenticated relay report replies include bounded complete alias snapshots for
  proof-backed ACTIVE domains assigned to that node. `internal/relay/domains.go`
  replaces local policy atomically, checks snapshot expiry and binds routing to
  the exact admitted generation. Session leases still bound availability. Control
  outages retain existing policy until expiry; fresh reports remove disabled,
  revoked or reassigned aliases.
- Optional `custom_domains` negotiation adds strict `public_host` metadata to
  OPEN_STREAM while `host` retains the registered hostname binding. The agent
  preserves alias Host and forwarded authority while dialing only the configured
  numeric loopback port. Legacy agents receive ordinary generated-host streams;
  custom aliases return 501 before opening a stream. Go/TypeScript share byte-level
  fixtures, strict malformed-input checks and exact uint64 generations.
- `internal/certificates` loads wildcard/custom PEM pairs into an atomic cache.
  A bounded joined worker validates private regular files, key match, validity,
  server usage and hostname coverage. TLS callbacks use only cached data and
  local policy. Invalid reload preserves the prior valid cache; expired leaves
  reject new handshakes. Certificate SAN coverage alone cannot authorize an alias.
- `cmd/certctl` builds as `bin/portway-cert`. Explicit CA initialization preserves
  existing valid roots and fails closed for partial/invalid roots. Issuance reuses
  valid matching pairs outside their renewal window or publishes immutable renewed
  leaf/key bundles under the same root through an atomic private manifest pointer.
  It installs no system trust and preserves agent credentials. README documents
  local CA, mkcert and external PEM issuance/deployment workflows.
- Makefile and CI include real PostgreSQL/DNS API and real HTTPS/CLI/relay domain
  suites. API/OpenAPI, DATABASE, ARCHITECTURE, ROUTING, PROTOCOL, implementation
  status and environment examples describe the new boundaries.

## Verification

Commands actually run:

- `make check`: formatting; all Go tests, race detection and vet; root/protocol/API
  tests; ESLint; TypeScript typechecking; production builds; memory/PostgreSQL
  control flows; database contracts; both fleet suites; both domain suites. Passed.
- `pnpm --filter @portway/api test`: 25 tests passed, including scoped domain
  lifecycle, one-time proof/idempotency, rotation/removal races, unsafe hostnames
  and a real bounded DNS resolver fixture.
- `pnpm test:database`: 11 checks passed with three migrations. Baseline records,
  exact signed-64-bit legacy generation, credentials and audit survive upgrade;
  legacy domain reservation survives in DISABLED state without proof authority.
- `pnpm test:domains-api`: 4 checks passed across two real PostgreSQL API clients
  and a UDP DNS fixture. Concurrent hostname reservation returns one proof;
  verification is durable and idempotent; activation binds the current generation;
  rotation/removal clears policy; DNS timeout returns 503 within its deadline
  without marking ownership verified or adding an audit.
- `pnpm test:domains`: real API, CLI, relay and HTTPS passed. Alias/wildcard requests
  preserve Host and forwarded authority. Unapproved SAN and mismatched Host/SNI
  fail before upstream access. Renewal changes the served certificate serial under
  unchanged CA trust; a malformed replacement preserves the good certificate.
  Admitted alias HTTPS survives API/database outage. Restart retains ACTIVE policy
  and disable removes it while the generated hostname continues to work.
- `go test -race ./internal/relay ./internal/protocol ./internal/control`: final
  alias snapshot expiry/generation fencing, legacy 501 behavior, strict nested
  snapshot fields and exact uint64 route validation passed after the full gate.
- `pnpm test:bootstrap`: actual development startup, PostgreSQL readiness,
  dashboard, duplicate-start rejection and interrupt cleanup passed while
  preserving the existing development volume.
- `pnpm db:generate` and `pnpm db:validate`: Prisma client generation and schema
  validation passed. OpenAPI parsed; all 177 local references resolved, with all
  five domain paths and the bounded alias schema present.
- `make fuzz FUZZTIME=5s`: all six protocol/control/WebSocket fuzz targets passed;
  OPEN_STREAM fuzzing includes the custom-domain fixture seed.
- `git bundle verify /workspace/portway-phase11.bundle`: complete history through
  Phase 11 verified. Final formatting and `git diff --check` passed.

Phase 12 was committed before starting Phase 13.

## Risks and deployment boundaries

- ACTIVE represents routing policy rather than distributed certificate readiness.
  Every selected relay needs matching certificate deployment, and public DNS/ingress
  must follow the assigned node. TXT ownership proof does not configure ingress;
  no cross-relay application proxy or ingress controller is installed.
- Production ACME accounts/challenges and issuer scheduling remain external/future
  work. Local CA tooling does not install OS/browser trust. Renewals keep the CA
  stable; operators plan explicit root replacement before its expiry. Already-open
  TLS streams remain governed by session/stream leases, rather than certificate
  replacement or expiry forcibly terminating them.
- Domain disable/revocation/reassignment propagates on the next successful report.
  During control outages, an old alias snapshot can remain for at most 15 minutes,
  shortened by session expiry. This retains Phase 11 partition/lease boundaries;
  it does not provide globally exclusive active ownership across partitioned nodes.
- The initial cap is 128 domain records, including disabled hostname reservations.
  Safe reassignment/retention administration and dashboard workflows remain future
  work. Legacy associations are preserved but require fresh proof before use.
- Retired certificate bundles are retained for rollback. Operators remove them
  only after confirming no active manifest references them. A partial root or stale
  issuer lock after a crashed invocation requires operator inspection; the tool
  never silently replaces a CA. Key/manifest paths must be private regular files;
  deploy renewals through immutable files and atomic manifest replacement.
- Existing writer serialization, process-local cursors/rate state and bounded
  report/resource budgets require sizing before larger production deployments.

Temporary DNS servers and database containers are owned and cleaned up by test
fixtures. Existing databases, development environment files and credentials are
preserved; no reset or drop command is used.
