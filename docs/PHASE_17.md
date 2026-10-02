# Phase 17 — CLI Completion

## Context

Phase 16 is committed as `e2ee23e`. This phase closes the remaining developer CLI
requirements in PRD.md: saved authentication/configuration, management commands,
diagnostics and bounded local service detection. Existing outbound TLS, local
forwarding, generation fencing, reconnect and graceful shutdown are preserved.
Management reuses the scoped control API; application bytes never enter it.

## Plan

1. Define the CLI contract and local client-state/security boundaries before code.
2. Add bounded typed API management calls and private, endpoint-bound session state.
3. Add login/logout, config, list/status, create/delete, domains, logs and version.
4. Add safe no-port discovery, foreground start, ephemeral bootstrap and local stop.
5. Add doctor, failure-oriented unit/race/process coverage and executable CI gates.
6. Run the full checks and document delivery, verification and remaining limits.

## Changes

Implemented private saved authentication/configuration, scoped list/status,
persistent create/delete, domains, bounded recent logs and version output.
Managed-session no-port starts detect local services and create ephemeral tunnels;
orderly exit cleans up the owned record. Existing external-file defaults and
connection event contracts are preserved. Foreground runtime status/stop uses a
private numeric-loopback control endpoint with instance verification, bounded
admission/deadlines and joined shutdown; no PID signaling is used.

`doctor` performs read-only API identity/readiness and verified relay TLS checks.
The API client rejects malformed/oversized replies, redirects and unsafe bindings,
preserves full unsigned counter precision, and never retries mutations. Cached
sessions cannot follow a different API URL/trust selection. Configuration writes
are private, bounded and atomic. Human management lists/logs remain concise.

New Go failure/race coverage exercises private state, expiration, endpoint binding,
discovery ambiguity/cancellation, stale ownership, unauthorized stop, slow peers,
bounded responses and mutation non-replay. `pnpm test:cli` adds a real PostgreSQL,
Redis, API and relay suite to `make check` and CI. No REST/OpenAPI, database or
tunnel protocol change was needed. User authorization now includes committing
Phase 17 before starting Phase 18.

## Verification

- `make check` passed: Go tests/race/vet, Node unit/lint/types/format/build and all
  existing process, browser, security, load and chaos suites. New CLI integration
  passed 7/7 checks, including DNS TXT verification, read-only doctor and API-outage
  stop/logout. Logs: `/tmp/portway-phase17-check.log`.
- Focused final CLI/state/control race tests passed after final validation and
  human-output changes. Six fuzz targets passed with `make fuzz FUZZTIME=3s`.
  Prisma schema validation passed; no schema migration is introduced.
- Fresh isolated bootstrap passed 1/1 using copied source, shared installed
  dependencies, new fixture credentials and a separate disposable Compose project.
  Its temporary workspace and volume were removed. Log:
  `/tmp/portway-phase17-bootstrap.log`.
- Final formatting, diff checks and private-file preservation checks passed. All
  13 original configuration/private files matched their snapshot; the original
  PostgreSQL volume remained. No remote push or deployment is part of this commit.

## Risks

Credentials remain private and are never included in JSON output or arguments.
Logout revokes a managed session, not a deployment API key. Local stop must verify
an owned agent instance instead of signaling a saved PID. Detection must refuse
ambiguous services and never execute package scripts. Release artifacts, installer
and production deployment remain Phase 18 work. Existing development credentials
and volumes will be preserved; fresh isolated fixtures provide test credentials.
Saved sessions retain their existing one-hour maximum expiry. Discovery probes
numeric IPv4 loopback and a bounded candidate set; it never executes scripts or
guarantees detection of every framework/port. Runtime controls require a shared
private state directory and a selected/explicit tunnel ID. A refused stale
endpoint can be reclaimed; ambiguous live endpoints fail closed. After an
interrupted state mutation, an exclusive lock/orphan file may require manual
inspection. POSIX permissions were verified here; Windows needs account ACLs.
During API outages, local stop succeeds while ephemeral deletion/logout reports
unconfirmed remote cleanup. Persisted tunnel status is not live presence proof.
