BEGIN;
SET LOCAL lock_timeout = '1s';
SET LOCAL statement_timeout = '5s';
ALTER TABLE "Domain" ADD COLUMN "verificationHash" TEXT, ADD COLUMN "verificationExpiresAt" TIMESTAMP(3);
-- Historical metadata has no verified proof; it cannot grant alias authority.
UPDATE "Domain" SET status='DISABLED', "verifiedAt"=NULL;
ALTER TABLE "Domain" ADD CONSTRAINT "Domain_proof_check" CHECK (
 ("verificationHash" IS NULL AND "verificationExpiresAt" IS NULL)
 OR ("verificationHash" IS NOT NULL AND "verificationHash" ~ '^[a-f0-9]{64}$' AND "verificationExpiresAt" IS NOT NULL)
);
ALTER TABLE "Domain" ADD CONSTRAINT "Domain_verified_check" CHECK (
 status NOT IN ('VERIFIED','ACTIVE') OR ("verificationHash" IS NOT NULL AND "verifiedAt" IS NOT NULL)
);
DROP INDEX "Domain_tunnelId_idx";
DROP INDEX "Domain_status_idx";
CREATE INDEX "Domain_tunnelId_id_idx" ON "Domain"("tunnelId",id);
CREATE INDEX "Domain_status_id_idx" ON "Domain"(status,id);
COMMIT;
