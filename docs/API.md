# Portway — API Contract

## 1. API Principles

Base path:

```text
/api/v1
```

Authentication:

```http
Authorization: Bearer <token>
```

All mutation endpoints SHOULD accept:

```http
Idempotency-Key: <unique-key>
```

## 2. Response Envelope

Success:

```json
{
  "data": {},
  "error": null,
  "meta": {}
}
```

Error:

```json
{
  "data": null,
  "error": {
    "code": "TUNNEL_NOT_FOUND",
    "message": "Tunnel not found"
  },
  "meta": {}
}
```

## 3. Error Codes

```text
AUTH_INVALID
AUTH_EXPIRED
AUTH_REVOKED
FORBIDDEN
VALIDATION_ERROR
RATE_LIMITED

TUNNEL_NOT_FOUND
TUNNEL_ALREADY_CONNECTED
TUNNEL_REVOKED
TUNNEL_INVALID_STATE

RELAY_UNAVAILABLE
RELAY_OVERLOADED
RELAY_DRAINING

DOMAIN_NOT_FOUND
DOMAIN_ALREADY_ASSIGNED
DOMAIN_VERIFICATION_REQUIRED

PAYLOAD_TOO_LARGE
STREAM_LIMIT_REACHED
REQUEST_TIMEOUT
INTERNAL_ERROR
```

## 4. Authentication API

### POST /auth/login

Start or complete user authentication.

### POST /auth/logout

Revoke the active session.

### GET /me

Return current user/account context.

Example:

```json
{
  "data": {
    "user": {
      "id": "usr_01J...",
      "email": "dev@example.com",
      "displayName": "Developer"
    }
  },
  "error": null,
  "meta": {}
}
```

## 5. Projects

### GET /projects

List projects visible to the caller.

### POST /projects

```json
{
  "name": "My App",
  "slug": "my-app"
}
```

### GET /projects/:id

Get one project.

### DELETE /projects/:id

Delete a project if policy allows.

## 6. Tunnels

### GET /tunnels

Optional filters:

```text
projectId
status
relayId
```

### POST /tunnels

Create an ephemeral or persistent tunnel record.

Request:

```json
{
  "projectId": "prj_01J...",
  "name": "local-api",
  "type": "PERSISTENT",
  "protocol": "http",
  "localHost": "127.0.0.1",
  "localPort": 3000
}
```

Response:

```json
{
  "data": {
    "tunnel": {
      "id": "tnl_01J...",
      "status": "CREATED",
      "type": "PERSISTENT",
      "publicHostname": "abc123.portway.example.com"
    }
  },
  "error": null,
  "meta": {}
}
```

### GET /tunnels/:id

Return current durable tunnel metadata.

### DELETE /tunnels/:id

Delete/revoke the tunnel according to lifecycle rules.

### POST /tunnels/:id/revoke

Immediately revoke credentials and prevent future connection registration.

### POST /tunnels/:id/connect

Optional orchestration endpoint for persistent-tunnel workflows. It should not carry application traffic.

## 7. Relay Selection

### POST /tunnels/:id/connect

The control plane SHOULD return a relay assignment such as:

```json
{
  "data": {
    "relay": {
      "id": "rel_01J...",
      "hostname": "relay-jkt-01.portway.example.com",
      "port": 443,
      "protocol": "tls"
    },
    "credential": {
      "expiresAt": "2026-09-30T15:00:00Z"
    }
  },
  "error": null,
  "meta": {}
}
```

The raw credential value should only be returned over a secure authenticated channel and should not be persisted in logs.

## 8. Domains

### GET /domains

List domains available to the caller.

### POST /domains

```json
{
  "hostname": "app.example.com",
  "tunnelId": "tnl_01J..."
}
```

### DELETE /domains/:id

Disable the association while preserving its hostname reservation and history.

Ownership requires the one-time DNS TXT challenge. Read, challenge, verify and
activate endpoints and their full lifecycle are defined in section 19.

## 9. Relays

### GET /relays

Return operator-visible relay metadata.

### GET /relays/:id

Return status/capacity information.

Relay health endpoints should be separate from the public API:

```text
GET /health
GET /ready
```

