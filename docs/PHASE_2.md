# Phase 2 — Authenticated Agent–Relay Connection

## Context and plan

The data-plane connection boundary follows [PROTOCOL.md](./PROTOCOL.md),
[ARCHITECTURE.md](./ARCHITECTURE.md), and [ROUTING.md](./ROUTING.md). Define strict
AUTH messages; add verified TLS dialing/listening; connect the HELLO/AUTH state
sequence to scoped expiring credentials; bound timeouts/concurrency and own
cleanup; expose the handshake through the CLI and development readiness; then
verify successful and hostile peers with real TLS, race checks, and fuzzing.

Agents connect outbound. Application bytes remain separate from the API/database.
This phase changes no durable schema or REST/OpenAPI semantics. The CLI JSON
interface is documented separately in [API.md](./API.md).

## Delivery

- Verified TLS 1.3 and ALPN `portway/1`; configured CA roots or system trust,
  hostname checks, no plaintext fallback, and no skipped certificate verification.
- Strict HELLO/HELLO_ACK then AUTH/AUTH_OK sequencing. Unexpected message types
  are rejected at the header before reading a body, including after authentication.
- AUTH payload validation, stable AUTH_ERROR codes, cryptographically random
  connection IDs, and validation of credential expiry in the agent ACK.
- A credential-verifier interface. The local implementation stores SHA-256 token
  hashes, verifies `connect` scope, rejects expired/revoked tokens, bounds records
  and file size, and snapshots policy. It never retains a raw token in relay policy.
- One deadline for TLS/HELLO/AUTH, bounded active idle reads and handshake writes,
  credential-expiration deadlines, and immediate socket closure on cancellation.
- Connection admission before goroutine creation, covering unauthenticated peers
  and active sessions. Shutdown closes the listener/connections and joins workers.
- Structured authentication/connection lifecycle logs with IDs and stable codes;
  custom verifier errors are redacted before reaching peers or logs.
- `portway connect --once` verifies a handshake. `portway connect` holds it;
  `portway <port>` also probes the local service. Human output is concise; JSON
  reports timestamped `relay_authenticated` events without claiming public readiness.
- Idempotent local certificate/credential generation through `make setup` and
  `make dev`. Readiness invokes the actual CLI and requires authenticated TLS.

## Development and configuration

```bash
make setup
make dev
# In another terminal:
go run ./cmd/portway connect --once
PORTWAY_JSON=1 go run ./cmd/portway connect --once
```

Files in `.tmp/dev` are ignored by Git: `ca.pem`, `relay-cert.pem`, `relay-key.pem`,
`agent-token`, and `relay-credentials.json`. Credentials expire after 24 hours;
certificates after seven days. Existing files are preserved. Stop development
and run `make dev-credentials` for explicit rotation. Partial fixtures require
explicit rotation; a lock prevents concurrent initializers.

The default development certificate covers localhost, 127.0.0.1, and ::1. No
system trust store is changed. The CA private key is discarded after signing.
Private files are mode 0600, in a mode-0700 directory. Credential-file permission
checks apply on POSIX; Windows deployments must use suitable filesystem ACLs.

The relay credential file is a JSON array using existing TunnelCredential field
meanings. It contains hashes only; this illustrative hash is not a credential:

```json
[
  {
    "tunnelId": "tnl_local_dev",
    "tokenHash": "<64 hexadecimal digits: SHA-256 of the random token>",
    "scope": "connect",
    "expiresAt": "2026-10-01T00:00:00Z"
  }
]
```

Optional `revokedAt` denies authentication. Token files contain 32–512 printable
ASCII bytes and may end with one newline. Records are capped at 1024 and their
file at 1 MiB. Issuance is intentionally a development fixture until Phase 9.
The verifier loads policy at startup; file edits require a relay restart.

