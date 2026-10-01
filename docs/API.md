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

Remove the domain association.

Verification may require a DNS TXT record, depending on domain-management design.

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