## 10. Logs and Metrics

### GET /tunnels/:id/logs

Return metadata-level request logs subject to retention policy.

Sensitive data must be redacted.

### GET /tunnels/:id/metrics

Return aggregated tunnel metrics or a dashboard-friendly projection.

## 11. CLI JSON Event Contract

CLI JSON is a separate interface from the control-plane REST API.

Every event should contain at least:

```json
{
  "event": "ready",
  "timestamp": "2026-09-30T14:00:00+07:00"
}
```

Recommended events:

```text
starting
server_starting
server_ready
tunnel_connecting
tunnel_authenticating
tunnel_registered
tunnel_connected
public_url
reconnect_scheduled
tunnel_disconnected
shutdown_started
shutdown_complete
error
```

## 12.1 CLI Exit Codes

Suggested:

```text
0 success
1 generic failure
2 usage/configuration error
3 authentication failure
4 local service unavailable
5 relay/control-plane unavailable
6 interrupted by user
```

Keep exit codes stable for automation.

### Current CLI connection, registration, and forwarding milestones (Phases 2–7)

`portway connect --once` verifies one authenticated relay handshake and exits.
`portway connect` holds an unregistered diagnostic connection until interruption,
registration timeout, expiry, or peer closure. `portway register [--once]`
authenticates and registers the configured tunnel. `portway <port>` also checks
the configured local service before authentication/registration, requests HTTP
forwarding, and runs the stream receiver after REGISTER_OK assigns a hostname
and HTTPS URL. The local upstream is fixed to the selected loopback port.
Phase 5 HTTP forwarding requires negotiated multiplexing and flow control;
diagnostic authentication/registration remains available to older peers. JSON
event shapes and exit codes are unchanged by the flow-control implementation.

Human output is concise. `PORTWAY_JSON=1` emits only newline-delimited JSON on
stdout: `starting`, `tunnel_connecting`, `relay_authenticated`, and (for a held
connection) `shutdown_complete` or `error`. Each event includes an RFC3339
`timestamp`. `relay_authenticated` includes `connection_id` and `relay`; it never
includes credentials. Successful registration emits `tunnel_registered` with
`tunnel_id`, `connection_id`, `generation` (decimal string), `public_hostname`,
and `relay`; port invocations also include `local_url` and `port`. Once the HTTP
receiver is configured, a port invocation emits `tunnel_connected` with
`connection_id` and `relay`, `public_url` with `url`, and `ready` with
`public_url`, `local_url`, and `port`. The URL comes from the verified relay's
registration ACK and must match the assigned hostname. These events describe
forwarding readiness, not a promise that the local application stays available.
Diagnostic `connect` and `register` commands do not emit forwarding readiness.
`register --once` exits immediately after the ACK and closes/removes its route;
it is a diagnostic command. Held registration closes on cancellation, replacement,
idle timeout, expiry, or peer closure. Registration errors use stable protocol
codes in the error event without echoing payloads. Local generations persist in
`PORTWAY_STATE_DIR`, scoped by `PORTWAY_TUNNEL_ID`; explicit recovery generations
also update the local counter.

Existing implemented exit codes are preserved: `0` for success/clean shutdown,
`1` for connection/authentication/registration/forwarding/local-service failures, and `2` for invalid
command usage. The broader suggested codes above remain a future CLI change.
This milestone changes the CLI interface only; REST/OpenAPI semantics are unchanged.

Phase 6 port invocations reconnect after transient transport/heartbeat failure.
`tunnel_disconnected` includes `connection_id`, `relay`, and a stable `reason`
(`transport_closed`, `heartbeat_timeout`, or `transport_timeout`).
`reconnect_scheduled` includes `relay`, `attempt` (consecutive retry number),
`delay_ms` (positive integer), and the same reason. A failed initial dial emits
only the scheduling event. Each new successful session repeats authentication,
registration and forwarding readiness events with a new connection ID and
higher generation; the assigned URL stays stable. Backoff has equal jitter,
a 1s initial base and 30s maximum base, resetting after 60s connected.
Ctrl+C cancels connection work or backoff and emits `shutdown_complete` with
exit 0. Terminal failures emit `error` and exit 1 without retries. Diagnostics
remain one-session commands. No request is replayed and no secret/raw network
error is included in reconnect events.

