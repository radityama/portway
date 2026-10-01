# Phase 4 — HTTP Data Plane

Phase 4 connects a public HTTPS request to the HTTP service on the agent's chosen
loopback port. Phase 3 registration ownership remains authoritative. The agent
connects outbound; public traffic uses relay-local state and never calls the API,
PostgreSQL, or Redis on the request path. No durable schema, REST endpoint, or
dependency changes are required.

## Implementation plan and delivery

1. Extend the v1 contract with validated HTTP stream metadata and bounded stream
   control payloads, keeping Go/TypeScript compatibility fixtures.
2. Add one reader and serialized, bounded writes per tunnel connection, monotonic
   stream IDs, admission limits, half-close, reset, and owned cancellation.
3. Attach HTTP registrations to public TLS ingress and validate Host/SNI before
   selecting the active owner.
4. Dial the agent's fixed numeric loopback endpoint and stream HTTP requests and
   responses with header/body limits.
5. Expose the assigned URL and actual forwarding readiness in the CLI, verify
   public TLS during development startup, and test failure paths.

All five steps are implemented. The contracts are documented in
[PROTOCOL.md](./PROTOCOL.md), [ROUTING.md](./ROUTING.md),
[ARCHITECTURE.md](./ARCHITECTURE.md), and the CLI section of [API.md](./API.md).
REST/OpenAPI semantics and the Prisma schema are unchanged.

## Run the acceptance path

```bash
make setup
make dev
```

In another terminal, with an HTTP application already listening on port 3000:

```bash
go run ./cmd/portway 3000
# Or emit newline-delimited JSON:
PORTWAY_JSON=1 go run ./cmd/portway 3000
```

After registration, the CLI reports `tunnel_connected`, `public_url`, and `ready`
when its stream receiver is configured. The URL is validated against the assigned
hostname. Diagnostic `connect` and `register` do not enable forwarding.
Readiness does not guarantee the local application remains available afterward.

The default development tunnel is `tnl_local_dev`. Test its printed URL with:

```bash
portway_host=p-9cbd6a472b3507c67e259cadf5a9ecc5.portway.localhost
curl --cacert .tmp/dev/public-ca.pem \
  --resolve "$portway_host:8443:127.0.0.1" \
  "https://$portway_host:8443/"
```

Use the printed hostname/port for nondefault settings. `make setup` adds
`public-ca.pem`, `public-cert.pem`, and `public-key.pem` under ignored `.tmp/dev`
without rotating existing agent credentials. The public certificate covers
`*.portway.localhost`; it has a separate development root. Neither root is
installed into system trust. `make dev-credentials` rotates both certificate
sets and the development credential; stop and restart the relay after rotation.

## Configuration and bounds

Standalone Go commands use shell environment; `make dev` also loads `.env`.

| Setting                  | Default                    | Meaning                                                    |
| ------------------------ | -------------------------- | ---------------------------------------------------------- |
| `PUBLIC_BIND_HOST`       | `127.0.0.1`                | Public listener IP; expose explicitly for remote clients   |
| `PUBLIC_PORT`            | `8443`                     | HTTPS listener and URL port; must differ from `RELAY_PORT` |
| `PUBLIC_TLS_CERT_FILE`   | `.tmp/dev/public-cert.pem` | Certificate covering assigned hostnames                    |
| `PUBLIC_TLS_KEY_FILE`    | `.tmp/dev/public-key.pem`  | Private public TLS key                                     |
| `PUBLIC_TLS_CA_FILE`     | `.tmp/dev/public-ca.pem`   | Optional development readiness trust override              |
| `PUBLIC_MAX_CONNECTIONS` | `128`                      | Admission before public TLS/HTTP worker creation           |
| `RELAY_MAX_STREAMS`      | `32`                       | Maximum active streams per relay tunnel connection         |
| `PORTWAY_MAX_STREAMS`    | `32`                       | Maximum accepted streams per agent connection              |
| `RELAY_STREAM_TIMEOUT`   | `30s`                      | Whole public request/response deadline                     |
| `PORTWAY_STREAM_TIMEOUT` | `30s`                      | Whole local forwarding deadline                            |

Stream limits allow 1–1024; connection limits allow 1–10000. Environment
durations must be positive and at most five minutes. Existing tunnel connection,
frame, registry, credential-expiry, idle, and write limits still apply. The
development certificate only covers the default base domain; operators supply
appropriate DNS and certificates when changing it. Port 443 is omitted from the
assigned URL; other ports are explicit. Public TLS requires version 1.3 and uses
HTTP/1.1 with a five-second TLS/header deadline.

