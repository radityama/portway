# Portway security boundaries

## Threat model

The relay's public HTTPS listener accepts untrusted visitors. Tunnel sockets
accept untrusted peers until TLS, protocol negotiation and AUTH succeed. A valid
agent credential grants one tunnel identity and issued generation, never another
tunnel or an arbitrary upstream destination. API users may be hostile tenants;
authentication must not grant access to another organization's resources. Relay
operator keys are a separate authority from user API keys and sessions.

The agent connects outbound and dials only its explicitly configured numeric
loopback service. It uses a separate upstream socket for each HTTP request and
returns redirects without following them. The relay checks canonical Host,
configured port and TLS SNI before local routing. Absolute/authority targets and
CONNECT cannot select an upstream. Application Authorization and Cookie headers
can reach that fixed service; proxy credentials, hop headers and visitor-supplied
Forwarded/X-Forwarded fields are removed. Forwarding metadata is rebuilt locally.

Go's HTTP parser selects chunked framing when both Content-Length and
Transfer-Encoding are present and collapses identical repeated Content-Length
values. Portway transports the parsed body and serializes fresh upstream framing;
it does not forward the supplied framing headers or concatenate requests onto an
upstream keep-alive socket. Conflicting lengths, repeated/unsupported transfer
encodings and declared trailers are rejected. Upstream headers use a separate
bounded parser; unsolicited upgrades and excessive informational responses fail.

The relay routes and expires admitted leases locally. PostgreSQL/Redis/API calls
are confined to control operations and periodic reporting. An API outage cannot
turn authentication into a successful fallback or add a dependency to each
application request. New AUTH checks parent key/session, membership, assignment,
generation and credential validity. OWNER, ADMIN and MEMBER are the explicit
parent roles that permit connection; every other role fails closed. PostgreSQL
also constrains membership roles. Phase 15 tests AUTH independently with deliberate
constraint drift in an owned disposable database, then restores the constraint.

## Resource and secret handling

- Admission bounds public and tunnel sockets before connection workers start.
  Public TLS/header reads expire after five seconds. Per-tunnel stream limits,
  stream/application idle deadlines, frame bounds and independent byte-credit
  windows limit slow peers and keep other streams progressing. Shutdown closes
  sockets and joins owned workers.
- HTTP metadata is bounded to 32 KiB/128 forwarded header pairs, request bodies
  to 16 MiB and response bodies to 64 MiB. Raw public parsing also has a bounded
  net/http header buffer; its parser allowance is separate from the stricter
  forwarded metadata limit. Bodies stream through bounded buffers.
- The API caps JSON at 64 KiB, active requests/sockets at 128 and uses bounded
  local rate buckets. Header, body, request, database and shutdown deadlines are
  explicit. Duplicate JSON keys, including escaped aliases, are rejected.
- Go token/policy files are bounded private regular files. The final path
  component cannot be a symlink; opened identity/type/size/permissions are checked.
  Keep directories controlled by the service account. These checks do not isolate
  a hostile local account that can modify the same directory; use appropriate
  filesystem permissions and Windows ACLs. Static relay policy requires restart.
- TLS validates relay certificates and hostnames, requires TLS 1.3 and negotiated
  ALPN, and never falls back to plaintext or disabled verification. Server identity
  and trust roots remain operator responsibilities.
- Persisted credentials contain hashes; one-time secrets are not retained in
  idempotent responses. Request logs and observations omit targets, headers,
  bodies and raw peer errors. Metrics use finite method/status labels. Operator
  scrapes are loopback-only or require operator authentication.
- The dashboard uses encrypted HttpOnly sessions, mutation-origin/JSON checks,
  an exact BFF route/query allowlist and bounded upstream requests. Its server
  does not follow API redirects; logout and parent revocation invalidate sessions.

## Phase 15 verification matrix