Phase 7 adds `shutdown_started` for local signal shutdown and `tunnel_draining`
for peer GOAWAY, each with `relay` and `reason` (`user_shutdown` or
`peer_shutdown`), plus `connection_id` when a session exists. Handshake/backoff
shutdown can omit the connection ID. Local shutdown stops reconnect, waits for admitted work up
to PORTWAY_SHUTDOWN_TIMEOUT (10s by default), then emits `shutdown_complete` and
exits 0, including forced cleanup at the deadline. Cancellation during handshake
or backoff remains prompt. Remote drain waits for active work, then schedules
reconnect with reason `relay_draining`; REGISTER_DRAINING is also retryable.
Diagnostic registrations drain their connection without reconnecting. Existing
exit codes remain unchanged. These are CLI/protocol changes; REST is unchanged.

Phase 8 keeps CLI commands, JSON events and exit codes unchanged. Negotiated
WebSocket and streaming requests use application idle timeouts configured by
PORTWAY_STREAM_TIMEOUT and RELAY_STREAM_TIMEOUT (30s by default, positive and at
most 5m), while older peers retain whole-request timeouts. Application events or
WebSocket ping/pong must occur within that interval. This data-plane change adds
no REST operation; the OpenAPI contract remains unchanged.

## 13. Pagination

Collection APIs should support cursor pagination:

```text
?cursor=<opaque>&limit=50
```

Response:

```json
{
  "data": [],
  "error": null,
  "meta": {
    "nextCursor": "..."
  }
}
```

## 14. Validation

Reject unknown or invalid values early.

Validation MUST exist for:

- IDs
- hostnames
- ports
- protocol names
- tunnel types
- request sizes
- pagination values

## 15. Authentication and Authorization

Authentication establishes identity.

Authorization determines whether the caller can operate on the target resource.

All resource endpoints MUST enforce organization/project/tunnel scope.

## 16. API Versioning

The initial contract uses:

```text
/v1
```

Breaking changes require a new version or a formally documented compatibility strategy.

## 14. Implemented Phase 9 contract

Phase 9 implements the auth, projects, tunnels and read-only relays endpoints
above. The API uses a bounded process-local store loaded from optional private
API_SEED_FILE JSON. State is lost at API restart; this is the pre-database boundary,
not durable persistence. PostgreSQL/migrations are Phase 10; dynamic relay
registration, health, capacity and failover are Phase 11. Phase 12 implements
domains as specified in section 19. Logs/metrics remain later endpoints and return
the normal NOT_IMPLEMENTED envelope.

Authentication uses deployment-provisioned API keys (hashes only in the seed),
with userId, organizationId, expiresAt and revokedAt. An active matching membership
is required. Roles OWNER/ADMIN/MEMBER can mutate organization projects/tunnels;
VIEWER can read. Project deletion requires OWNER/ADMIN and no child tunnels.
Cross-organization resources return their normal *_NOT_FOUND (404), preventing
resource disclosure. Relays are shared operator metadata visible to authenticated
members; users cannot mutate that registry.

POST /auth/login requires JSON {"token":"<API key>"} and returns
{"session":{"accessToken":"<one-time secret>","expiresAt":"<UTC time>"}}.
A session expires after at most one hour and never later than its parent API key.
Both API keys and sessions can authenticate requests. GET /me returns user and
organization context. POST /auth/logout returns 204 and revokes the supplied bearer
(session or API key); revoking a parent key also invalidates its sessions. Missing,
invalid, expired and revoked bearers return 401 with AUTH_INVALID, AUTH_EXPIRED or
AUTH_REVOKED. Responses containing secrets set Cache-Control: no-store. Server
state stores tokenHash only; raw credentials are returned once and never logged.

