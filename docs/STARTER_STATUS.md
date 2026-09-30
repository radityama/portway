# Portway Repository Status

## Phase 0

Phase 0 bootstrap is implemented. Toolchains, dependency installs, formatting,
linting, tests, builds, environment initialization, development supervision, and
CI are configured. See [PHASE_0.md](./PHASE_0.md) for the delivery boundary and
acceptance commands.

The next implementation phase is Phase 1: protocol compatibility, validation,
partial I/O, and fuzz coverage. Authenticated TLS, tunnel registration, public
forwarding, database migrations, and dashboard features remain scheduled in
later phases.

## What this starter contains

- monorepo layout for agent, relay, API, dashboard, shared packages, and docs
- PostgreSQL schema implementation matching the Prisma baseline in `docs/DATABASE.md`
- OpenAPI contract derived from `docs/API.md`
- versioned protocol framing skeleton
- relay/agent Go skeletons
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
pnpm db:validate
pnpm test:bootstrap
```
