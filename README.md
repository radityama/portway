# Portway

A path from your local port to the web. Portway is a self-hosted reverse-tunneling platform with a Go CLI and relay, a TypeScript control plane, and a Next.js dashboard.

## Current milestone

Phase 0 provides a reproducible development workspace. The API, dashboard, and relay are skeletons. The CLI currently checks whether a local port is reachable; authenticated connections and public forwarding are implemented in Phases 1–4.

See [Phase 0](./docs/PHASE_0.md), [Starter Status](./docs/STARTER_STATUS.md), and the canonical [implementation phases](./docs/IMPLEMENTATION.md).

## Prerequisites

- Go 1.26+; the verified version is pinned in [.go-version](./.go-version).
- Node.js 24.19+ within the 24.x release line, pinned in [.node-version](./.node-version).
- pnpm 10.12.1, pinned in `package.json`.
- Docker with Compose v2 and a running daemon.
- Make for the convenience commands.

## Start development

```bash
corepack enable
make setup
make dev
```

`make setup` creates `.env` from `.env.example` if it is missing, installs dependencies using the committed lockfile, and generates the Prisma client. Existing `.env` values are preserved. Shell variables take precedence.

`make dev` waits for healthy PostgreSQL and Redis containers, builds the relay, and starts the API, dashboard, and relay. It announces readiness after checking all three application services.

Default addresses:

| Service        | Address                        |
| -------------- | ------------------------------ |
| Dashboard      | `http://127.0.0.1:3000`        |
| API health     | `http://127.0.0.1:8080/health` |
| Relay skeleton | `127.0.0.1:8081` (TCP)         |
| PostgreSQL     | `127.0.0.1:5432`               |
| Redis          | `127.0.0.1:6379`               |

Press Ctrl+C to stop the application processes and remove the development containers and network. PostgreSQL data stays in the `portway_portway-postgres` named volume. Startup failures also clean up resources created by that run. Starting a second development session on the same application ports fails before it touches the running stack.

Change service ports in `.env`. When changing PostgreSQL or Redis ports, also update `DATABASE_URL` or `REDIS_URL`. API/dashboard development servers and dependency ports bind to loopback. Relay TLS is scheduled for Phase 2.

If Docker Hub rate-limits image pulls, the official public mirror can be selected:

```bash
POSTGRES_IMAGE=public.ecr.aws/docker/library/postgres:17-alpine \
REDIS_IMAGE=public.ecr.aws/docker/library/redis:8-alpine \
make dev
```

Once the dashboard is running, the CLI skeleton can be exercised from another terminal:

```bash
go run ./cmd/portway 3000
PORTWAY_JSON=1 go run ./cmd/portway 3000
```

## Quality gates

```bash
make check
pnpm db:validate
pnpm test:bootstrap
```

`make check` verifies Go/Prettier formatting, Go and TypeScript tests, Go race detection, Go vet, ESLint, TypeScript types, and production builds. `pnpm test:bootstrap` requires Docker and checks real development startup, duplicate-start rejection, and interrupt cleanup. Run it when no other Portway development session is using the Compose project.

Stop the development stack before running production builds; Next.js uses the same `.next/` directory for both.

Use `make fmt` to apply formatting. `make doctor` checks development tooling; this is separate from the future `portway doctor` product command.

Build outputs are `bin/portway`, `bin/portway-relay`, API/shared-package `dist/` directories, and the dashboard `.next/` directory. `make docker-up` and `make docker-down` manage just the development dependencies. Database migrations, full data-plane integration tests, and load tests remain scheduled in later phases.

## Repository and contracts

- `cmd/portway` — CLI / agent binary
- `cmd/relay` — relay binary
- `apps/api` — Hono control-plane API
- `apps/dashboard` — Next.js dashboard
- `packages` — shared TypeScript contracts
- `internal` — Go data-plane packages
- `prisma` — PostgreSQL schema representation
- `scripts` — development tooling and bootstrap tests
- `docs` — product and engineering contracts

The agent connects outbound to the relay. Public application bytes stay in the data plane; no API or PostgreSQL call is added per public request.

The source-of-truth hierarchy is PRD → architecture → database/API/routing contracts → implementation. `docs/DATABASE.md` defines the logical data model; `prisma/schema.prisma` represents it. `docs/API.md` defines REST semantics; `docs/openapi.yaml` represents them. See the [documentation index](./docs/INDEX.md).