POST /projects requires name (1–100 characters), slug (1–63 lowercase slug
characters); the organization comes from the credential. Duplicate slugs return
409 PROJECT_CONFLICT. POST /tunnels requires projectId, name, type (EPHEMERAL or
PERSISTENT), protocol (http), localHost (numeric loopback) and localPort (1–65535).
Optional slug uses the same rules; omission derives a slug from the opaque tunnel
ID. Metadata is declarative; only the CLI's explicit numeric loopback port is dialed.
Tunnel IDs are opaque and hostnames use the relay's existing SHA-256 mapping under
PUBLIC_BASE_DOMAIN. DELETE /tunnels/:id and POST /tunnels/:id/revoke transition to
REVOKED and invalidate credentials; deletion retains the terminal record/history.
Generation is a canonical uint64 decimal string, preserving all bits in JSON.

GET collection endpoints accept limit (1–100, default 50) and opaque cursor.
Responses contain the corresponding plural collection and meta.nextCursor (string
or null). Cursors are bound to caller, endpoint and validated filters. Tunnel
filters projectId, status and relayId are applied after authorization. Unknown or
duplicate query parameters, invalid cursors and unsupported enum values return 400.

POST /tunnels/:id/connect accepts an optional flat JSON object with
minimumGeneration (canonical uint64 decimal string, >=1). It atomically selects
the configured HEALTHY TLS relay, reserves max(current generation + 1, minimum),
sets CONNECTING/relayId and returns {relay,credential,generation,publicHostname}.
The credential has id,tunnelId,scope (connect),issuedAt,expiresAt and token; TTL is
5 minutes by default, configurable up to 15 minutes. At most 16 live credentials
per tunnel are retained. Healthy configured relay absence returns 503
RELAY_UNAVAILABLE; generation exhaustion returns 409 GENERATION_EXHAUSTED.
Issuance is atomic: failed admission creates no credential or generation change.

Idempotency-Key (8–255 ASCII token characters) binds caller, method/path and exact
request bytes for project/tunnel creation, deletion, revoke and connect. Successful
replays preserve IDs/generations; changed payload returns 409 IDEMPOTENCY_CONFLICT.
Records expire after 10 minutes and are bounded. Secret-bearing login/connect
results are not retained for replay: an exact keyed retry returns 409
CREDENTIAL_ALREADY_ISSUED; use a new key to issue a fresh credential. Authorization
and current parent-resource access are checked before every replay.

API JSON bodies are capped at 64 KiB and must be objects with documented fields;
unknown/duplicate fields, wrong types and trailing data return 400, oversize returns 413. Authenticated/unauthenticated work uses a bounded rate limiter (120 requests
per minute per bearer/remote address), 128 active requests and explicit header,
body, request and shutdown timeouts. State saturation returns 503 CAPACITY_REACHED;
unknown routes use NOT_FOUND and errors never echo request values or secrets.

### Internal relay credential verification

POST /api/v1/internal/credentials/verify is authenticated by a separate private
RELAY_API_TOKEN_FILE credential (hash in the API seed), scoped to relayId. Body:
{"tokenHash":"<64 lowercase SHA-256 hex>","relayId":"rel_..."}. The API checks
credential expiry/revocation, tunnel state, issuance relay and parent user/key
policy, returning {tunnelId,generation,expiresAt}; failures use AUTH_INVALID,
AUTH_EXPIRED or AUTH_REVOKED. User API keys cannot invoke this endpoint; relay
keys cannot access user endpoints. Verification runs only during AUTH and is
bounded by the relay handshake deadline. There is no successful-auth cache or
public-request API call. Revocation prevents future AUTH; admitted sessions keep
their bounded lease until expiry, transport failure or shutdown. Active push
revocation/distributed presence remain later work. From Phase 10, a credential's
relay/generation must also match the latest durable assignment. Superseded leases
return AUTH_REVOKED on future AUTH, including after relay/API restarts; already
admitted sessions retain their local lease until replacement or expiry.

### Agent API bootstrap

PORTWAY_API_TOKEN_FILE explicitly enables control mode for port invocations.
PORTWAY_API_URL identifies the /api/v1 base; PORTWAY_API_CA_FILE optionally adds
trusted API roots; HTTP is permitted only for numeric loopback/localhost dev URLs.
Redirects are never followed. The API is served on loopback and remote operators
must terminate HTTPS with a trusted proxy. The CLI requests a connection for
PORTWAY_TUNNEL_ID before each new relay session, supplies its persisted minimum
generation and validates the returned tunnel/relay/lease/generation/hostname.
It emits relay_assigned without credentials, then the existing authentication,
registration and readiness events. API-issued generations advance local state.
The API does not choose the local service destination. Direct private-file mode
remains available when PORTWAY_API_TOKEN_FILE is unset.

