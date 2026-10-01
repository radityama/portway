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
  id          String       @id
  email       String       @unique
  displayName String?
  createdAt   DateTime     @default(now())
  updatedAt   DateTime     @updatedAt
  memberships Membership[]
  apiKeys     ApiKey[]
  auditLogs   AuditLog[]
}

model Organization {
  id          String       @id
  name        String
  slug        String       @unique
  createdAt   DateTime     @default(now())
  updatedAt   DateTime     @updatedAt
  memberships Membership[]
  projects    Project[]
  apiKeys     ApiKey[]
  auditLogs   AuditLog[]
}

model Membership {
  id             String       @id
  organizationId String
  userId         String
  role           String
  createdAt      DateTime     @default(now())
  organization   Organization @relation(fields: [organizationId], references: [id], onDelete: Cascade)
  user           User         @relation(fields: [userId], references: [id], onDelete: Cascade)

  @@unique([organizationId, userId])
  @@index([userId])
}

model Project {
  id             String       @id
  organizationId String
  name           String
  slug           String
  createdAt      DateTime     @default(now())
  updatedAt      DateTime     @updatedAt
  organization   Organization @relation(fields: [organizationId], references: [id], onDelete: Cascade)
  tunnels        Tunnel[]

  @@unique([organizationId, slug])
  @@index([organizationId, id])
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
  generation      Decimal            @default(0) @db.Decimal(20, 0)
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
  @@index([projectId, id])
  @@index([relayId, status])
  @@index([status])
}

model TunnelCredential {
  id         String      @id
  tunnelId   String
  tokenHash  String      @unique
  scope      String
  issuedAt   DateTime    @default(now())
  expiresAt  DateTime?
  revokedAt  DateTime?
  tunnel     Tunnel      @relation(fields: [tunnelId], references: [id], onDelete: Cascade)
  relayId    String?
  generation Decimal?    @db.Decimal(20, 0)
  apiKeyId   String?
  sessionId  String?
  relay      Relay?      @relation(fields: [relayId], references: [id], onDelete: Cascade)
  apiKey     ApiKey?     @relation(fields: [apiKeyId], references: [id], onDelete: Cascade)
  session    ApiSession? @relation(fields: [sessionId], references: [id], onDelete: Cascade)

  @@index([tunnelId, expiresAt])
  @@index([expiresAt])
}

model Domain {
  id         String       @id
  tunnelId   String?
  hostname   String       @unique
  status     DomainStatus @default(PENDING_VERIFICATION)
  verifiedAt DateTime?
  verificationHash String?
  verificationExpiresAt DateTime?
  createdAt  DateTime     @default(now())
  updatedAt  DateTime     @updatedAt
  tunnel     Tunnel?      @relation(fields: [tunnelId], references: [id], onDelete: SetNull)

  @@index([tunnelId, id])
  @@index([status, id])
}

model Relay {
  id          String             @id
  name        String
  region      String
  hostname    String
  port        Int
  protocol    String             @default("tls")
  status      RelayStatus        @default(OFFLINE)
  createdAt   DateTime           @default(now())
  updatedAt   DateTime           @updatedAt
  lastSeenAt  DateTime?
  tunnels     Tunnel[]
  apiKeys     ApiKey[]
  credentials TunnelCredential[]

  @@index([region, status])
  @@index([status, lastSeenAt])
}

model ApiKey {
  id             String              @id
  userId         String?
  organizationId String?
  name           String
  tokenHash      String              @unique
  expiresAt      DateTime?
  revokedAt      DateTime?
  createdAt      DateTime            @default(now())
  user           User?               @relation(fields: [userId], references: [id], onDelete: Cascade)
  organization   Organization?       @relation(fields: [organizationId], references: [id], onDelete: Cascade)
  relayId        String?
  relay          Relay?              @relation(fields: [relayId], references: [id], onDelete: Cascade)
  sessions       ApiSession[]
  credentials    TunnelCredential[]
  idempotency    IdempotencyRecord[]

  @@index([relayId])
  @@index([userId])
  @@index([organizationId])
  @@index([expiresAt])
}

