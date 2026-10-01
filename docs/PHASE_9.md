# Phase 9 — Control Plane

Context: implement authentication, scoped projects/tunnels, a configured relay
registry, short-lived credentials and CLI bootstrap. API.md/OpenAPI define REST;
DATABASE.md defines logical entities; migrations remain Phase 10, dynamic relay
health/selection remains Phase 11. Phase 8 is committed as
`8e95b1792799c89f317afa1aee894679f900abec`; its full-history bundle is
`/workspace/portway-phase8.bundle`.

Plan:

1. Define concrete auth, authorization, pagination, idempotency, credential and
   bootstrap contracts before implementation.
2. Implement a bounded process-local control store and HTTP API with strict input,
   protected private seed configuration and safe error envelopes.
3. Add bounded authenticated relay credential verification outside the hot path;
   bind issued credentials to the selected relay, tunnel and generation.
4. Integrate opt-in CLI API bootstrap and credential refresh on reconnect without
   changing direct-file development mode or replaying failed requests.
5. Verify tenant/role isolation, expiry/revocation, concurrent/idempotent mutations,
   input/capacity/timeouts, control outage isolation, actual bootstrap and all gates.

Invariants: outbound agent, local fixed service, no API/database call per public
request, monotonic generation ownership, hashed server-side credentials, bounded
state/requests/concurrency, secret-free logs and terminal authorization failures.

Implementation and acceptance verification are complete. This milestone is committed
before Phase 10 database implementation.

## Changes

- `apps/api/src/store.ts`, `models.ts`, `validation.ts` and `seed.ts` implement a
  bounded process-local store with private hash-only provisioning, API keys and
  expiring sessions, organization/role authorization, scoped projects/tunnels,
  configured relay metadata, cursor binding and atomic credential/generation
  issuance. Successful metadata mutations support bounded idempotency. Secret
  responses are returned once and never stored for replay.
- `apps/api/src/app.ts` implements the REST envelope and routes. It rejects
  duplicate/unknown JSON fields, wrong types, malformed UTF-8, invalid queries and
  excessive input. Input is capped at 64 KiB, active requests at 128 and rates at
  120/minute per remote address/user key. Body reads time out at five seconds;
  HTTP sockets, headers, requests and shutdown also have finite limits.
- `internal/control` uses verified HTTPS (loopback HTTP allowed for development),
  no redirects, 64 KiB response/32 KiB header limits, bounded connection pools,
  explicit deadlines/cancellation, duplicate-key/nesting checks, exact JSON field
  names and safe errors. `internal/auth/control.go` sends only the credential hash
  during relay AUTH using a separate relay-scoped key.
- Relay registration binds the issued tunnel ID and exact generation. The existing
  local registry, mux, heartbeat, streaming, ownership and expiry mechanisms carry
  public requests without API/database calls.
- The CLI opts in with `PORTWAY_API_TOKEN_FILE`, reserves its persisted generation
  minimum, obtains/validates assignment, advances local state and verifies the
  relay's expiry/registration against the assignment. Port invocations refresh
  expired leases or recover transport/API failures with the existing bounded
  backoff. Each attempt obtains a fresh credential; failed application requests
  are never replayed. `register --once` supports API readiness too.
- `scripts/control-init.mjs` provisions distinct private development user/relay
  tokens and a hash-only seed, preserving existing files. Setup/dev invoke it;
  `.env.example` and README document opt-in configuration, seven-day development
  keys, separate API/relay trust and the restart boundary. The SDK exports envelope
  and assignment types with string generations. OpenAPI and logical-model notes
  were updated before behavior implementation; architecture/routing/status docs
  now describe the implemented boundary.
- CI and `make check` include the built API/CLI/relay integration test. New tests
  cover authorization, revoked/expired parent policy, idempotency, cursors,
  concurrent issuance, uint64 limits, private seed loading, malformed input,
  capacity, slow peers, cancellation, verified TLS and generation-bound leases.

## Verification

Actually run in this workspace with Go 1.26.8, Node 24.19.0, pnpm 10.12.1 and
Docker 28.4.0:

- `make fmt` and `make check` pass: formatting, all Go tests, all Go race tests,
  `go vet`, workspace lint/typecheck/tests, Go/TypeScript/dashboard builds and real
  control integration. The API suite passes 15 tests, including slow-body timeout
  and 128-request admission/release.
- `make fuzz FUZZTIME=5s` passes all six targets (567,147 executions): framing, round trip, handshake,
  stream payloads, control JSON and WebSocket response validation.
- `pnpm test:control` passes using the actual built Node API and Go binaries,
  verified public TLS/SNI, an isolated private seed and local HTTP/SSE service.
  With the API stopped, admitted HTTPS and SSE requests complete. Lease expiry
  schedules bootstrap; API restart recovers the same URL with a higher generation.
  Revocation then rejects further bootstrap and the CLI exits without request
  replay. The test owns and stops its child processes and removes private fixtures.
- The Go CLI integration independently verifies that public requests cause zero
  API calls during an outage, expiry reacquires a different credential, recovery
  advances generations and shutdown joins workers without exposing credentials.
- `pnpm test:bootstrap` passes the Docker development readiness, duplicate-start
  rejection and interrupt cleanup test (22.52 seconds).
- `pnpm db:validate` and `pnpm db:generate` pass against the unchanged logical
  Prisma schema baseline.
- OpenAPI YAML parses; every local `$ref` resolves; JSON generations are strings
  and the returned credential token is represented as a response property.
  `git diff --check` and Phase 8 bundle verification pass.

## Risks and next boundary

- API metadata, sessions, issuance records, audit history and revocation are in
  memory. Restart reloads only the configured seed and loses mutations; it can
  restore a seed's earlier non-revoked policy. Use this boundary for development,
  not durable production policy. Phase 10 must provide transactions/migrations and
  durable key, tunnel, generation and revocation state.
- Existing admitted sessions are not polled or pushed closed on revocation/API
  restart. They keep their bounded lease until expiry/failure/shutdown. Default
  lease expiry is five minutes (configurable 1–900 seconds); refresh creates a new
  session and interrupts outstanding streams.
- Configured HEALTHY relay metadata is operator provisioning, not live presence or
  capacity measurement. API `CONNECTING` means assignment issued. Dynamic relay
  registration/health, confirmed presence, selection/drain and failover are Phase 11.
- Persisted client minima prevent generation rollback after API restart for the
  same client. Lost local generation state still requires the explicit recovery
  override when a running relay retains a higher watermark.
- OAuth/device login, API-key management UI, custom domains, metrics/log storage,
  active revocation delivery and dashboard implementation remain later work.
  Terminal revoked tunnel records are retained; project deletion requires no child
  tunnel records. State saturation fails closed instead of silently evicting
  active policy. The API binds loopback and remote access needs a trusted HTTPS proxy.