RELAY_API_URL, RELAY_API_TOKEN_FILE and RELAY_ID opt the relay into API verification;
otherwise its private file verifier remains in force. API credential expiry in
control mode schedules bootstrap/reconnect with credential_expired; API 5xx/429
and network timeouts use control_unavailable. User auth/TLS/protocol/invalid
assignment errors stay terminal. Active data-plane sessions do not poll the API
and continue during outages until their credential expiry. Failed application
streams are never replayed. Existing CLI exit codes remain unchanged.

`register` also uses API bootstrap when `PORTWAY_API_TOKEN_FILE` is configured,
including `register --once` for development readiness. `connect` remains a direct
TLS/authentication diagnostic. Expired hash records can be reclaimed: a known
expired credential returns `AUTH_EXPIRED`, while a reclaimed/unknown hash returns
`AUTH_INVALID`. Both reject authentication.

## 17. Phase 10 persistence contract

`API_STORAGE=postgres` is the default runtime backend and requires DATABASE_URL
and deployed Prisma migrations. The API never silently falls back to memory.
`API_STORAGE=memory` explicitly selects Phase 9 behavior for isolated tests or
local development. `/health` is process liveness; `/ready` checks backend access
and usable user/relay policy and reports `storage: postgres` or `memory`.

Projects, tunnels, configured relays, API keys, sessions, credential bindings,
revocation, generation reservations, idempotency and audit records survive API
restart in PostgreSQL. Existing REST shapes, tenant/role rules, canonical uint64
string generations, one-time secrets and credential lease TTLs are unchanged. Superseded generation
credentials fail future authentication against durable assignment policy.
Cursor signing and IP/user-key rate limiting remain bounded process-local state;
cursors may become invalid after restart and rate quotas are not fleet-wide yet.

Successful writes commit metadata, credential/generation changes, idempotency and
audit together. Concurrent API instances serialize mutation admission and replay
through a transaction-scoped database lock. A lock/statement/connection/transaction
failure returns 503 STORAGE_UNAVAILABLE with no partial mutation or secrets in the
error. Expired sessions/credentials/idempotency records may be reclaimed; unknown
reclaimed hashes still fail authentication. Resource reads use scoped bounded
keyset queries. Metadata on historical unbound credential rows grants no AUTH.

Provisioning uses an explicit transactional create-only `db:seed` import of the
private API_SEED_FILE. Runtime never reloads provisioning over durable policy.
Rerunning the seed does not undo revocation, overwrite generations or resurrect
terminal tunnels. Database migration and seeding happen before development API
startup. Public HTTP/WebSocket/SSE continue using the relay's existing local lease
and are independent of database availability until expiry/failure/shutdown.

## 18. Phase 11 relay registration, health and failover

Control-mode assignments require fresh HEALTHY Redis presence, usable relay keys,
enabled durable policy and capacity headroom. Seeded health alone is insufficient.
GET /relays and /relays/:id expose effective status/lastSeenAt and nullable
`capacity` (activeConnections,activeTunnels,retainedTunnels,activeStreams,
maxConnections,maxTunnels,maxStreams). Missing presence is OFFLINE. `/ready`
requires usable database policy and at least one live enabled healthy relay.

The following flat-object POST endpoints are authenticated exclusively with the
node's relay-scoped key, and cannot change operator-provisioned endpoints:

- `/internal/relays/:id/register`: instanceId (32 lowercase hex), status
  (HEALTHY/DEGRADED/DRAINING), and the seven integer capacity fields above.
