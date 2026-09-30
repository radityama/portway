# Phase 0 — Portway Bootstrap

## Boundary

Repository naming, local development, toolchains, quality checks, CI, and the API skeleton. Product name: **Portway**, meaning a path from a local port to the web. The executable is `portway`; TypeScript packages use `@portway/*`; CLI environment variables use `PORTWAY_*`.

Tunnel remains the domain term used in API resources, data models, protocol messages, and metrics. No durable entity, field, or API resource is renamed.

## Delivery

- Go 1.26.8 and Node.js 24.19.0 pins shared with CI; pnpm 10.12.1 pinned in the manifest.
- A committed pnpm lockfile, exact dependencies, frozen setup installs, and explicit dependency-build allowlist.
- Actual ESLint checks, Prettier and gofmt checks, TypeScript checks, Go race tests, and production builds.
- Repeatable environment initialization without overwriting local configuration.
- A supervised `make dev` that checks tooling/ports, waits for dependency health, starts the application skeletons, checks readiness, and cleans up after interruption or failure.
- Meaningful API/configuration tests and process-supervisor tests, including grandchild cleanup.
- A Docker-backed bootstrap test for startup, duplicate-start rejection, and Ctrl+C cleanup.
- Working Prisma validation/generation commands for the shared schema. Migrations remain Phase 10 work.
- CI uses the same pins and commands, including the bootstrap test.
- Patched dependency versions replace vulnerable versions in the supplied starter.

Scoped overrides pin patched PostCSS and Sharp dependencies of Next.js and
DeepmergeTS used by Prisma configuration. These are checked through the production
build, Prisma validation/generation, and the bootstrap test.

Prisma client tooling is also declared at the workspace root so the generator can resolve the client from `prisma/schema.prisma`. The API uses the same client version; the logical schema remains unchanged.

## Acceptance commands

```bash
make setup
make doctor
make check
pnpm db:validate
pnpm test:bootstrap
```

Use the prerequisites in the root README. The bootstrap test owns the `portway` Compose project for its duration and preserves its PostgreSQL volume.

## Verified locally

Verified on 2026-09-30 with the pinned Go, Node.js, and pnpm versions:

- `make setup` and `make doctor` passed.
- `make check` passed: formatting, Go unit/race tests and vet, 11 Node.js/API test cases, ESLint, TypeScript checks, and production builds.
- `pnpm db:validate` and Prisma client generation passed.
- `pnpm test:bootstrap` passed against real PostgreSQL/Redis containers: API and dashboard readiness, relay reachability, duplicate-start rejection, and interrupt cleanup.
- `pnpm audit --audit-level low` reported no known vulnerabilities.
- The Prisma schema remains identical to the supplied starter and its documented baseline.

Docker Hub limited image pulls in this workspace; the official public ECR mirror
was used for the container test and configured in the local ignored `.env` file.
Go/pnpm tooling for this workspace is installed in `/workspace/.portway-tools/bin`;
add that directory to `PATH` when running the commands here. CI is configured;
the hosted workflow has not been run from this extracted repository.

## Preserved invariants

Application traffic remains separate from the API/database. This phase does not add public routing, authentication, or protocol messages. Configuration failures do not print connection strings or credential values. The development supervisor owns and cancels its child process groups. Docker cleanup is scoped to the Portway Compose project and preserves database data.

## Next phase

Phase 1 completes the versioned protocol. Begin by replacing Go's sequential frame type constants with the explicit values already documented in `PROTOCOL.md` and `packages/protocol/src/index.ts`, then add byte-level compatibility fixtures, partial-write handling, malformed-input coverage, and fuzz tests. The current relay is a TCP skeleton; authenticated TLS is Phase 2.
