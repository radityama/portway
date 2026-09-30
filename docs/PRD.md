# Portway — Product Requirements Document

## 1. Overview

**Project codename:** Portway

**Category:** Developer Infrastructure / Networking / Reverse Tunnel

Portway is a self-hosted developer tunneling platform that exposes a local development service through a public HTTPS endpoint without requiring inbound firewall rules or port forwarding.

The product is inspired by the simplicity of `radityama/peek`, but its infrastructure is owned by the project: control plane, relay/edge servers, tunnel protocol, authentication, routing, domains, observability, and reliability mechanisms are first-class components.

## 2. Problem

Developers frequently need to expose local services for:

- webhook callbacks
- mobile-device testing
- client demos
- QA review
- OAuth callbacks
- third-party integrations
- remote collaboration
- AI agent/browser testing

Existing tools can hide infrastructure behind a hosted provider, but this project must also support a fully self-controlled deployment.

## 3. Product Vision

The primary workflow should be as simple as:

```bash
portway 3000
```

The CLI should return a stable, machine-readable description of the tunnel while the human-facing mode remains concise:

```text
✓ Tunnel connected

Local   http://localhost:3000
Public  https://abc123.portway.example.com
Relay   jkt-01

Ready.
```

## 4. Goals

### 4.1 MVP goals

The MVP MUST provide:

- local HTTP tunneling
- public HTTPS endpoint
- outbound-only agent connection
- self-hosted relay
- control plane
- authentication
- tunnel registration
- hostname routing
- connection heartbeat
- automatic reconnect with exponential backoff and jitter
- multiplexed concurrent streams
- bounded buffering and backpressure
- request/response streaming
- WebSocket support
- graceful shutdown
- structured logging
- Prometheus-compatible metrics
- CLI JSON output
- `portway doctor`
- automatic development-server detection
- basic persistent tunnels
- basic multi-relay support

### 4.2 Post-MVP goals

- custom domains
- ACME automation
- access policies and OIDC
- webhook inspector
- request replay
- regional relay selection
- TCP tunnels
- private/internal tunnels
- organization/team management
- usage quotas and billing
- advanced tracing

## 5. Non-Goals for MVP

The MVP should NOT include:

- UDP tunneling
- arbitrary public proxying to user-selected destinations
- transparent replay of failed HTTP requests after agent reconnect
- a full billing system
- complex multi-region consensus
- production-grade traffic recording of sensitive payloads by default

## 6. Personas

### Developer

Wants to expose a local port quickly with minimal configuration.

### AI Coding Agent

Needs deterministic CLI output, JSON events, predictable exit codes, and docs it can reason over safely.

### Platform Operator

Needs relay health, draining, metrics, logs, capacity controls, and safe deployment procedures.

## 7. User Stories

### Basic tunnel

As a developer, I can run:

```bash
portway 3000
```

and receive a public HTTPS URL.

### Persistent tunnel

As a developer, I can create a persistent tunnel with a stable identity and reconnect it after a local process restart.

### WebSocket

As a developer, I can expose an application that uses WebSocket connections without breaking upgrade semantics or bidirectional streaming.

### Reconnect

As a developer, when the network briefly disconnects, the CLI reconnects automatically without requiring me to restart it manually.

### Multi-relay

As an operator, I can run multiple relay nodes and drain one without taking the whole service offline.

### Automation

As an AI coding agent, I can invoke the CLI with `--json` and consume structured events without parsing terminal decoration.

## 8. Functional Requirements

### FR-001 — CLI

The CLI MUST support at least:

```text
portway [port]
portway start [port]
portway stop
portway status
portway list
portway login
portway logout
portway create
portway delete
portway domain add
portway domain remove
portway config
portway doctor
portway logs
portway version
```

### FR-002 — Zero-configuration mode

`portway <port>` MUST be sufficient for the default ephemeral workflow after authentication.

### FR-003 — Local service discovery