- `/internal/relays/:id/report`: the same fields plus leaseId (32 lowercase hex)
  and sequence (1..2147483647). Replies contain relayId,instanceId,leaseId,sequence,
  expiresAt and drainRequested. A same-instance register retry preserves its
  existing sequence/lease. A fresh different instance requires prior expiry or a
  DRAINING report; stale incarnations or changed duplicate sequences
  return 409 PRESENCE_STALE. Missing presence returns 409 PRESENCE_EXPIRED and
  permits re-registration. Heartbeats report every 2s by default (100ms..5s),
  lease TTL is 15s, and receipt time is assigned by the API rather than the node.
- `/internal/relays/:id/drain` and `/activate`: empty optional objects. These
  idempotent operator commands persist DRAINING/HEALTHY policy with an audit.
  Activation requires a fresh process/report before assignment; it cannot reopen
  a locally drained relay. A drain reply/report flag triggers node graceful
  shutdown; pending admissions stop and existing streams finish within the
  existing shutdown deadline. User API keys cannot operate the shared fleet.

Counts are bounded by configured server limits: maxConnections<=10000,
maxTunnels<=100000, maxStreams<=1024 per tunnel; active counts cannot exceed their
physical bounds. Unknown/duplicate keys and malformed types/counters return 400.
Redis failures return 503 PRESENCE_UNAVAILABLE with no successful local fallback.
Report traffic has separate bounded 4096/min per-IP and 900/min per-node buckets
so multiple nodes sharing API ingress do not exhaust the user quota. Report intervals
still need sizing for fleet load. Normal node shutdown publishes ephemeral DRAINING before the local graceful
shutdown; it preserves durable operator enablement for a later restart. Only an
explicit `/drain` command persists drain policy. Development starts a reporting
worker in private-file mode using separate `RELAY_REPORT_API_URL`,
`RELAY_REPORT_API_TOKEN_FILE`, `RELAY_REPORT_API_CA_FILE` and `RELAY_REPORT_ID`
(falling back to verification API settings). Reporting cannot enable API credential
verification in private-file mode. No issued secret is stored in Redis or logged. Report leases only fence state
updates and are not substitutes for relay authentication.

`POST /tunnels/:id/connect` additionally accepts optional `avoidRelayId` (opaque
relay ID). Selection keeps the current usable relay when possible, otherwise
chooses the node with most proportional headroom, using ID as deterministic tie
break. A healthy alternative to avoidRelayId is preferred; the avoided relay is
still a fallback if no other eligible node exists. This handles agent-specific
transport failure before node-wide reports expire. No available node returns
503 RELAY_UNAVAILABLE without allocating a credential/generation. Renewing a
tunnel excludes its own existing credential reservation from the admission count;
observed local socket, stream and retained-registry limits still apply.

Control-mode port invocations remember their assigned relay ID and prefer an
alternative after a retryable connection failure; every retry obtains a new
lease/generation. Draining nodes and expired reports are excluded. Errors caused
by credentials, TLS validation or protocol violations remain terminal. Failover
cancels interrupted HTTP/WebSocket/SSE and never replays application traffic.

The public hostname remains stable. Each relay's REGISTER_OK supplies its actual
public HTTPS port. Local multi-relay tests use separate ports and the newly emitted
URL after recovery. Production requires operator ingress/DNS to route that hostname
to its assigned relay (e.g. a controller watching assignment metadata); this phase
does not install an ingress load balancer or proxy application bytes across relays.
Sending traffic indiscriminately to every node cannot resolve a remote local mux.

## 19. Phase 12 domains and TLS

Custom domains are scoped through their tunnel/project/organization. Reads require
membership; OWNER/ADMIN/MEMBER may mutate, VIEWER receives 403. Cross-organization
IDs return DOMAIN_NOT_FOUND. Hostnames are canonical lowercase ASCII DNS names
with at least two labels, at most 220 characters; uppercase input is normalized.
Reject IPs, ports, URLs, wildcard input, trailing dots, underscores, numeric TLDs,
and PUBLIC_BASE_DOMAIN or its children. Global hostname reservations are unique,
including DISABLED records. Domain association never selects a local destination.
The initial deployment admits at most 128 domain records (and existing lower
resource limits), retaining disabled records for audit and safe reservation.

