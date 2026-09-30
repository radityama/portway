# Portway — AI Coding Agent Instructions

## 1. Mission

You are working on a production-grade self-hosted reverse-tunneling platform.

The product provides a simple developer experience similar to Peek:

```bash
portway 3000
```

but owns its own relay infrastructure and protocol.

## 2. Documentation hierarchy

The documentation is the source of truth for behavior and architecture.

Read as needed:

- `docs/PRD.md` — product requirements
- `docs/ARCHITECTURE.md` — system design
- `docs/DATABASE.md` — logical data model
- `docs/API.md` — API contract
- `docs/ROUTING.md` — routing/data plane
- `docs/IMPLEMENTATION.md` — implementation sequence

`prisma/schema.prisma` is only the current Prisma representation of the database model. If the project later switches ORM or storage technology, the logical model in `docs/DATABASE.md` remains authoritative.

## 3. Architecture invariants

Never violate these:

1. The agent connects outbound to the relay.
2. Public application traffic does not go through the control-plane API.
3. PostgreSQL is never queried for every public request.
4. Relay state needed on the hot path is local/ephemeral.
5. The tunnel protocol is explicit and versioned.
6. All network input is validated.
7. Buffers and concurrency are bounded.
8. Every network operation has timeout/cancellation behavior.
9. A stale connection cannot regain tunnel ownership.
10. Secrets are never logged.
11. Control-plane outages should not necessarily terminate existing data-plane sessions.
12. Do not add durable entities or fields solely because an implementation detail suggests them; update `docs/DATABASE.md` first.
13. API behavior is changed in `docs/API.md` and `docs/openapi.yaml` before implementation divergence is introduced.

## 4. Change workflow

For every non-trivial task:

1. Inspect existing code.
2. Read the relevant documentation.
3. Identify the affected boundary.
4. Identify which source-of-truth contract changes, if any.
5. State the invariant(s) being preserved.
6. Make the smallest safe change.
7. Add or update tests.
8. Run quality gates.
9. Update documentation/contracts if behavior changed.
10. Summarize risks and unresolved issues.

Do not rewrite large areas unless required by the task.

## 5. Networking rules

### Never

- use unbounded channels for network data
- read an entire large body into memory by default
- leave sockets without timeouts
- trust Host headers blindly
- permit public requests to specify arbitrary upstream destinations
- panic because of malformed peer input
- log raw Authorization/Cookie headers
- put database access in the relay request hot path

### Always

- propagate `context.Context`
- cap frame sizes
- cap stream count
- cap request/body sizes
- handle partial reads/writes
- handle connection closure explicitly
- clean up goroutines and sockets
- propagate cancellation from client → relay → agent → local service

## 6. Protocol rules

Protocol messages must have:

- explicit type
- explicit length
- version/capability handling
- validation
- deterministic decoding

Adding a new message type requires:

1. protocol documentation
2. codec implementation
3. compatibility tests
4. malformed-input tests
5. integration coverage where relevant

## 7. Database rules

The logical schema in `docs/DATABASE.md` is authoritative.

When using Prisma:

- keep `prisma/schema.prisma` aligned with `docs/DATABASE.md`;
- do not introduce extra models/enums/fields without a documented reason;
- use Prisma migrations for schema changes;
- do not store raw credentials where a hash is sufficient;
- use transactions for interdependent resource creation;
- avoid indexes without a query-path justification.

Redis is ephemeral/distributed state, not the sole durable source of truth.

## 8. API rules

Use the documented response envelope:

```json
{
  "data": {},
  "error": null,
  "meta": {}
}
```

Use stable machine-readable error codes.

Every API resource operation must enforce organization/project/tunnel authorization scope.

When changing the API:

1. update `docs/API.md`;
2. update `docs/openapi.yaml`;
3. update implementation;
4. update tests.

## 9. Security rules

Threat model every public-network change against:

- malformed input
- credential theft
- tunnel enumeration
- host injection
- resource exhaustion
- slowloris behavior
- request smuggling
- unauthorized resource access
- arbitrary proxy abuse

Public hostname entropy is not authentication.

## 10. Logging rules

Production logs should be structured.

Never log:

```text
API tokens
Tunnel credentials
Private keys
Authorization headers
Cookies
Session secrets
Raw request bodies by default
```

Redact sensitive fields before logging.

## 11. Testing rules

For networking changes, include appropriate combinations of:

- unit tests
- integration tests
- end-to-end tests
- race tests
- fuzz tests
- load tests

Minimum Go gates:

```bash
go test ./...
go test -race ./...
go vet ./...
```

TypeScript workspace gates:

```bash
pnpm lint
pnpm typecheck
pnpm test
```

## 12. Failure-oriented development

For each component, explicitly test:

```text
success
slow peer
peer disappears
malformed input
timeout
resource exhaustion
shutdown
reconnect
stale session
```

Do not declare network features complete from a single happy-path request.

## 13. Required task output

For a non-trivial implementation task, report:

### Context

Affected subsystem and source-of-truth docs.

### Plan

Concrete steps.

### Changes

Files and reasons.

### Verification

Commands actually run.

### Risks

Known limitations and follow-up items.
