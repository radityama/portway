# Portway — Database Design

## 1. Storage Responsibilities

### PostgreSQL

Durable metadata and audit history:

- users
- organizations
- memberships
- projects
- tunnels
- tunnel credentials
- domains
- relays
- API keys
- access policies
- audit logs
- usage records

### Redis

Ephemeral/distributed state:

- relay presence
- connection metadata
- routing cache
- short-lived locks
- rate limiting
- pub/sub notifications

Redis must not be the sole durable source of truth.

## 2. Entity Relationship

```text
Organization
├── Memberships ── User
├── Projects
│   └── Tunnels
│       ├── TunnelCredentials
│       ├── Domains
│       ├── AccessPolicies
│       └── UsageRecords
├── ApiKeys
└── AuditLogs

Relay
└── active runtime state in Redis; durable registration in PostgreSQL
```

## 3. Identifier Strategy

Use opaque IDs rather than sequential public IDs.

Examples:

```text
usr_01J...
org_01J...
prj_01J...
tnl_01J...
cred_01J...
dom_01J...
rel_01J...
key_01J...
```

The specific ID implementation may use UUIDv7/ULID-like semantics, but public identifiers must not disclose row counts or enable easy enumeration.

## 4. Prisma Schema — Initial Baseline

The following is a baseline schema. It may be adapted during implementation, but API and routing contracts should preserve these logical entities.

```prisma
generator client {
  provider = "prisma-client-js"
}

datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

enum TunnelStatus {
  CREATED
  CONNECTING
  CONNECTED
  DISCONNECTED
  DRAINING
  REVOKED
}

enum TunnelType {
  EPHEMERAL
  PERSISTENT
}

enum RelayStatus {
  HEALTHY
  DEGRADED
  DRAINING
  OFFLINE
}

enum DomainStatus {
  PENDING_VERIFICATION
  VERIFIED
  ACTIVE
  DISABLED
}

model User {
  id            String         @id
  email         String         @unique
  displayName   String?
  createdAt     DateTime       @default(now())
  updatedAt     DateTime       @updatedAt
  memberships   Membership[]
  apiKeys       ApiKey[]
  auditLogs     AuditLog[]
}

model Organization {
  id            String          @id
  name          String
  slug          String          @unique
  createdAt     DateTime        @default(now())
  updatedAt     DateTime        @updatedAt
  memberships   Membership[]
  projects      Project[]
  apiKeys       ApiKey[]
  auditLogs     AuditLog[]
}

model Membership {
  id             String        @id
  organizationId String
  userId         String
  role           String
  createdAt      DateTime      @default(now())
  organization   Organization  @relation(fields: [organizationId], references: [id], onDelete: Cascade)
  user           User          @relation(fields: [userId], references: [id], onDelete: Cascade)

  @@unique([organizationId, userId])
  @@index([userId])
}

model Project {
  id             String        @id
  organizationId String
  name           String
  slug           String
  createdAt      DateTime      @default(now())
  updatedAt      DateTime      @updatedAt
  organization   Organization  @relation(fields: [organizationId], references: [id], onDelete: Cascade)
  tunnels        Tunnel[]

  @@unique([organizationId, slug])
  @@index([organizationId])
}

model Tunnel {
  id              String             @id
  projectId       String
  name            String
  slug            String
  type            TunnelType         @default(EPHEMERAL)
  status          TunnelStatus       @default(CREATED)
  protocol        String             @default("http")
  localHost       String             @default("127.0.0.1")
  localPort       Int?
  publicHostname  String?            @unique
  relayId         String?
  generation      BigInt             @default(0)
  createdAt       DateTime           @default(now())
  updatedAt       DateTime           @updatedAt
  lastConnectedAt DateTime?
  project         Project            @relation(fields: [projectId], references: [id], onDelete: Cascade)
  relay           Relay?             @relation(fields: [relayId], references: [id], onDelete: SetNull)
  credentials     TunnelCredential[]
  domains         Domain[]
  accessPolicies  AccessPolicy[]
  usageRecords    UsageRecord[]

  @@unique([projectId, slug])
  @@index([projectId])
  @@index([relayId, status])
  @@index([status])
}

model TunnelCredential {
  id         String    @id
  tunnelId   String
  tokenHash  String
  scope      String
  issuedAt   DateTime  @default(now())
  expiresAt  DateTime?
  revokedAt  DateTime?
  tunnel     Tunnel    @relation(fields: [tunnelId], references: [id], onDelete: Cascade)

  @@index([tunnelId])
  @@index([expiresAt])
}

model Domain {
  id         String       @id
  tunnelId   String?
  hostname   String       @unique
  status     DomainStatus @default(PENDING_VERIFICATION)
  verifiedAt DateTime?
  createdAt  DateTime     @default(now())
  updatedAt  DateTime     @updatedAt
  tunnel     Tunnel?      @relation(fields: [tunnelId], references: [id], onDelete: SetNull)

  @@index([tunnelId])
  @@index([status])
}

model Relay {
  id          String       @id
  name        String
  region      String
  hostname    String
  port        Int
  protocol    String       @default("tls")
  status      RelayStatus  @default(OFFLINE)
  createdAt   DateTime     @default(now())
  updatedAt   DateTime     @updatedAt
  lastSeenAt  DateTime?
  tunnels     Tunnel[]

  @@index([region, status])
  @@index([status, lastSeenAt])
}

model ApiKey {
  id             String        @id
  userId         String?
  organizationId String?
  name           String
  tokenHash      String
  expiresAt      DateTime?
  revokedAt      DateTime?
  createdAt      DateTime      @default(now())
  user           User?         @relation(fields: [userId], references: [id], onDelete: Cascade)
  organization   Organization? @relation(fields: [organizationId], references: [id], onDelete: Cascade)

  @@index([userId])
  @@index([organizationId])
  @@index([expiresAt])
}

model AccessPolicy {
  id          String    @id
  tunnelId    String
  type        String
  config      Json
  createdAt   DateTime  @default(now())
  updatedAt   DateTime  @updatedAt
  tunnel      Tunnel    @relation(fields: [tunnelId], references: [id], onDelete: Cascade)

  @@index([tunnelId])
}

model AuditLog {
  id             String        @id
  userId         String?
  organizationId String?
  action         String
  resourceType   String
  resourceId     String?
  metadata       Json?
  createdAt      DateTime      @default(now())
  user           User?         @relation(fields: [userId], references: [id], onDelete: SetNull)
  organization   Organization? @relation(fields: [organizationId], references: [id], onDelete: SetNull)

  @@index([organizationId, createdAt])
  @@index([resourceType, resourceId])
  @@index([userId, createdAt])
}

model UsageRecord {
  id          String   @id
  tunnelId    String
  windowStart DateTime
  requests    BigInt   @default(0)
  bytesIn     BigInt   @default(0)
  bytesOut    BigInt   @default(0)
  tunnel      Tunnel   @relation(fields: [tunnelId], references: [id], onDelete: Cascade)

  @@unique([tunnelId, windowStart])
  @@index([windowStart])
}
```

