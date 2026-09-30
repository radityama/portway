# Portway Repository Status

## Phase 0

Phase 0 bootstrap is implemented. Toolchains, dependency installs, formatting,
linting, tests, builds, environment initialization, development supervision, and
CI are configured. See [PHASE_0.md](./PHASE_0.md) for the delivery boundary and
acceptance commands.

## Phase 1

The protocol package now implements v1 framing, explicit message values, header
validation before payload allocation, configurable frame limits, complete
partial writes, and bounded version/capability negotiation. Shared fixtures
verify Go and TypeScript compatibility. Unit, pipe I/O, race, and active fuzz
checks cover failure behavior. See [PHASE_1.md](./PHASE_1.md).

## Phase 2

Authenticated agent-to-relay TLS is implemented. Both sides require TLS 1.3 and
Portway ALPN, follow HELLO/AUTH ordering, bound network operations, and close on
cancellation or credential expiry. The relay caps unauthenticated and active
connections and joins them on shutdown. The file verifier uses token hashes,
connect scope, expiration, and revocation. The CLI reports the actual
`relay_authenticated` milestone; development readiness verifies it end to end.
See [PHASE_2.md](./PHASE_2.md).

The next phase is Phase 3: tunnel registration, hostname assignment, and stale
session replacement. Public forwarding, durable credential issuance, database
migrations, and dashboard features remain later work.

## What this starter contains

- monorepo layout for agent, relay, API, dashboard, shared packages, and docs
- PostgreSQL schema implementation matching the Prisma baseline in `docs/DATABASE.md`
- OpenAPI contract derived from `docs/API.md`
- implemented versioned protocol framing and capability-negotiation package
- agent/relay TLS handshake, connection lifecycle, and private development setup
- control-plane HTTP API scaffold
- Docker Compose for PostgreSQL and Redis
- CI quality gates and Docker-backed bootstrap verification
- Codex/Claude Code instructions and master prompt

## Important source-of-truth rule

`docs/DATABASE.md` is authoritative for the logical data model. The Prisma file is an implementation representation. It should not acquire extra domain entities, fields, or enums without first updating the database documentation.

`docs/API.md` is authoritative for API semantics and `docs/openapi.yaml` is its machine-readable representation.

## Verification

Run the following locally when the required toolchains/dependencies are installed:

```bash
make setup
make check
make fuzz
pnpm db:validate
pnpm test:bootstrap
```