| Requirement              | Executable evidence                                                                                                                                                                                                                                          |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Expired credentials      | `internal/relay/server_test.go`: `TestInvalidExpiredAndRevokedCredentials`; production API security integration tests credential expiry                                                                                                                      |
| Revoked credentials      | Same Go test; production API tests parent logout, dependent leases and denied idempotent replays                                                                                                                                                             |
| Malformed frames         | `server_test.go`: `TestStateSequenceRejectsBodiesBeforeReading`, `TestVersionAndMalformedAuthRejection`, `TestConfiguredFrameLimitIsEnforced`; protocol malformed/state tests and fuzz targets                                                               |
| Hostname injection       | `security_test.go`: `TestSecurityRawHTTPSRejectsUnsafeRequestsBeforeUpstream` verifies rejected requests never reach the local service                                                                                                                       |
| Oversized headers        | Same raw HTTPS matrix; `TestSecurityHostileUpstreamHeadersDoNotPoisonTunnel` exercises agent and relay parsers over real sockets                                                                                                                             |
| Oversized bodies         | `http_test.go`: `TestHTTPBodyAndHeaderLimits` covers declared/chunked request and declared response limits; raw matrix rejects declared oversize before upstream work                                                                                        |
| Connection exhaustion    | `TestSecuritySlowTLSAndHeadersReleasePublicAdmission` verifies filled admission, rejection, deadlines and recovery; `server_test.go`: `TestConnectionCapacityAndShutdown`                                                                                    |
| Stream exhaustion        | `http_test.go`: `TestHTTPTimeoutCancellationAndStreamCapacity`; WebSocket capacity and mux credit tests                                                                                                                                                      |
| Slow clients             | Raw TLS/partial-header timeout test; production API partial chunked upload returns 408 without allocating a project; existing slow-reader/forced-shutdown WebSocket tests                                                                                    |
| Slow upstream            | `TestHTTPTimeoutCancellationAndStreamCapacity`, `TestSlowLocalUploadAllowsAnotherHTTPRequest`, SSE and independent stream/flow-control tests                                                                                                                 |
| Host-header edge cases   | Raw duplicate Host, SNI mismatch, wrong/noncanonical ports and target forms; `TestCanonicalAuthorityRejectsInjectionAndWrongPorts`; metadata/forwarding-header policy integration                                                                            |
| Authorization violations | `scripts/tests/security.integration.mjs`: real PostgreSQL/Redis/production API checks foreign reads/actions/observations, viewer mutations, user/operator separation, policy drift, session and parent revocation; existing domain/DNS and browser/BFF tests |

`TestSecurityAmbiguousFramingIsCanonicalizedWithoutSmuggling` sends HTTP-looking
body content followed by a legitimate pipelined request. It verifies exact bodies,
exact request count, canonical framing and distinct local sockets. Healthy
requests after hostile input verify that failures do not poison the tunnel.
`TestCredentialFilesRejectSymlinks` and the parent-role tests cover the Phase 15
hardening changes directly. The production API suite also checks that its process
output contains no issued secrets or injected body values.

Run `make security-integration` for the focused Go/production API suite, `make
check` for all required gates and `make fuzz FUZZTIME=5s` for bounded fuzz campaigns.
Docker fixtures own temporary PostgreSQL/Redis containers and remove them on exit.
No production data or development secret files are used by those tests.

## Current scope

Application visitors are public unless the exposed application authenticates
them. Public hostname entropy is not authentication. Public per-IP/application
token-bucket policies, OIDC and distributed abuse prevention are not implemented
by this phase; existing socket/stream/byte/deadline limits bound resource use.
Keep the control API/dashboard and metrics behind their documented deployment
boundaries and keep private files out of source control.

Revocation blocks future AUTH. Already admitted leases remain usable until local
expiry, supersession, transport failure or shutdown; there is no active revocation
push. Credential theft within that lease still requires rotation/revocation and
operational response. This test suite is regression coverage for these boundaries,
not a claim of an external penetration audit. [Phase 16](./PHASE_16.md) exercises
infrastructure failures, bounded sustained concurrency, real TCP loss and
reconnect storms. Redis-dependent assignments fail without allocating credentials;
stale relay incarnations drain; interrupted mutations are canceled without replay.
