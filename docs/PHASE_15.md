# Phase 15 — Security Hardening

## Context

Phase 14 is committed as `6292b93`. Phase 15 verifies hostile input at the public
HTTPS, tunnel protocol and control-plane authorization boundaries. Application
traffic continues to use the relay's local registry and the agent's explicitly
configured numeric loopback service.

## Plan

1. Review existing defenses and map all twelve Phase 15 requirements to tests.
2. Exercise raw HTTPS framing, hostname injection, forwarding-header spoofing,
   slow clients and admission recovery through a real relay/agent connection.
3. Verify tenant, viewer, operator, session and parent-credential boundaries with
   disposable PostgreSQL/Redis state and the production API server.
4. Fix demonstrated failures, add regression coverage and run quality gates.
5. Document the threat model, coverage and remaining deployment boundaries.

## Changes

Implemented and committed before starting Phase 16:

- Raw HTTPS tests run through real verified TLS, relay, agent and local HTTP
  sockets. The sixteen-case ingress matrix rejects unsafe hosts/SNI/ports/targets,
  conflicting framing, unsupported transfer encodings, trailers and oversized
  bodies/headers before upstream work. Healthy requests follow every rejection.
- Pipelined framing tests verify that identical duplicate Content-Length values
  and CL+TE input are canonicalized. HTTP-looking body bytes remain body bytes;
  each legitimate request uses a separate upstream socket. Hostile response tests
  reject conflicting lengths, repeated transfer encodings, oversized headers,
  trailers, unsolicited upgrades and excessive informational responses.
- Slow TLS/partial-header peers fill public admission, cause excess peers to be
  rejected and expire under the existing deadline; capacity returns and normal
  forwarding succeeds. Existing stream, body, flow-control, heartbeat, WebSocket
  and shutdown tests cover the remaining resource boundaries.
- Go private token/policy loaders reject symlinks at the final path component,
  check path/file identity and recheck bounded size/type/POSIX permissions. New
  regressions failed before this change and pass afterward. No credential files
  are rewritten or rotated by these checks.
- Relay AUTH explicitly requires parent roles OWNER, ADMIN or MEMBER in both
  control backends. Unknown roles now fail with AUTH_REVOKED. Production PostgreSQL
  already constrains roles; the new suite deliberately removes that constraint
  in its disposable database to model policy drift, verifies AUTH independently
  and restores it. Memory readiness uses the same supported connection roles.
- The production API suite verifies foreign resource/actions/observations remain
  undisclosed, viewer mutations fail, user/operator credentials stay separate,
  malformed/oversized JSON does not allocate state and a stalled chunked upload
  receives 408. Expired/revoked credentials and parent logout invalidate new
  authentication and idempotent replays; process output omits issued secrets.
- `make security-integration`, `pnpm test:security` and CI include the new coverage.
  [SECURITY.md](./SECURITY.md) documents the threat model and all twelve requirement
  mappings. API/OpenAPI and file-loading contracts were updated before their
  implementation changes. No dependency, migration or protocol frame was added;
  public routing remains local with a fixed agent destination.

## Verification

Passed on the final code:

- `DASHBOARD_CHROMIUM_PATH=/usr/bin/chromium make check`: formatting, Go tests,
  race checks, vet, Node lint/types/unit tests, production builds and all real
  control, PostgreSQL, fleet, domain, Chromium, observability and security suites.
  The browser suite reports 10/10; the production API security suite reports 6/6.
- Focused security Go tests passed under `-race`. New file-loader regressions
  failed on the prior implementation; the drift test reproduced successful AUTH
  with an unsupported role before the explicit allowlist and now returns 401
  AUTH_REVOKED. Ordinary PostgreSQL constraints continue rejecting unsupported roles.
- `pnpm test:bootstrap`: real development startup with randomized ports, dashboard
  login/session/logout, duplicate-start rejection and interrupt cleanup.
- `make fuzz FUZZTIME=3s`: all six protocol/control/HTTP fuzz targets passed.
  `pnpm db:validate` passed; no schema migration was introduced.
- OpenAPI 3.1 parsed and all 192 local references resolved. The twelve security
  requirement rows and fourteen named Go test references were checked against
  source. The final formatting and `git diff --check` passed.
- Phase 14's complete-history bundle was verified through `6292b93`. Existing
  development credentials and PostgreSQL data were preserved; disposable
  containers and supervised application processes were removed after testing.

Logs: `/tmp/portway-phase15-{check,security,security-go,control-unit,bootstrap,fuzz,prisma}.log`.
Phase 15 is committed before starting Phase 16. CI includes the new suite; equivalent
gates were run locally, without a GitHub push or deployment.

## Risks

- Public tunnel hostnames do not authenticate application visitors. Per-visitor
  application auth/rate policies and distributed abuse prevention remain outside
  this phase; existing sockets, streams, bytes and deadlines bound resource use.
- Revocation blocks future tunnel authentication; admitted sessions retain their
  bounded lease until local expiry, replacement or transport closure. There is
  no active revocation push. Neither behavior changes in this phase.
- Credential files must now be real private files; final-component symlinks fail
  configuration validation. Keep their directories controlled by the service
  account. Identity checks do not isolate a hostile local user with write access
  to the same directory; Windows deployments need appropriate ACLs.
- This is adversarial regression coverage, not an external penetration audit.
  Phase 16 remains the load and infrastructure chaos milestone. No remote push
  or deployment was performed during this phase.