- GET /domains: cursor/limit and optional tunnelId; scoped keyset pagination.
- GET /domains/:id: scoped metadata; verification hashes are never exposed.
- POST /domains: flat {hostname,tunnelId}. Reject revoked tunnels. Returns 201 with
  {domain,verification:{type:"TXT",name:"_portway-challenge.<hostname>",value}}.
  The value is `portway-verification=` plus a random 32-byte base64url token,
  returned once; only SHA-256 of that complete value is stored. Save the value
  and publish it as DNS TXT. Challenges expire after 24h.
- POST /domains/:id/challenge: empty optional object; rotates a one-time challenge,
  clears verification and returns the domain to PENDING_VERIFICATION. This also
  removes any active alias, and can recover a lost/expired challenge.
- POST /domains/:id/verify: empty optional object; resolves the exact TXT name via
  the operator's DNS resolver, with at most 16 simultaneous lookups, a 2s deadline,
  and bounded records/bytes. No URL/HTTP callback or caller-selected resolver.
  Exact hashed proof transitions PENDING_VERIFICATION to VERIFIED; already
  VERIFIED/ACTIVE is an idempotent read. Mismatch/expired challenge returns
  409 DOMAIN_VERIFICATION_REQUIRED; resolver failure returns 503 DNS_UNAVAILABLE.
  DNS runs outside database transactions; commit rechecks auth, current challenge,
  tunnel revocation and expiry, rejecting rotated/disabled proof races.
- POST /domains/:id/activate: empty optional object; VERIFIED or already ACTIVE
  becomes ACTIVE. PENDING_VERIFICATION/DISABLED returns verification-required.
  ACTIVE means operator-enabled routing policy, not globally confirmed certificate
  deployment. Relay needs a matching valid locally provisioned certificate.
- DELETE /domains/:id: empty optional object; sets DISABLED and clears proof.
  Association/history/hostname reservation remain; rotate a challenge to re-enable.

Create/challenge are one-time proof responses; keyed retries use the existing
CREDENTIAL_ALREADY_ISSUED error without storing raw proof. Activate/delete use
metadata idempotency with scope rechecked on replay. Verify is logically idempotent
and does not hold a writer transaction during DNS. Mutations and audits commit
atomically. Unknown/duplicate fields and queries use existing validation limits.
API_DNS_SERVER optionally selects an operator-provisioned numeric resolver address
(with optional port); configuration is validated before listening.

Relay register/report acknowledgements additionally include `routes`, a complete
snapshot of at most 128 {hostname,tunnelId,generation,expiresAt} bindings. They
include only proof-backed ACTIVE domains for non-revoked tunnels currently assigned
to the authenticated node with a positive generation. Snapshot lease is 15m;
local session expiry can shorten its use. A newer report replaces the entire map,
so disable/revoke/reassignment removes aliases on the next report. An API outage
retains existing aliases until their snapshot/session expiry; it cannot create or
extend them. A TLS certificate alone never authorizes an alias. The public Host/SNI
must agree and route generation must match the live local owner before any stream.
The generated hostname and wire registration binding remain stable. Optional
`custom_domains` negotiates `public_host` OPEN_STREAM metadata for local alias Host
preservation; legacy agents return 501 for aliases and retain generated-host HTTP.

PUBLIC_TLS_MANIFEST_FILE optionally supplies a private local JSON certificate
manifest with optional `default: {hostname,certFile,keyFile}` and
`domains: [{hostname,certFile,keyFile}]`. A default entry atomically redirects the
wildcard pair to an immutable renewed bundle. It contains no API-provided
paths. The existing PUBLIC_TLS_CERT_FILE/KEY_FILE supply the default wildcard pair.
A bounded joined worker reloads pairs every PUBLIC_TLS_RELOAD_INTERVAL (30s default,
100ms..5m). Key/manifest files must be private regular files, not symlinks; PEM and
manifest sizes are bounded. Validate key match, certificate validity/server usage
and coverage before atomic in-memory publication. Failed reload keeps prior valid
certificates; expired certificates cannot serve new handshakes. API/DB/Redis/DNS
and certificate file reads never occur in the public request/TLS callback.

