-- Keep migration failures atomic, including constraint/index validation.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- DropIndex
DROP INDEX "TunnelCredential_tunnelId_idx";

-- AlterTable
ALTER TABLE "Tunnel" ALTER COLUMN "generation" SET DEFAULT 0,
ALTER COLUMN "generation" SET DATA TYPE DECIMAL(20,0);

-- AlterTable
ALTER TABLE "TunnelCredential" ADD COLUMN     "apiKeyId" TEXT,
ADD COLUMN     "generation" DECIMAL(20,0),
ADD COLUMN     "relayId" TEXT,
ADD COLUMN     "sessionId" TEXT;

-- AlterTable
ALTER TABLE "ApiKey" ADD COLUMN     "relayId" TEXT;

-- CreateTable
CREATE TABLE "ApiSession" (
    "id" TEXT NOT NULL,
    "tokenHash" TEXT NOT NULL,
    "apiKeyId" TEXT NOT NULL,
    "expiresAt" TIMESTAMP(3) NOT NULL,
    "revokedAt" TIMESTAMP(3),
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "ApiSession_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "IdempotencyRecord" (
    "id" TEXT NOT NULL,
    "apiKeyId" TEXT NOT NULL,
    "sessionId" TEXT,
    "requestHash" TEXT NOT NULL,
    "status" INTEGER NOT NULL,
    "response" JSONB,
    "secret" BOOLEAN NOT NULL,
    "resourceType" TEXT,
    "resourceId" TEXT,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "expiresAt" TIMESTAMP(3) NOT NULL,

    CONSTRAINT "IdempotencyRecord_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "ApiSession_tokenHash_key" ON "ApiSession"("tokenHash");

-- CreateIndex
CREATE INDEX "ApiSession_apiKeyId_idx" ON "ApiSession"("apiKeyId");

-- CreateIndex
CREATE INDEX "ApiSession_expiresAt_idx" ON "ApiSession"("expiresAt");

-- CreateIndex
CREATE INDEX "IdempotencyRecord_apiKeyId_idx" ON "IdempotencyRecord"("apiKeyId");

-- CreateIndex
CREATE INDEX "IdempotencyRecord_expiresAt_idx" ON "IdempotencyRecord"("expiresAt");

-- CreateIndex
CREATE UNIQUE INDEX "TunnelCredential_tokenHash_key" ON "TunnelCredential"("tokenHash");

-- CreateIndex
CREATE INDEX "TunnelCredential_tunnelId_expiresAt_idx" ON "TunnelCredential"("tunnelId", "expiresAt");

-- CreateIndex
CREATE UNIQUE INDEX "ApiKey_tokenHash_key" ON "ApiKey"("tokenHash");

-- CreateIndex
CREATE INDEX "ApiKey_relayId_idx" ON "ApiKey"("relayId");

-- AddForeignKey
ALTER TABLE "TunnelCredential" ADD CONSTRAINT "TunnelCredential_relayId_fkey" FOREIGN KEY ("relayId") REFERENCES "Relay"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "TunnelCredential" ADD CONSTRAINT "TunnelCredential_apiKeyId_fkey" FOREIGN KEY ("apiKeyId") REFERENCES "ApiKey"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "TunnelCredential" ADD CONSTRAINT "TunnelCredential_sessionId_fkey" FOREIGN KEY ("sessionId") REFERENCES "ApiSession"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ApiKey" ADD CONSTRAINT "ApiKey_relayId_fkey" FOREIGN KEY ("relayId") REFERENCES "Relay"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ApiSession" ADD CONSTRAINT "ApiSession_apiKeyId_fkey" FOREIGN KEY ("apiKeyId") REFERENCES "ApiKey"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "IdempotencyRecord" ADD CONSTRAINT "IdempotencyRecord_apiKeyId_fkey" FOREIGN KEY ("apiKeyId") REFERENCES "ApiKey"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "IdempotencyRecord" ADD CONSTRAINT "IdempotencyRecord_sessionId_fkey" FOREIGN KEY ("sessionId") REFERENCES "ApiSession"("id") ON DELETE CASCADE ON UPDATE CASCADE;


-- Prisma does not represent CHECK constraints. These enforce the documented
-- logical boundary in addition to API validation and relational/unique indexes.
ALTER TABLE "Tunnel" ADD CONSTRAINT "Tunnel_generation_uint64" CHECK ("generation" BETWEEN 0 AND 18446744073709551615);
ALTER TABLE "Membership" ADD CONSTRAINT "Membership_role_valid" CHECK ("role" IN ('OWNER','ADMIN','MEMBER','VIEWER'));
ALTER TABLE "Relay" ADD CONSTRAINT "Relay_port_valid" CHECK ("port" BETWEEN 1 AND 65535);
ALTER TABLE "Tunnel" ADD CONSTRAINT "Tunnel_localPort_valid" CHECK ("localPort" IS NULL OR "localPort" BETWEEN 1 AND 65535);
ALTER TABLE "ApiKey" ADD CONSTRAINT "ApiKey_scope_valid" CHECK (
  ("relayId" IS NOT NULL AND "userId" IS NULL AND "organizationId" IS NULL) OR
  ("relayId" IS NULL AND (("userId" IS NULL AND "organizationId" IS NULL) OR ("userId" IS NOT NULL AND "organizationId" IS NOT NULL)))
);
ALTER TABLE "TunnelCredential" ADD CONSTRAINT "TunnelCredential_binding_valid" CHECK (
  ("relayId" IS NULL AND "generation" IS NULL AND "apiKeyId" IS NULL AND "sessionId" IS NULL) OR
  ("relayId" IS NOT NULL AND "generation" IS NOT NULL AND "apiKeyId" IS NOT NULL AND "expiresAt" IS NOT NULL AND "scope" = 'connect' AND "generation" BETWEEN 1 AND 18446744073709551615 AND "expiresAt" > "issuedAt")
);
ALTER TABLE "ApiKey" ADD CONSTRAINT "ApiKey_hash_valid" CHECK ("tokenHash" ~ '^[a-f0-9]{64}$');
ALTER TABLE "ApiSession" ADD CONSTRAINT "ApiSession_hash_valid" CHECK ("tokenHash" ~ '^[a-f0-9]{64}$');
ALTER TABLE "TunnelCredential" ADD CONSTRAINT "TunnelCredential_hash_valid" CHECK ("tokenHash" ~ '^[a-f0-9]{64}$');

-- Scoped keyset pages use these ordered compound indexes; their prefixes also
-- serve organization/project lookups and cascading foreign-key deletions.
DROP INDEX "Project_organizationId_idx";
CREATE INDEX "Project_organizationId_id_idx" ON "Project"("organizationId", "id");
DROP INDEX "Tunnel_projectId_idx";
CREATE INDEX "Tunnel_projectId_id_idx" ON "Tunnel"("projectId", "id");

COMMIT;