| Setting                                                 | Default                                              | Behavior                                                |
| ------------------------------------------------------- | ---------------------------------------------------- | ------------------------------------------------------- |
| `RELAY_BIND_HOST` / `RELAY_PORT`                        | `127.0.0.1` / `8081`                                 | Listener address                                        |
| `RELAY_TLS_CERT_FILE` / `RELAY_TLS_KEY_FILE`            | `.tmp/dev/relay-cert.pem` / `.tmp/dev/relay-key.pem` | Relay identity                                          |
| `RELAY_CREDENTIALS_FILE`                                | `.tmp/dev/relay-credentials.json`                    | Hashed credential policy                                |
| `RELAY_MAX_CONNECTIONS`                                 | `128`                                                | Includes stalled/unauthenticated peers; allowed 1–10000 |
| `RELAY_MAX_FRAME_BYTES`                                 | `4194304`                                            | Session limit, 4096–4194304                             |
| `RELAY_HANDSHAKE_TIMEOUT`                               | `10s`                                                | Whole TLS/HELLO/AUTH deadline                           |
| `RELAY_IDLE_TIMEOUT` / `RELAY_WRITE_TIMEOUT`            | `120s` / `5s`                                        | Idle reads / writes                                     |
| `PORTWAY_RELAY_ADDR`                                    | `127.0.0.1:<RELAY_PORT>`                             | Agent destination                                       |
| `PORTWAY_RELAY_SERVER_NAME`                             | Destination host                                     | Verified certificate name                               |
| `PORTWAY_RELAY_CA_FILE`                                 | `.tmp/dev/ca.pem`                                    | CA bundle; empty means system roots                     |
| `PORTWAY_TOKEN_FILE`                                    | `.tmp/dev/agent-token`                               | Private raw credential file                             |
| `PORTWAY_CONNECT_TIMEOUT` / `PORTWAY_HANDSHAKE_TIMEOUT` | `10s` / `10s`                                        | Dial/TLS bound / whole connection handshake             |
| `PORTWAY_IDLE_TIMEOUT` / `PORTWAY_WRITE_TIMEOUT`        | `120s` / `5s`                                        | Idle reads / writes                                     |

Environment durations must be positive and no more than five minutes. Configuration
is immutable while a client/server is running. No capability is advertised in
Phase 2 because multiplexing, flow control, heartbeat, and draining are later work.

Development scripts load `.env`; standalone Go binaries use shell environment.
Set `PORTWAY_RELAY_ADDR` explicitly for a custom-port CLI invocation outside the
development supervisor.

## Acceptance

```bash
make setup
make check
make fuzz
pnpm test:bootstrap
```

Tests use real TLS sockets and pipe-backed TLS to cover authentication, trust and
hostname failures, ALPN/TLS-version failures, malformed messages, wrong sequence,
expired/revoked credentials, slow handshakes/readers, capacity exhaustion, peer
disappearance, cancellation, expiry without an agent timer, shutdown, and a fresh
authenticated reconnect. CLI tests verify JSON/exit codes and secret redaction.
Fuzzing covers all five handshake payload types as well as arbitrary framing.

## Verified locally

Verified on 2026-09-30 with the pinned toolchains:

- `make setup` passed frozen dependency installation, Prisma generation, and
  idempotent private development fixture generation.
- `make check` passed formatting, Go and TypeScript tests, Go race detection,
  vet, lint, type checks, and production builds.
- `make fuzz` passed all three ten-second targets with 666,208 inputs across
  frame decoding, encoder round trips, and all five handshake payload codecs.
- `pnpm test:bootstrap` passed real API/dashboard/dependency startup, the CLI's
  authenticated TLS readiness probe, duplicate-start rejection, and interrupt
  cleanup. No Portway development containers or application listeners remained.
- Git ignores the raw credential, relay private key, and local policy files.

CI is configured to run the same checks. These results are local; the hosted
workflow has not run here.

## Limits and next phase

Phase 2 authenticates a connection but assigns no hostname/public URL and moves
no application bytes. The existing registry remains a Phase 3 scaffold. The
next phase binds the authenticated credential identity to a tunnel, validates
generation ownership, and implements REGISTER/REGISTER_OK.

Durable credential issuance and live revocation propagation are Phase 9 work.
Heartbeat/reconnect policy and GOAWAY draining remain Phases 6–7. A quiet Phase 2
session ends on idle timeout. Connection shutdown here closes sockets immediately;
it does not promise stream draining, since streams do not exist yet.