When no port is specified, the agent SHOULD detect the development server using:

1. explicit CLI configuration
2. environment/configuration
3. framework-aware package-manager detection
4. dev-process output parsing
5. local port probing

Supported package managers MUST include npm, pnpm, yarn, and bun.

### FR-004 — Authentication

The platform MUST authenticate:

- CLI to control plane
- agent tunnel session to relay

Credentials MUST support expiration and revocation.

### FR-005 — Relay

The relay MUST terminate public TLS, route by hostname, maintain active tunnel connections, and forward streams.

### FR-006 — Hot-path isolation

Application traffic MUST NOT require a control-plane API or PostgreSQL query per request.

### FR-007 — Reliability

The system MUST have:

- heartbeat
- reconnect
- exponential backoff with jitter
- stale-connection protection
- graceful shutdown
- relay draining
- bounded resource usage

### FR-008 — Protocol safety

Malformed frames MUST be rejected safely and MUST NOT crash the process.

### FR-009 — Streaming

The system MUST stream request and response bodies rather than loading entire payloads into memory.

### FR-010 — Security

The system MUST implement TLS, authorization, rate limiting, request-size limits, concurrency limits, timeout handling, audit logging, and secret redaction.

## 9. Non-Functional Requirements

### Reliability

Target tunnel recovery after a transient relay/network failure: under 10 seconds in normal conditions.

Target relay availability: 99.9%+ in a production deployment, subject to infrastructure quality.

### Performance

The public request path should avoid synchronous database access.

The relay should maintain low per-stream overhead and support many concurrent streams per agent connection.

### Security

Public hostnames are discoverability mechanisms, not authentication.

No component may log bearer tokens, private keys, cookies, authorization headers, or raw sensitive request bodies by default.

### Operability

Every important connection and stream failure should be observable through logs and metrics.

## 10. CLI UX Requirements

Human mode MUST be concise.

JSON mode MUST emit one complete JSON object per line and MUST NOT mix human-readable logs into stdout.

Example:

```json
{"event":"starting"}
{"event":"server_ready","port":3000}
{"event":"tunnel_connecting"}
{"event":"tunnel_connected","relay":"jkt-01"}
{"event":"public_url","url":"https://abc123.portway.example.com"}
{"event":"ready","local_url":"http://localhost:3000","public_url":"https://abc123.portway.example.com"}
```

## 11. Dashboard Requirements

The dashboard SHOULD expose:

- overview
- active tunnels
- tunnel detail
- request metrics
- bandwidth
- relay health
- domains
- logs
- settings
- audit events

## 12. Security and Abuse Requirements

The platform exposes developer machines to the public Internet and therefore needs abuse controls.

Minimum controls:

- authenticated accounts
- API credentials
- per-IP rate limiting
- per-account quotas
- concurrent-stream limits
- request-body limits
- idle timeouts
- connection timeouts
- token revocation
- audit logging
- manual tunnel revoke

Do not implement an arbitrary forward proxy. A tunnel may only target the explicitly configured local service unless a separate proxy product is intentionally added.

## 13. Acceptance Criteria

The MVP is accepted when all of the following succeed:

```text
local HTTP request            ✓
public HTTPS request          ✓
multiple concurrent streams   ✓
request streaming             ✓
response streaming            ✓
WebSocket                     ✓
heartbeat                     ✓
reconnect                     ✓
backpressure                  ✓
graceful shutdown              ✓
authentication                ✓
revocation                    ✓
hostname routing              ✓
relay health                  ✓
relay draining                ✓
multi-relay registration       ✓
control-plane outage isolation✓
structured logs               ✓
metrics                       ✓
JSON CLI                      ✓
doctor                        ✓
protocol fuzz tests            ✓
end-to-end tests               ✓
```

## 14. Product Principle

The project is not merely a CLI that creates a public URL. It is a distributed reverse-tunneling platform whose CLI is the primary developer interface.