Local certificate tooling creates an explicitly selected private CA directory,
issues stable-root wildcard/custom PEM bundles and renews near expiry without
rotating its CA or agent credentials. It does not install OS trust. mkcert can
supply trusted local PEM files; production may use an external ACME issuer and
atomically replace versioned pairs/manifest. ACME account/challenge automation is
post-MVP work. Operators must route wildcard/custom DNS to the assigned relay and
keep ingress consistent with Phase 11; there is no cross-relay byte proxy.

## 20. Phase 13 browser dashboard adapter

The Next.js dashboard consumes the existing /api/v1 contract. It has no database,
Redis, relay-key or application-byte access. Browser-local routes below belong to
the dashboard origin and are outside the control-plane OpenAPI base.

- POST /api/session accepts JSON {token} with a provisioned user API key, calls
  POST /auth/login once and encrypts the returned session in a host-only HttpOnly,
  SameSite=Lax cookie (Secure for a configured HTTPS dashboard origin). The browser
  cookie is authenticated with AES-256-GCM under DASHBOARD_SESSION_KEY_FILE, a
  private regular file containing a 32-byte base64url key. The key is loaded once
  per server process; setup creates and preserves a separate development key.
  Editing a cookie cannot substitute a parent API key. The browser
  response contains expiry only, never the session bearer. No local/sessionStorage
  or URL holds authentication secrets. Login does not silently load operator keys.
- DELETE /api/session revokes the cookie's session through /auth/logout and always
  clears the local cookie. During control failure, the response explicitly reports
  that remote revocation could not be confirmed; the bounded API session expires
  normally. It never substitutes the user's parent API key for a session.
- /api/control/* forwards only allowlisted scoped metadata: me; project list/create;
  tunnel list/detail/create/revoke; domain list/detail/create/challenge/verify/
  activate/disable; relay list/detail; scoped tunnel logs/metrics reads. Internal
  relay operations, connect/credential issuance, arbitrary paths/destinations and
  caller Authorization/Cookie forwarding are excluded. The fixed server API URL
  selects the destination. API envelopes/statuses and keyed metadata mutations
  remain authoritative. Successful mutations invalidate dashboard views before
  browser refresh. Mutation controls share pending state through a document reload
  so another change cannot overlap an unfinished view update. One-time proof forms
  defer that reload until acknowledgement/close to retain their current value.
  Link prefetching and automatic polling are disabled. Browser
  sessions receive no relay credentials.
- Every mutation requires exact Origin equality with DASHBOARD_ORIGIN, JSON content
  type and same-origin browser semantics; missing/cross-origin requests fail before
  API access. Inbound bodies are bounded to 64 KiB with a 5s deadline, API replies
  to 256 KiB, and concurrent API calls to 32 per process. Calls have a complete
  configurable timeout, propagate cancellation and never follow redirects/replay.
- DASHBOARD_API_URL is a fixed /api/v1 HTTPS URL, or numeric loopback/localhost HTTP
  for development. DASHBOARD_ORIGIN is a fixed HTTPS origin, or loopback HTTP in
  development. They default to the development API/dashboard ports. Remote private
  CAs use Node's configured trust (for example NODE_EXTRA_CA_CERTS), without disabling
  TLS verification. Production requires its own private session-key file; key
  replacement requires server restart and invalidates existing dashboard cookies.
  DASHBOARD_API_TIMEOUT_MS defaults to 5000, allowed 100..10000.
- Authenticated SSR and browser replies are uncached. Auth failure redirects to
  /login or clears a browser adapter cookie on 401. Protected pages always obtain
  authoritative /me context; role-based UI is a convenience, API authorization is
  the enforcement boundary. Domain proofs appear once in the current form and
  are not stored or logged. API response errors are rendered safely as text.

Pages follow IMPLEMENTATION's order. Overview/list/detail use real bounded API
metadata and cursor pagination; CONNECTING is issuance state, not proof of live
presence. Tunnel actions create policy/revoke and show a CLI command, without
changing generations just to render a page. Relays are read-only for user keys.
Domain forms expose the ownership lifecycle and distinguish ACTIVE routing policy
from certificate readiness. Settings shows scoped account/organization/role and
logout. Logs shows the scoped API result, including honest 501 NOT_IMPLEMENTED;
request metrics, traffic recording and audit browsing await supported API contracts.
