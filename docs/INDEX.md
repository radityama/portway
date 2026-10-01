# Portway Documentation

## Source-of-truth documents

- [PRD](./PRD.md) — product scope, goals, requirements, user stories, acceptance criteria
- [ARCHITECTURE](./ARCHITECTURE.md) — system boundaries, components, control/data plane
- [DATABASE](./DATABASE.md) — durable/ephemeral storage responsibilities and logical schema baseline
- [API](./API.md) — REST API semantics, responses, errors, CLI JSON contract
- [ROUTING](./ROUTING.md) — hostname routing, streams, multiplexing, flow control
- [IMPLEMENTATION](./IMPLEMENTATION.md) — phased build order, testing and quality gates
- [AGENTS](./AGENTS.md) — repository agent instructions

## Machine-readable contracts / supplements

- [OpenAPI](./openapi.yaml) — machine-readable representation of `API.md`
- [Protocol](./PROTOCOL.md) — binary framing and protocol supplement for the implementation
- [Decisions](./DECISIONS.md) — architecture decision notes
- [Starter Status](./STARTER_STATUS.md) — what is scaffolded and what still requires implementation
- [Phase 0](./PHASE_0.md) — bootstrap deliverables, acceptance checks, and the next implementation boundary
- [Phase 1](./PHASE_1.md) — protocol framing, negotiation, compatibility, and failure tests
- [Phase 2](./PHASE_2.md) — authenticated TLS connections, credential verification, and lifecycle tests
- [Phase 3](./PHASE_3.md) — tunnel registration, hostname resolution, generation ownership, and replacement tests
- [Phase 4](./PHASE_4.md) — public HTTPS, HTTP stream forwarding, limits, and cancellation tests
- [Phase 5](./PHASE_5.md) — stream/connection credit, bounded buffering, and slow-consumer isolation
- [Phase 6](./PHASE_6.md) — heartbeat liveness, reconnect backoff, fresh generations, and no-replay recovery
- [Phase 7](./PHASE_7.md) — negotiated draining, active work completion, bounded shutdown, and cleanup
- [Phase 8](./PHASE_8.md) — WebSocket upgrades, SSE and chunked streaming, idle supervision
- [Phase 9](./PHASE_9.md) — scoped control API, short-lived relay credentials and CLI bootstrap
- [Phase 10](./PHASE_10.md) — PostgreSQL durability, migrations, safe provisioning and transaction tests
- [Phase 11](./PHASE_11.md) — live relay reports, capacity selection, operator draining and agent failover
- [Phase 12](./PHASE_12.md) — DNS ownership proofs, generation-bound custom aliases and certificate renewal

## Contract hierarchy

```text
PRD
 ↓
ARCHITECTURE
 ├── DATABASE
 ├── API
 │    └── openapi.yaml
 └── ROUTING
      ↓
IMPLEMENTATION
      ↓
code
```

`docs/DATABASE.md` is the logical persistence source of truth. `prisma/schema.prisma` is only the current Prisma representation of that documented model.