model AccessPolicy {
  id        String   @id
  tunnelId  String
  type      String
  config    Json
  createdAt DateTime @default(now())
  updatedAt DateTime @updatedAt
  tunnel    Tunnel   @relation(fields: [tunnelId], references: [id], onDelete: Cascade)

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

model ApiSession {
  id          String              @id
  tokenHash   String              @unique
  apiKeyId    String
  expiresAt   DateTime
  revokedAt   DateTime?
  createdAt   DateTime            @default(now())
  apiKey      ApiKey              @relation(fields: [apiKeyId], references: [id], onDelete: Cascade)
  credentials TunnelCredential[]
  idempotency IdempotencyRecord[]

  @@index([apiKeyId])
  @@index([expiresAt])
}

model IdempotencyRecord {
  id           String      @id
  apiKeyId     String
  sessionId    String?
  requestHash  String
  status       Int
  response     Json?
  secret       Boolean
  resourceType String?
  resourceId   String?
  createdAt    DateTime    @default(now())
  expiresAt    DateTime
  apiKey       ApiKey      @relation(fields: [apiKeyId], references: [id], onDelete: Cascade)
  session      ApiSession? @relation(fields: [sessionId], references: [id], onDelete: Cascade)

  @@index([apiKeyId])
  @@index([expiresAt])
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

## 14. Phase 10 durable control-plane state

The Phase 9 memory implementation is retained only as an explicit test/development
backend. PostgreSQL is the default runtime source of truth. Existing entities
remain; the additions below represent policy that must survive an API restart,
not relay sockets or runtime presence:

- `Tunnel.generation` is exact `Decimal(20,0)`, constrained to unsigned 64-bit
  values. PostgreSQL BIGINT is signed and cannot preserve the Phase 9 API's full
  uint64 range. JSON still uses canonical decimal strings.
- `ApiKey.tokenHash` is unique for indexed authentication. An optional `relayId`
  identifies relay-scoped operator keys; they cannot authenticate user endpoints.
  User keys have both user and organization scope. Mixed relay/user scope is
  forbidden. Legacy unscoped rows fail authentication.
- `ApiSession` stores a unique token hash, parent API key, expiration and revocation
  for login/logout persistence. Its child credentials cascade on deletion so a
  removed session cannot leave an authenticatable detached credential.
- `TunnelCredential` stores the issuing relay, exact generation, parent API key
  and optional session. Nullable bindings preserve historical unbound records;
  unbound records fail authentication. AUTH also requires relay/generation to match
  the latest durable tunnel assignment, preventing an older lease from reclaiming
  ownership after a relay restart. Newly issued leases require all primary
  bindings and a finite expiry after issuance. Parent deletion cascades credentials.
  Token hashes are unique and indexed; the tunnel/expiry index serves live-lease
  admission and cleanup. No raw token is stored.
- `IdempotencyRecord` durably binds an opaque hashed caller/method/path/key identity
  to the exact request hash, status, bounded metadata response, resource scope and
  expiry. It never stores secret login/connect responses. Caller/session deletion
  cascades retry records. Expired entries are reclaimed before bounded admission.

All API writes use interactive transactions with a transaction-scoped advisory
lock shared by API instances. This initially serializes writers to make global
admission, slug uniqueness, generation allocation, idempotent retries, revocation
and audit insertion atomic. Bodies are size-bounded and parsed before acquiring that lock.
Project `(organizationId,id)` and tunnel `(projectId,id)` indexes serve scoped
keyset pages (`id > cursor ORDER BY id LIMIT limit+1`), replacing redundant
single-column prefix indexes. Indexed reads and keyset pages do not hydrate whole tables. Pool size, lock wait,
statement time, transaction time and retry/admission are bounded. Database failures
return a safe 503; no process-local success is substituted for a failed commit.

The hash-only provisioning seed is imported explicitly in a transaction and only
creates missing identities/records. Existing tunnel generations/status, revoked
keys/sessions/credentials and audit history are never reset by seed reruns. A key
ID or scope conflict fails the whole import. New development key IDs include a
hash-derived suffix so deliberate rotation creates a new key rather than reviving
an expired/revoked one. Runtime startup does not import seed files or run migrations.

Baseline and additive SQL migrations are transactional with finite lock/statement
deadlines, so failed validation rolls back the schema change. Deployment uses
`prisma migrate deploy`; there is no automatic
reset/drop of an existing database. Migrations include uint64, role, port, key-scope
and credential-binding checks alongside Prisma foreign keys and unique indexes.
Seeded relay HEALTHY metadata is still operator configuration; dynamic presence,
capacity, selection/draining and failover remain Phase 11. Audit history is durable;
operator retention/archival and fleet-scale fine-grained writer locks remain later
work. Relay hot-path state remains local and ephemeral.

## 15. Phase 11 relay fleet state

No new durable entity is required. `Relay` remains operator-provisioned identity,
endpoint/region/protocol and administrative policy; its durable status is enabled
HEALTHY/DEGRADED/OFFLINE or terminal-for-process DRAINING. Relay-scoped ApiKeys
own one node. Reporter registration cannot create or change endpoint metadata.
Self drain/activate commands persist status and a metadata-only operator audit.

Redis `portway:relay:{relayId}:presence` holds a bounded validated health/capacity
snapshot, random process instance ID, server-issued lease ID, monotonic sequence,
receipt and expiry times. Reports expire after 15 seconds. Missing/expired state
is OFFLINE, independent of seeded HEALTHY metadata. Registration retries for the
same instance preserve its lease; a new instance registers after expiry or a DRAINING report, fencing earlier
reporters. A fresh different HEALTHY/DEGRADED instance rejects replacement. An
empty Redis key permits re-registration; an extant different lease rejects stale
updates. Duplicate sequences must carry the same report and never extend expiry.
All updates/TTL/fencing are atomic Lua operations. Redis has finite connection and
command deadlines, no unbounded/offline command queue, and no memory fallback.

Assignment reads bounded live reports and durable current unexpired credential
reservations in the existing writer transaction. Admission uses the larger of
reported active tunnels and outstanding latest-generation tunnel assignments;
the requesting tunnel is excluded from its own reservation count so renewing its
latest generation does not reserve a second tunnel slot. Observed local
connection/stream/retained-watermark saturation also excludes the node. Snapshot
capacity is advisory; relay hard socket/stream/registry limits are authoritative.
A relay's reported observations never grant credentials or tenant access. Redis
loss prevents new control assignments until nodes report again; already admitted
sessions keep their local lease. Presence is not durable identity or live tunnel
ownership consensus. API memory mode uses bounded process-local presence only.

## 16. Phase 12 domain ownership

Domain stays scoped through its non-null tunnel association for new records.
Add nullable `verificationHash` (lowercase SHA-256 hex of the full DNS TXT proof)
and `verificationExpiresAt` (24h challenge deadline). These fields bind persisted
ownership verification across API restarts without storing raw challenges.
Legacy records with neither field cannot acquire routing authority; existing
legacy domain rows become DISABLED during migration. Both fields are present or
absent together; VERIFIED/ACTIVE require verifiedAt and a proof hash. New hostname
values are canonical ASCII DNS names, unique across enabled/disabled reservations.
A `(tunnelId,id)` index serves scoped keyset listing, replacing the tunnel-only
index; `(status,id)` serves bounded enabled-domain snapshots. Domain mutation,
challenge rotation and operator metadata audit use existing writer transactions.
DNS lookup happens outside transactions and commits only after a current hash,
expiry, state and authorization recheck. Private certificates/CA keys remain in
operator-managed private files, never in application tables or API responses.