## 5. Sensitive Data Rules

Never store raw credentials when a hash is sufficient.

Store:

```text
tokenHash
```

not:

```text
rawToken
```

TLS private keys, if managed by the platform, should be stored outside the normal application table model or encrypted with a proper secret-management approach.

## 6. Hot-Path Data

The relay needs an in-memory representation such as:

```text
hostname → tunnel ID → session pointer
```

Redis may back cross-node ephemeral state.

PostgreSQL must not be queried for every request.

## 7. Transactions

Use transactions when creating interdependent resources such as:

```text
Tunnel
+ initial credential
+ hostname reservation
+ relay assignment metadata
```

Idempotency keys should be used for retryable mutations.

## 8. Deletion Policy

Do not immediately hard-delete audit history.

Operational resources may use soft deletion or terminal statuses where history matters.

Cascade only for child records whose history has no independent operational value.

## 9. Indexing Rules

Index fields used for:

- uniqueness
- organization scoping
- status filtering
- relay assignment
- expiration cleanup
- recent audit queries
- routing metadata lookup

Avoid adding indexes solely because a field exists.

## 10. Migration Rules

Development:

```bash
pnpm prisma migrate dev
```

Production:

```bash
pnpm prisma migrate deploy
```

Never edit production tables manually unless performing a controlled emergency operation with a corresponding migration afterward.

## 11. Redis Key Conventions

Suggested namespace:

```text
relay:{relayId}:presence
relay:{relayId}:capacity
relay:{relayId}:tunnel:{tunnelId}
tunnel:{tunnelId}:session
tunnel:{tunnelId}:generation
routing:host:{hostname}
ratelimit:{scope}:{identifier}:{window}
lock:{resource}
```

Keys must include an expiration policy when the data is ephemeral.

## 12. Consistency Rules

Durable identity lives in PostgreSQL.

Ephemeral connection state lives in the relay process and Redis.

The system must tolerate Redis loss by allowing relays to re-register and rebuild ephemeral state.

## 13. Phase 9 storage boundary

Before Phase 10 migrations, the control API uses bounded in-memory representations
of the existing User, Organization, Membership, Project, Tunnel, TunnelCredential,
Relay and ApiKey meanings. No Prisma entity or field is added. Private seed files
contain hashes, not raw tokens. API sessions, issuance relay/generation/parent-key
bindings, rate buckets, cursors and idempotency entries are bounded ephemeral
control state with TTLs; they are not additional durable entities. Secret-bearing
responses are not cached. Tunnel deletion retains a terminal REVOKED record.
API restart loses runtime mutations and credentials; relay ownership watermarks
still fail closed against stale generations. Phase 10 must make identity,
credentials, generation reservations and audit history durable before production.
