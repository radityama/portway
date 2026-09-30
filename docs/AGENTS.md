# Portway — AI Coding Agent Instructions

## 1. Mission

You are working on a production-grade self-hosted reverse-tunneling platform.

The product provides a simple developer experience similar to Peek:

```bash
portway 3000
```

but owns its own relay infrastructure and protocol.

## 2. Mandatory Reading

Before changing networking behavior, read:

```text
PRD.md
ARCHITECTURE.md
ROUTING.md
IMPLEMENTATION.md
```

Before changing persistence, read:

```text
DATABASE.md
API.md
```

## 3. Architecture Invariants

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

## 4. Change Workflow

For every non-trivial task:

1. Inspect existing code.
2. Read the relevant documentation.
3. Identify the affected boundary.
4. Write a small implementation plan.
5. Make the smallest safe change.
6. Add or update tests.
7. Run quality gates.
8. Update documentation if behavior changed.
9. Summarize risks and unresolved issues.

Do not rewrite large areas unless required by the task.

## 5. Networking Rules

### Never

- use unbounded channels for network data
- read an entire large body into memory by default
- create one permanent goroutine per untrusted connection without lifecycle controls
- leave sockets without timeouts
- trust Host headers blindly
- permit public requests to specify arbitrary upstream destinations
- panic because of malformed peer input
- log raw Authorization/Cookie headers

### Always

- propagate `context.Context`
- cap frame sizes
- cap stream count
- cap request/body sizes
- handle partial reads/writes
- handle connection closure explicitly
- clean up goroutines and sockets
- propagate cancellation from client → relay → agent → local service

## 6. Protocol Rules

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

Do not invent ad-hoc JSON messages in the middle of a binary data stream unless explicitly documented.

## 7. Stream Rules

Every stream must have:

- unique stream ID
- lifecycle state
- cancellation path
- flow-control state
- buffer limit
- timeout policy where appropriate

A stream must not outlive its parent tunnel connection without an explicit supported mechanism.

## 8. Reconnect Rules

Use exponential backoff with jitter.

Never reconnect in a tight loop.

On reconnect:

```text
connect
→ authenticate
→ register
→ compare generation
→ replace stale session
```

Do not silently replay arbitrary failed requests.

## 9. Relay Rules

Relay hot path must remain fast.

Do not add calls such as:

```text
PostgreSQL query per HTTP request
HTTP call to control API per HTTP request
```

Use local memory and Redis-backed ephemeral state where needed.

## 10. Control Plane Rules

The control plane manages metadata and policy, not application bytes.

Mutations should be idempotent where possible.

Authorization must be enforced by organization/project/tunnel scope.

Never assume authentication alone means authorization.

## 11. Database Rules

Use Prisma migrations.

Do not store raw long-lived tokens when hashes are sufficient.

Use transactions for multi-row resource creation where partial success would leave inconsistent state.

Do not add indexes blindly. Confirm the query path first.

## 12. API Rules

Use the documented response envelope:

```json
{
  "data": {},
  "error": null,
  "meta": {}
}
```

Use stable machine-readable error codes.

Do not break API contracts without updating versioning/documentation.

## 13. Security Rules

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

## 14. Logging Rules

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

## 15. Testing Rules

For networking changes, include appropriate combinations of:

- unit tests
- integration tests
- end-to-end tests
- race tests
- fuzz tests
- load tests

At minimum run:

```bash
go test ./...
go test -race ./...
go vet ./...
```

and for the TypeScript workspace:

```bash
pnpm lint
pnpm typecheck
pnpm test
```

## 16. Failure-Oriented Development

For each component, explicitly test:

```text
success
slow peer
peer disappears
malformed input
timeout
resource exhaustion
shutdown
restart
reconnect
```

Do not consider a networking feature complete merely because the happy path works.

## 17. CLI Rules

Human mode should be concise.

JSON mode must be parseable line-by-line and contain no ANSI-only semantics.

Exit codes are part of the interface. Preserve them.

## 18. Documentation Rules

When behavior changes, update the relevant document in the same change.

Examples:

```text
protocol change     → ROUTING.md / ARCHITECTURE.md
DB change           → DATABASE.md
REST change         → API.md
feature semantics   → PRD.md
implementation      → IMPLEMENTATION.md
agent behavior      → AGENTS.md
```

## 19. Dependency Rules

Do not add a dependency without checking:

- maintenance status
- license compatibility
- security posture
- necessity
- whether the standard library already solves the problem

Prefer well-maintained, narrowly scoped dependencies.

## 20. Code Style

Prefer:

- explicit naming
- small functions
- clear ownership
- clear error propagation
- deterministic tests

Avoid:

- clever concurrency
- global mutable state
- hidden retries
- magic timeouts
- deep abstraction layers with no current use

## 21. Completion Checklist

Before saying "done":

```text
[ ] code implemented
[ ] tests added
[ ] failure paths tested
[ ] race/leak concerns reviewed
[ ] logs are safe
[ ] resource limits exist
[ ] timeouts exist
[ ] docs updated
[ ] CI passes
[ ] no known protocol incompatibility introduced
```

## 22. Golden Rule

The right question is not:

> Does this work on localhost?

The right question is:

> Does this still behave safely and predictably when the network, relay, agent, client, or local service behaves badly?
