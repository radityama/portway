import { Prisma, type PrismaClient } from '@prisma/client';
import type { Seed } from './models.ts';
import { ControlStore, digest } from './store.ts';

// Create-only provisioning deliberately does not reset runtime state. A conflict
// rolls back the entire import, including dependent records already inserted.
export async function provision(
  client: PrismaClient,
  seed: Seed,
  baseDomain = 'portway.localhost',
) {
  const validated = new ControlStore({ seed, baseDomain });
  await client.$transaction(
    async (tx) => {
      await tx.$executeRaw`SET LOCAL statement_timeout = '3000ms'`;
      await tx.$executeRaw`SET LOCAL lock_timeout = '2000ms'`;
      await tx.$queryRaw`SELECT pg_advisory_xact_lock(1347375700, 10)::text`;
      for (const user of validated.users.values()) {
        const old = await tx.user.findUnique({ where: { id: user.id } });
        if (old) {
          if (old.email !== user.email)
            throw new Error('Provisioning identity conflict');
          continue;
        }
        await tx.user.create({
          data: {
            ...user,
            createdAt: new Date(user.createdAt),
            updatedAt: new Date(user.updatedAt),
          },
        });
      }
      for (const org of validated.organizations.values()) {
        const old = await tx.organization.findUnique({ where: { id: org.id } });
        if (old) {
          if (old.slug !== org.slug)
            throw new Error('Provisioning organization conflict');
          continue;
        }
        await tx.organization.create({ data: org });
      }
      for (const membership of validated.memberships) {
        const old = await tx.membership.findUnique({
          where: {
            organizationId_userId: {
              organizationId: membership.organizationId,
              userId: membership.userId,
            },
          },
        });
        // Existing role changes are durable policy, never overwritten by a seed.
        if (!old)
          await tx.membership.create({
            data: {
              ...membership,
              id:
                'mem_' +
                digest(
                  membership.organizationId + ':' + membership.userId,
                ).slice(0, 32),
            },
          });
      }
      for (const relay of validated.relays.values())
        if (!(await tx.relay.findUnique({ where: { id: relay.id } })))
          await tx.relay.create({
            data: {
              ...relay,
              lastSeenAt: relay.lastSeenAt ? new Date(relay.lastSeenAt) : null,
            },
          });
      for (const project of validated.projects.values()) {
        const old = await tx.project.findUnique({ where: { id: project.id } });
        if (old) {
          if (old.organizationId !== project.organizationId)
            throw new Error('Provisioning project conflict');
          continue;
        }
        await tx.project.create({
          data: {
            ...project,
            createdAt: new Date(project.createdAt),
            updatedAt: new Date(project.updatedAt),
          },
        });
      }
      const keys = [
        ...[...validated.keys.values()].map((k) => ({
          ...k,
          name: 'Provisioned user key',
          relayId: null,
        })),
        ...[...validated.relayKeys.values()].map((k) => ({
          ...k,
          id: 'key_relay_' + k.tokenHash.slice(0, 32),
          name: 'Provisioned relay key',
          userId: null,
          organizationId: null,
        })),
      ];
      for (const key of keys) {
        const old = await tx.apiKey.findUnique({ where: { id: key.id } });
        if (old) {
          if (
            old.tokenHash !== key.tokenHash ||
            old.userId !== key.userId ||
            old.organizationId !== key.organizationId ||
            old.relayId !== key.relayId
          )
            throw new Error('Provisioning key conflict');
          continue;
        }
        await tx.apiKey.create({
          data: {
            ...key,
            expiresAt: new Date(key.expiresAt),
            revokedAt: key.revokedAt ? new Date(key.revokedAt) : null,
          },
        });
      }
      for (const tunnel of validated.tunnels.values()) {
        const old = await tx.tunnel.findUnique({ where: { id: tunnel.id } });
        if (old) {
          if (old.projectId !== tunnel.projectId)
            throw new Error('Provisioning tunnel conflict');
          continue;
        }
        await tx.tunnel.create({
          data: {
            ...tunnel,
            generation: new Prisma.Decimal(tunnel.generation),
            createdAt: new Date(tunnel.createdAt),
            updatedAt: new Date(tunnel.updatedAt),
            lastConnectedAt: tunnel.lastConnectedAt
              ? new Date(tunnel.lastConnectedAt)
              : null,
          },
        });
      }
    },
    { maxWait: 2000, timeout: 10000 },
  );
}