OPEN metadata is capped at 64 KiB before allocation and contains at most 128
header pairs and 32 KiB total HTTP metadata. Request targets are origin-form and
at most 8192 bytes. DATA frames are at most 16 KiB or the negotiated frame cap,
whichever is smaller; other stream controls are capped at 4096 bytes. Request
bodies are limited to 16 MiB, responses to 64 MiB. Bodies are streamed rather than
collected in memory. The HTTP parser has Go's small fixed read allowance beyond
the configured header limit; forwarded metadata still obeys the stricter cap.

Host/SNI must match. The local Host is the assigned hostname without a public
port; forwarding headers are rebuilt from the HTTPS authority and immediate
client IP. Hop-by-hop headers and proxy credentials are removed. Application
Authorization/Cookie values are forwarded without logging. The local connection
serves a single HTTP request. Redirects are returned, and repeated response
headers such as Set-Cookie are preserved.

## Lifecycle and failure behavior

Only the relay allocates stream IDs, increasing monotonically without reuse.
The agent acknowledges OPEN only after dialing its fixed loopback service.
FIN half-closes one direction; RESET cancels both. Local sockets, body writers,
and stream pipes close on request cancellation, timeout, tunnel expiry,
replacement, or shutdown, and owned workers are joined. New ownership becomes
routable after its registration ACK; old cleanup cannot remove the replacement.
Requests interrupted by a lost owner fail without replay.

Unknown hosts return 404, offline/diagnostic routes 503, SNI mismatches 421,
stream capacity 503, local dial/invalid response failures 502, deadlines 504,
oversized requests 413, and oversized metadata 431. CONNECT returns 405;
upgrades return 501; trailers are rejected. Body failures after response headers
have been sent abort the response instead of changing its status.

Tests exercise authenticated TLS end to end through a real local HTTP service:
method/path/query preservation, application headers, forwarding-header policy,
repeated cookies, multi-frame bodies, HEAD and redirects, responses arriving before completion,
concurrent requests, generation replacement, invalid routing, unavailable local
services, timeouts, client cancellation, capacity, and body/header limits. CLI
tests request the actual HTTPS URL from its `ready` event. Multiplexer tests cover
partial stream delivery, half-close/reset, malformed state, and cancellation of
a blocked reader. Shared payload fixtures and a stream-payload fuzzer cover the
wire contract. Public certificate tests cover upgrade preservation and wildcard
trust; Docker bootstrap tests cover public TLS readiness and process cleanup.

## Verification

Local verification on 2026-09-30 used Go 1.26.8, Node.js 24.19.0, pnpm 10.12.1,
and Docker 28.4.0:

- `make setup` passed with a frozen dependency install, Prisma generation, and
  idempotent public certificate setup.
- `make check` passed formatting, Go and TypeScript tests, Go race detection/vet,
  lint, type checks, and production builds.
- `make fuzz` passed all four targets at 10 seconds each: 894,246 executions
  across frame decoding, frame round trips, handshakes, and HTTP stream payloads.
- `pnpm test:bootstrap` passed real Docker-backed startup, registration/public
  TLS readiness, duplicate-start rejection, and interrupt cleanup.
- After the final HTTP parser correction, `go test -race ./...` and
  `go vet ./...` passed again. The correction rejects unsupported response
  trailers without draining a slow untrusted body. Final Go binaries were rebuilt
  after preserving remote stream rejection codes across cancellation races.
- HEAD/redirect semantics, asynchronous public-slot release, and active owner
  replacement passed five repeated runs under the race detector. A multiplexer
  test verifies that repeated remote capacity rejections retain their stable code.
- A standalone CLI/relay/local-service smoke check used curl with CA verification
  and explicit hostname resolution; the CLI's ready URL returned the exact local
  response. The test processes were stopped afterward.

These are local results. GitHub-hosted CI has not been run for the uncommitted
Phase 4 changes.

## Remaining limits and Phase 5

Synchronous pipe delivery retains at most one decoded DATA payload per tunnel
reader and has no growing receive queue. A slow stream can block that reader and
delay other streams on the connection until consumption, cancellation, or its
deadline. Phase 5 adds independent receive/send windows, WINDOW_UPDATE, and
per-stream/connection buffer budgets. This phase does not advertise credit-window
support. Throughput and load validation remain later work.

HTTP/2, WebSockets/upgrades, trailers, custom Host rewriting, transparent replay,
and automatic reconnect are not enabled. Control-plane issuance, live credential
revocation propagation, distributed ownership, custom domains, and dashboards
remain in their planned phases.
