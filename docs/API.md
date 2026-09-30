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
