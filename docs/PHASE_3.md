# Phase 3 — Tunnel Registration

## Context and plan

Extend the authenticated agent–relay boundary from [Phase 2](./PHASE_2.md).
Define strict registration payloads in [PROTOCOL.md](./PROTOCOL.md), bind tunnel
identity to credential scope, assign canonical hostnames, replace owners by
generation, retain bounded watermarks, add local generation reservations and CLI
events, then verify hostile peers and lifecycle cleanup.

The architecture and routing contracts remain authoritative. Agents dial outbound;
hostname ownership is in relay memory, with no API/PostgreSQL lookup or new durable
schema. REST/OpenAPI semantics are unchanged; the CLI contract is in [API.md](./API.md).

## Delivery

- Strict REGISTER/REGISTER_OK/REGISTER_ERROR, capped at 4096 bytes, with exact
  required fields and malformed/duplicate/unknown/null/trailing JSON rejection.
- Case-sensitive tunnel IDs matching the authenticated credential. Agents cannot
  claim another tunnel or request a hostname.
- Full-width uint64 generations encoded as canonical decimal strings, shared
  Go/TypeScript fixtures and active fuzz coverage.
- Stable hostnames derived from SHA-256(tunnel ID) under an operator base domain;
  collisions fail closed, without using hostname entropy as authentication.
- Atomic strictly newer generation replacement, immediate old-socket closure,
  owner-aware cleanup, and watermarks retained after disconnect or failed ACK.
- Bounded registry state; new IDs fail at capacity, while known IDs can advance.
- Canonical hostname lookup returning an immutable active-owner snapshot, excluding
  disconnected, cancelled or expired owners.
- Registration/read/write/expiry deadlines and session cancellation; no reader
  sharing between Register and Wait, and no retry after partial failed exchanges.
- Private per-tunnel CLI counters with exclusive start locks, bounded reads,
  atomic file replacement, corruption/overflow checks and recovery overrides.
- `portway register [--once]` and numeric-port registration. JSON emits the actual
  `tunnel_registered` milestone without claiming public forwarding readiness.
- Development readiness exercises TLS authentication and registration via the CLI.

## Use

```bash
make setup
make dev
# In another terminal:
go run ./cmd/portway register
PORTWAY_JSON=1 go run ./cmd/portway register --once
# With a local service listening:
go run ./cmd/portway 3000
```

`connect --once` remains an authentication diagnostic. `register --once` removes
its route when it exits. A held registration is routable metadata until it ends;
Phase 3 has no public HTTP listener, forwarding, or stream support.

Standalone Go commands read shell environment; development scripts load `.env`.
Set `PORTWAY_RELAY_ADDR` for a custom relay port outside the development supervisor.

| Setting                        | Default             | Meaning                                                 |
| ------------------------------ | ------------------- | ------------------------------------------------------- |
| `PUBLIC_BASE_DOMAIN`           | `portway.localhost` | Canonical lowercase DNS base, at most 218 bytes         |
| `RELAY_MAX_TUNNELS`            | `1024`              | Retained identities/watermarks, allowed 1–100000        |
| `RELAY_REGISTRATION_TIMEOUT`   | `10s`               | Deadline after AUTH_OK for one REGISTER                 |
| `PORTWAY_TUNNEL_ID`            | `tnl_local_dev`     | Must match the credential's tunnel ID                   |
| `PORTWAY_STATE_DIR`            | `.tmp/agent-state`  | Private generation files, retained across starts        |
| `PORTWAY_GENERATION`           | unset               | Explicit recovery generation; also persists the counter |
| `PORTWAY_REGISTRATION_TIMEOUT` | `10s`               | Client REGISTER/ACK exchange deadline                   |

The Phase 2 connection/frame/timeouts still apply. Environment durations are
positive and at most five minutes. Hostname lookup deliberately accepts a canonical
hostname, not raw HTTP Host/SNI input; ingress parsing is Phase 4.

## Ownership and recovery

Once generation 11 replaces 10, session 10 is closed and cannot remove or overwrite 11. Equal generations fail. Failed ACK writes release routing but retain generation
11, so a retry must use 12 or higher. Numbers may have gaps and never wrap.

Relay watermarks are ephemeral but are not evicted within a process lifetime.
Deleting tombstones on disconnect or expiry would let old sessions reclaim a tunnel.
New identities stop at the configured capacity. Changing credentials or base domain
requires relay restart; restart clears registry state. Persistent/multi-relay fencing
and domain allocation remain later phases.

Generation files are mode 0600 in a private mode-0700 directory on POSIX. Windows
operators must set suitable ACLs. A lock rejects concurrent reservations; a crashed
process may leave a lock file. Remove that lock only after confirming the owning
process has stopped. Corrupt or overflowed counters fail rather than resetting.
Missing local state begins at one, which the relay will reject if already accepted.
Use an operator-selected `PORTWAY_GENERATION` above both counters for recovery, then
unset it. Separate machines sharing a tunnel need coordinated generation state;
automatic reconnect/increment policy is Phase 6. Do not delete state to retry.

## Acceptance

```bash
make check
make fuzz
pnpm test:bootstrap
```

Unit and real-TLS integration tests cover successful hostname resolution, credential
scope, maximum uint64 generations, stale/equal rejection, replacement and late
cleanup, concurrent registrations, offline watermarks, capacity, malformed/wrong
sequence/oversized inputs, hostile ACK bindings, disappearance, slow peers, failed
ACK writes, timeout, cancellation, idle expiry, credential expiry and shutdown.
CLI tests cover persisted increments, numeric-port registration, stable JSON events,
clean interruption, failure codes and secret redaction. TypeScript reads the same
registration wire fixtures without precision loss. Fuzzing includes all eight
implemented connection payload codecs.

## Verified locally

Verified on 2026-09-30 with the pinned toolchains:

- `make check` passed formatting, all Go/TypeScript tests, Go race detection,
  vet, ESLint, type checks and production builds.
- `make fuzz` passed the three ten-second targets with 814,067 inputs, including
  all eight implemented connection payload codecs.
- `pnpm test:bootstrap` passed real API/dashboard/dependency startup, authenticated
  registration readiness, duplicate-start rejection and interrupt cleanup.
  No Portway development containers remained after shutdown.
- The complete Phase 2 bundle was verified and cloned in a separate checkout;
  it contains history through `762c412`, with a clean checkout. Phase 3 changes
  are not included in that checkpoint bundle.
- Git ignores local generation state, credentials and private keys.

Hosted CI has not run in this workspace. Public HTTP acceptance belongs to Phase 4;
heartbeat, reconnect and GOAWAY draining remain Phases 6–7.
