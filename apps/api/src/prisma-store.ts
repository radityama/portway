import {
  choose,
  effective,
  parseReport,
  incarnation,
  sequence,
  acknowledgement,
  type PresenceStore,
} from './presence.ts';
import { Prisma } from '@prisma/client';
import type {
  ApiKey as DbKey,
  ApiSession as DbSession,
  Project as DbProject,
  Tunnel as DbTunnel,
  Relay as DbRelay,
} from '@prisma/client';
import type {
  ControlBackend,
  Awaitable,
  MutationResult,
  TunnelFilters,
} from './backend.ts';
import type {
  Assignment,
  Principal,
  Role,
  Project,
  Tunnel,
  Relay,
} from './models.ts';
import { ControlStore, digest, opaque, secret } from './store.ts';
import {
  ApiFailure,
  fields,
  generation,
  id,
  invalid,
  loopback,
  port,
  slug,
  text,
  token,
} from './validation.ts';
import { Database, type Transaction } from './database.ts';
import { CursorCodec } from './cursor.ts';
const MAX_GENERATION = 18446744073709551615n;
const projectDto = (p: DbProject): Project => ({
  ...p,
  createdAt: p.createdAt.toISOString(),
  updatedAt: p.updatedAt.toISOString(),
});
const tunnelDto = (t: DbTunnel): Tunnel => ({
  ...t,
  protocol: t.protocol as 'http',
  generation: t.generation.toFixed(0),
  createdAt: t.createdAt.toISOString(),
  updatedAt: t.updatedAt.toISOString(),
  lastConnectedAt: t.lastConnectedAt?.toISOString() ?? null,
});
const relayDto = (r: DbRelay): Relay => ({
  id: r.id,
  name: r.name,
  region: r.region,
  hostname: r.hostname,
  port: r.port,
  protocol: r.protocol as 'tls',
  status: r.status,
  lastSeenAt: r.lastSeenAt?.toISOString() ?? null,
});
function keyPolicy(key: DbKey | null, now: number) {
  if (!key) throw new ApiFailure(401, 'AUTH_INVALID', 'Authentication failed');
  if (key.revokedAt)
    throw new ApiFailure(401, 'AUTH_REVOKED', 'Authentication revoked');
  if (key.expiresAt && key.expiresAt.getTime() <= now)
    throw new ApiFailure(401, 'AUTH_EXPIRED', 'Authentication expired');
  return key;
}
function sessionPolicy(session: DbSession, now: number) {
  if (session.revokedAt)
    throw new ApiFailure(401, 'AUTH_REVOKED', 'Authentication revoked');
  if (session.expiresAt.getTime() <= now)
    throw new ApiFailure(401, 'AUTH_EXPIRED', 'Authentication expired');
}
export class PrismaStore implements ControlBackend {
  readonly storage = 'postgres' as const;
  private readonly guard: ControlStore;
  private readonly cursors: CursorCodec;
  readonly database: Database;
  readonly presence: PresenceStore;
  constructor(
    database: Database,
    presence: PresenceStore,
    options: ConstructorParameters<typeof ControlStore>[0] = {},
  ) {
    if (options.seed) throw new Error('Database provisioning must be explicit');
    this.database = database;
    this.presence = presence;
    this.guard = new ControlStore(options);
    this.cursors = new CursorCodec(this.guard.now);
  }
  private now() {
    return this.guard.now();
  }
  rate(key: string, limit = 120) {
    this.guard.rate(key, limit);
  }
  private async identity(tx: Transaction, hash: string): Promise<Principal> {
    const session = await tx.apiSession.findUnique({
      where: { tokenHash: hash },
      include: { apiKey: true },
    });
    if (session) sessionPolicy(session, this.now());
    const key = keyPolicy(
      session?.apiKey ??
        (await tx.apiKey.findUnique({ where: { tokenHash: hash } })),
      this.now(),
    );
    if (key.relayId || !key.userId || !key.organizationId)
      throw new ApiFailure(401, 'AUTH_INVALID', 'Authentication failed');
    const m = await tx.membership.findUnique({
      where: {
        organizationId_userId: {
          organizationId: key.organizationId,
          userId: key.userId,
        },
      },
    });
    if (!m || !['OWNER', 'ADMIN', 'MEMBER', 'VIEWER'].includes(m.role))
      throw new ApiFailure(403, 'FORBIDDEN', 'Access denied');
    return {
      keyHash: key.tokenHash,
      ...(session ? { sessionHash: session.tokenHash } : {}),
      userId: key.userId,
      organizationId: key.organizationId,
      role: m.role as Role,
      expiresAt: Math.min(
        key.expiresAt?.getTime() ?? Infinity,
        session?.expiresAt.getTime() ?? Infinity,
      ),
    };
  }
  private async check(
    tx: Transaction,
    p: Principal,
    mutate = false,
    admin = false,
  ) {
    const fresh = await this.identity(tx, p.sessionHash ?? p.keyHash);
    if (
      fresh.keyHash !== p.keyHash ||
      fresh.userId !== p.userId ||
      fresh.organizationId !== p.organizationId ||
      fresh.role !== p.role ||
      (mutate &&
        (fresh.role === 'VIEWER' ||
          (admin && !['OWNER', 'ADMIN'].includes(fresh.role))))
    )
      throw new ApiFailure(403, 'FORBIDDEN', 'Access denied');
    return fresh;
  }
  private async scopedProject(
    tx: Transaction,
    p: Principal,
    projectId: string,
  ) {
    const found = await tx.project.findFirst({
      where: { id: projectId, organizationId: p.organizationId },
    });
    if (!found)
      throw new ApiFailure(404, 'PROJECT_NOT_FOUND', 'Project not found');
    return found;
  }
  private async scopedTunnel(tx: Transaction, p: Principal, tunnelId: string) {
    const found = await tx.tunnel.findFirst({
      where: { id: tunnelId, project: { organizationId: p.organizationId } },
    });
    if (!found)
      throw new ApiFailure(404, 'TUNNEL_NOT_FOUND', 'Tunnel not found');
    return found;
  }
  private async audit(
    tx: Transaction,
    p: Principal,
    action: string,
    resourceType: string,
    resourceId: string,
  ) {
    await tx.auditLog.create({
      data: {
        id: opaque('aud'),
        userId: p.userId,
        organizationId: p.organizationId,
        action,
        resourceType,
        resourceId,
      },
    });
  }
  private async sweep(tx: Transaction) {
    const now = new Date(this.now());
    await tx.idempotencyRecord.deleteMany({
      where: { expiresAt: { lte: now } },
    });
    await tx.tunnelCredential.deleteMany({
      where: { expiresAt: { lte: now } },
    });
    await tx.apiSession.deleteMany({ where: { expiresAt: { lte: now } } });
  }
  authenticate(bearer: string) {
    if (!/^[A-Za-z0-9_-]{32,512}$/.test(bearer))
      throw new ApiFailure(401, 'AUTH_INVALID', 'Authentication failed');
    return this.database.work(false, (tx) => this.identity(tx, digest(bearer)));
  }
  recheck(p: Principal) {
    return this.database.work(false, async (tx) => {
      await this.check(tx, p);
    });
  }
  mutate(p: Principal, admin = false) {
    return this.database.work(false, async (tx) => {
      await this.check(tx, p, true, admin);
    });
  }
  relayAccess(bearer: string, relayId?: string) {
    return this.database.work(false, async (tx) => {
      const key = keyPolicy(
        await tx.apiKey.findUnique({ where: { tokenHash: digest(bearer) } }),
        this.now(),
      );
      if (!key.relayId || key.userId || key.organizationId)
        throw new ApiFailure(
          401,
          'AUTH_INVALID',
          'Relay authentication failed',
        );
      if (relayId !== undefined && key.relayId !== relayId)
        throw new ApiFailure(403, 'FORBIDDEN', 'Relay access denied');
    });
  }
  relayReport(
    bearer: string,
    relayId: string,
    body: Record<string, unknown>,
    update: boolean,
  ) {
    const report = parseReport(body, update);
    return this.database.work(false, async (tx) => {
      await this.relayAccess(bearer, relayId);
      const relay = await tx.relay.findUnique({ where: { id: relayId } });
      if (!relay)
        throw new ApiFailure(404, 'RELAY_NOT_FOUND', 'Relay not found');
      const value = update
        ? await this.presence.report(
            relayId,
            report,
            incarnation(body.leaseId),
            sequence(body.sequence),
          )
        : await this.presence.register(relayId, report);
      return acknowledgement(value, relay.status === 'DRAINING');
    });
  }
  relayPolicy(bearer: string, relayId: string, drain: boolean) {
    return this.database.work(true, async (tx) => {
      await this.relayAccess(bearer, relayId);
      const old = await tx.relay.findUnique({ where: { id: relayId } });
      if (!old) throw new ApiFailure(404, 'RELAY_NOT_FOUND', 'Relay not found');
      const status = drain ? 'DRAINING' : 'HEALTHY';
      let relay = old;
      if (old.status !== status) {
        relay = await tx.relay.update({
          where: { id: relayId },
          data: { status },
        });
        await tx.auditLog.create({
          data: {
            id: opaque('aud'),
            action: drain ? 'relay.drain' : 'relay.activate',
            resourceType: 'relay',
            resourceId: relayId,
            metadata: {
              apiKeyId: (
                await tx.apiKey.findUniqueOrThrow({
                  where: { tokenHash: digest(bearer) },
                  select: { id: true },
                })
              ).id,
            },
          },
        });
      }
      return {
        relay: effective(
          relayDto(relay),
          (await this.presence.get([relayId])).get(relayId),
        ),
      };
    });
  }
  private async candidates(tx: Transaction) {
    return (
      await tx.relay.findMany({
        where: {
          status: 'HEALTHY',
          protocol: 'tls',
          apiKeys: {
            some: {
              revokedAt: null,
              OR: [
                { expiresAt: null },
                { expiresAt: { gt: new Date(this.now()) } },
              ],
            },
          },
        },
        orderBy: { id: 'asc' },
        take: 4096,
      })
    ).map(relayDto);
  }
  ready() {
    return this.database.work(false, async (tx) => {
      const now = new Date(this.now());
      const [result] = await tx.$queryRaw<{ ready: boolean }[]>`
   SELECT EXISTS (
    SELECT 1 FROM "ApiKey" k JOIN "Membership" m
     ON m."userId"=k."userId" AND m."organizationId"=k."organizationId"
    WHERE k."relayId" IS NULL AND k."revokedAt" IS NULL
     AND (k."expiresAt" IS NULL OR k."expiresAt">${now})
     AND m.role IN ('OWNER','ADMIN','MEMBER')
   ) AND EXISTS (
    SELECT 1 FROM "Relay" r JOIN "ApiKey" k ON k."relayId"=r.id
    WHERE r.status='HEALTHY' AND r.protocol='tls' AND k."revokedAt" IS NULL
     AND (k."expiresAt" IS NULL OR k."expiresAt">${now})
   ) AS ready`;
      if (!result?.ready) return false;
      const relays = await this.candidates(tx),
        live = await this.presence.get(relays.map((r) => r.id));
      return relays.some(
        (r) => effective(r, live.get(r.id)).status === 'HEALTHY',
      );
    });
  }
  profile(p: Principal) {
    return this.database.work(false, async (tx) => {
      await this.check(tx, p);
      const user = await tx.user.findUniqueOrThrow({ where: { id: p.userId } });
      const org = await tx.organization.findUniqueOrThrow({
        where: { id: p.organizationId },
      });
      return {
        user: {
          ...user,
          createdAt: user.createdAt.toISOString(),
          updatedAt: user.updatedAt.toISOString(),
        },
        organization: { id: org.id, name: org.name, slug: org.slug },
        role: p.role,
      };
    });
  }
  login(raw: string) {
    token(raw);
    return this.database.work(true, async (tx) => {
      const p = await this.identity(tx, digest(raw));
      if (p.sessionHash)
        throw new ApiFailure(401, 'AUTH_INVALID', 'An API key is required');
      await this.sweep(tx);
      this.guard.capacity(
        await tx.apiSession.count({
          where: { revokedAt: null, expiresAt: { gt: new Date(this.now()) } },
        }),
        this.guard.limits.sessions,
      );
      const key = await tx.apiKey.findUniqueOrThrow({
        where: { tokenHash: p.keyHash },
      });
      const accessToken = secret(),
        expiresAt = new Date(Math.min(this.now() + 3600_000, p.expiresAt));
      const session = await tx.apiSession.create({
        data: {
          id: opaque('ses'),
          tokenHash: digest(accessToken),
          apiKeyId: key.id,
          expiresAt,
        },
      });
      await this.audit(tx, p, 'auth.login', 'session', session.id);
      return { session: { accessToken, expiresAt: expiresAt.toISOString() } };
    });
  }
  logout(p: Principal) {
    return this.database.work(true, async (tx) => {
      await this.check(tx, p);
      if (p.sessionHash) {
        const session = await tx.apiSession.update({
          where: { tokenHash: p.sessionHash },
          data: { revokedAt: new Date(this.now()) },
        });
        await this.audit(tx, p, 'auth.logout', 'session', session.id);
      } else {
        const key = await tx.apiKey.update({
          where: { tokenHash: p.keyHash },
          data: { revokedAt: new Date(this.now()) },
        });
        await this.audit(tx, p, 'auth.logout', 'apiKey', key.id);
      }
    });
  }
  project(p: Principal, projectId: string) {
    return this.database.work(false, async (tx) => {
      await this.check(tx, p);
      return projectDto(await this.scopedProject(tx, p, projectId));
    });
  }
  tunnel(p: Principal, tunnelId: string) {
    return this.database.work(false, async (tx) => {
      await this.check(tx, p);
      return tunnelDto(await this.scopedTunnel(tx, p, tunnelId));
    });
  }
  relay(relayId: string) {
    return this.database.work(false, async (tx) => {
      const r = await tx.relay.findUnique({ where: { id: relayId } });
      if (!r) throw new ApiFailure(404, 'RELAY_NOT_FOUND', 'Relay not found');
      return effective(
        relayDto(r),
        (await this.presence.get([relayId])).get(relayId),
      );
    });
  }
  listProjects(p: Principal, cursor: string | undefined, limit: number) {
    return this.database.work(false, async (tx) => {
      await this.check(tx, p);
      const after = this.cursors.read(p, 'projects', cursor, '');
      const where = { organizationId: p.organizationId };
      if (
        after &&
        !(await tx.project.findFirst({
          where: { ...where, id: after },
          select: { id: true },
        }))
      )
        invalid();
      const rows = await tx.project.findMany({
        where: { ...where, ...(after ? { id: { gt: after } } : {}) },
        orderBy: { id: 'asc' },
        take: limit + 1,
      });
      const items = rows.slice(0, limit).map(projectDto);
      return {
        items,
        nextCursor:
          rows.length > limit
            ? this.cursors.write(p, 'projects', items.at(-1)!.id, '')
            : null,
      };
    });
  }
  listTunnels(
    p: Principal,
    filters: TunnelFilters,
    cursor: string | undefined,
    limit: number,
  ) {
    return this.database.work(false, async (tx) => {
      await this.check(tx, p);
      if (filters.projectId) await this.scopedProject(tx, p, filters.projectId);
      const binding = JSON.stringify([
        filters.projectId,
        filters.status,
        filters.relayId,
      ]);
      const after = this.cursors.read(p, 'tunnels', cursor, binding);
      const where: Prisma.TunnelWhereInput = {
        project: { organizationId: p.organizationId },
        ...(filters.projectId ? { projectId: filters.projectId } : {}),
        ...(filters.relayId ? { relayId: filters.relayId } : {}),
        ...(filters.status
          ? { status: filters.status as Tunnel['status'] }
          : {}),
      };
      if (
        after &&
        !(await tx.tunnel.findFirst({
          where: { ...where, id: after },
          select: { id: true },
        }))
      )
        invalid();
      const rows = await tx.tunnel.findMany({
        where: { ...where, ...(after ? { id: { gt: after } } : {}) },
        orderBy: { id: 'asc' },
        take: limit + 1,
      });
      const items = rows.slice(0, limit).map(tunnelDto);
      return {
        items,
        nextCursor:
          rows.length > limit
            ? this.cursors.write(p, 'tunnels', items.at(-1)!.id, binding)
            : null,
      };
    });
  }
  listRelays(p: Principal, cursor: string | undefined, limit: number) {
    return this.database.work(false, async (tx) => {
      await this.check(tx, p);
      const after = this.cursors.read(p, 'relays', cursor, '');
      if (
        after &&
        !(await tx.relay.findUnique({
          where: { id: after },
          select: { id: true },
        }))
      )
        invalid();
      const rows = await tx.relay.findMany({
        where: after ? { id: { gt: after } } : {},
        orderBy: { id: 'asc' },
        take: limit + 1,
      });
      const live = await this.presence.get(
        rows.slice(0, limit).map((r) => r.id),
      );
      const items = rows
        .slice(0, limit)
        .map((r) => effective(relayDto(r), live.get(r.id)));
      return {
        items,
        nextCursor:
          rows.length > limit
            ? this.cursors.write(p, 'relays', items.at(-1)!.id, '')
            : null,
      };
    });
  }
  createProject(p: Principal, body: Record<string, unknown>) {
    return this.database.work(true, async (tx) => {
      await this.check(tx, p, true);
      fields(body, ['name', 'slug']);
      const name = text(body.name),
        projectSlug = slug(body.slug);
      if (
        await tx.project.findUnique({
          where: {
            organizationId_slug: {
              organizationId: p.organizationId,
              slug: projectSlug,
            },
          },
        })
      )
        throw new ApiFailure(
          409,
          'PROJECT_CONFLICT',
          'Project slug already exists',
        );
      this.guard.capacity(
        await tx.project.count(),
        this.guard.limits.resources,
      );
      const project = await tx.project.create({
        data: {
          id: opaque('prj'),
          organizationId: p.organizationId,
          name,
          slug: projectSlug,
        },
      });
      await this.audit(tx, p, 'project.create', 'project', project.id);
      return { project: projectDto(project) };
    });
  }
  deleteProject(p: Principal, projectId: string) {
    return this.database.work(true, async (tx) => {
      await this.check(tx, p, true, true);
      await this.scopedProject(tx, p, projectId);
      if (await tx.tunnel.count({ where: { projectId } }))
        throw new ApiFailure(409, 'PROJECT_NOT_EMPTY', 'Project has tunnels');
      await tx.project.delete({ where: { id: projectId } });
      await this.audit(tx, p, 'project.delete', 'project', projectId);
    });
  }
  createTunnel(p: Principal, body: Record<string, unknown>) {
    return this.database.work(true, async (tx) => {
      await this.check(tx, p, true);
      fields(body, [
        'projectId',
        'name',
        'slug',
        'type',
        'protocol',
        'localHost',
        'localPort',
      ]);
      const project = await this.scopedProject(tx, p, id(body.projectId));
      const name = text(body.name),
        localHost = loopback(body.localHost),
        localPort = port(body.localPort);
      if (
        !['EPHEMERAL', 'PERSISTENT'].includes(String(body.type)) ||
        body.protocol !== 'http'
      )
        invalid();
      const tunnelId = opaque('tnl'),
        tunnelSlug =
          body.slug === undefined
            ? tunnelId.replace('_', '-')
            : slug(body.slug);
      if (
        await tx.tunnel.findUnique({
          where: {
            projectId_slug: { projectId: project.id, slug: tunnelSlug },
          },
        })
      )
        throw new ApiFailure(
          409,
          'TUNNEL_CONFLICT',
          'Tunnel slug already exists',
        );
      this.guard.capacity(await tx.tunnel.count(), this.guard.limits.resources);
      const tunnel = await tx.tunnel.create({
        data: {
          id: tunnelId,
          projectId: project.id,
          name,
          slug: tunnelSlug,
          type: body.type as Tunnel['type'],
          protocol: 'http',
          localHost,
          localPort,
          publicHostname: this.guard.publicHostname(tunnelId),
        },
      });
      await this.audit(tx, p, 'tunnel.create', 'tunnel', tunnelId);
      return { tunnel: tunnelDto(tunnel) };
    });
  }
  revoke(p: Principal, tunnelId: string) {
    return this.database.work(true, async (tx) => {
      await this.check(tx, p, true);
      await this.scopedTunnel(tx, p, tunnelId);
      const tunnel = await tx.tunnel.update({
        where: { id: tunnelId },
        data: { status: 'REVOKED' },
      });
      await tx.tunnelCredential.updateMany({
        where: { tunnelId, revokedAt: null },
        data: { revokedAt: new Date(this.now()) },
      });
      await this.audit(tx, p, 'tunnel.revoke', 'tunnel', tunnelId);
      return { tunnel: tunnelDto(tunnel) };
    });
  }
  connect(
    p: Principal,
    tunnelId: string,
    body: Record<string, unknown>,
  ): Promise<Assignment> {
    return this.database.work(true, async (tx) => {
      const fresh = await this.check(tx, p, true),
        tunnel = await this.scopedTunnel(tx, p, tunnelId);
      fields(body, ['minimumGeneration', 'avoidRelayId']);
      const avoid =
        body.avoidRelayId === undefined ? undefined : id(body.avoidRelayId);
      const minimum =
        body.minimumGeneration === undefined
          ? 1n
          : BigInt(generation(body.minimumGeneration));
      if (tunnel.status === 'REVOKED')
        throw new ApiFailure(409, 'TUNNEL_REVOKED', 'Tunnel revoked');
      const now = new Date(this.now());
      const candidates = await this.candidates(tx);
      const counts = await tx.$queryRaw<{ relayId: string; count: number }[]>`
        SELECT t."relayId",count(DISTINCT t.id)::int AS count FROM "Tunnel" t
        JOIN "TunnelCredential" c ON c."tunnelId"=t.id AND c.generation=t.generation AND c."relayId"=t."relayId"
        WHERE t.status<>'REVOKED' AND t.id<>${tunnelId} AND c."revokedAt" IS NULL AND c."expiresAt">${now} GROUP BY t."relayId"`;
      const relay = choose(
        candidates,
        await this.presence.get(candidates.map((r) => r.id)),
        new Map(counts.map((c) => [c.relayId, c.count])),
        tunnel.relayId,
        avoid,
      );
      if (!relay)
        throw new ApiFailure(503, 'RELAY_UNAVAILABLE', 'Relay unavailable');
      const next = BigInt(tunnel.generation.toFixed(0)) + 1n,
        selected = next > minimum ? next : minimum;
      if (selected > MAX_GENERATION)
        throw new ApiFailure(
          409,
          'GENERATION_EXHAUSTED',
          'Tunnel generation exhausted',
        );
      const publicHostname =
        tunnel.publicHostname ?? this.guard.publicHostname(tunnelId);
      if (publicHostname !== this.guard.publicHostname(tunnelId))
        throw new ApiFailure(
          503,
          'STORAGE_UNAVAILABLE',
          'Tunnel hostname configuration mismatch',
        );
      await this.sweep(tx);
      const active = { revokedAt: null, expiresAt: { gt: now } };
      this.guard.capacity(
        await tx.tunnelCredential.count({ where: active }),
        this.guard.limits.credentials,
      );
      this.guard.capacity(
        await tx.tunnelCredential.count({ where: { ...active, tunnelId } }),
        16,
      );
      const parent = await tx.apiKey.findUniqueOrThrow({
          where: { tokenHash: p.keyHash },
        }),
        session = p.sessionHash
          ? await tx.apiSession.findUniqueOrThrow({
              where: { tokenHash: p.sessionHash },
            })
          : null;
      const raw = secret(),
        expiresAt = new Date(
          Math.min(this.now() + this.guard.credentialTTL, fresh.expiresAt),
        );
      const issuedAt = new Date(this.now());
      if (expiresAt <= issuedAt)
        throw new ApiFailure(401, 'AUTH_EXPIRED', 'Authentication expired');
      const credential = await tx.tunnelCredential.create({
        data: {
          id: opaque('cred'),
          tunnelId,
          tokenHash: digest(raw),
          scope: 'connect',
          issuedAt,
          expiresAt,
          relayId: relay.id,
          generation: selected.toString(),
          apiKeyId: parent.id,
          sessionId: session?.id ?? null,
        },
      });
      await tx.tunnel.update({
        where: { id: tunnelId },
        data: {
          generation: selected.toString(),
          relayId: relay.id,
          status: 'CONNECTING',
          publicHostname,
        },
      });
      await this.audit(tx, p, 'tunnel.connect', 'tunnel', tunnelId);
      return {
        relay,
        credential: {
          id: credential.id,
          tunnelId,
          scope: 'connect',
          issuedAt: issuedAt.toISOString(),
          expiresAt: expiresAt.toISOString(),
          token: raw,
        },
        generation: selected.toString(),
        publicHostname,
      };
    });
  }
  verify(bearer: string, relayId: string, tokenHash: string) {
    return this.database.work(false, async (tx) => {
      const relayKey = keyPolicy(
        await tx.apiKey.findUnique({ where: { tokenHash: digest(bearer) } }),
        this.now(),
      );
      if (!relayKey.relayId || relayKey.userId || relayKey.organizationId)
        throw new ApiFailure(
          401,
          'AUTH_INVALID',
          'Relay authentication failed',
        );
      if (relayKey.relayId !== relayId)
        throw new ApiFailure(403, 'FORBIDDEN', 'Relay access denied');
      const c = await tx.tunnelCredential.findUnique({
        where: { tokenHash },
        include: {
          tunnel: { include: { project: true } },
          apiKey: true,
          session: true,
        },
      });
      if (
        !c ||
        c.relayId !== relayId ||
        !c.generation ||
        !c.apiKey ||
        !c.expiresAt
      )
        throw new ApiFailure(401, 'AUTH_INVALID', 'Credential invalid');
      if (
        c.revokedAt ||
        c.tunnel.status === 'REVOKED' ||
        !c.tunnel.generation.equals(c.generation) ||
        c.tunnel.relayId !== c.relayId ||
        c.apiKey.revokedAt ||
        !c.apiKey.userId ||
        !c.apiKey.organizationId ||
        c.apiKey.relayId ||
        c.tunnel.project.organizationId !== c.apiKey.organizationId ||
        (c.sessionId &&
          (!c.session ||
            c.session.revokedAt ||
            c.session.apiKeyId !== c.apiKey.id))
      )
        throw new ApiFailure(401, 'AUTH_REVOKED', 'Credential revoked');
      const m = await tx.membership.findUnique({
        where: {
          organizationId_userId: {
            organizationId: c.apiKey.organizationId,
            userId: c.apiKey.userId,
          },
        },
      });
      if (!m || m.role === 'VIEWER')
        throw new ApiFailure(401, 'AUTH_REVOKED', 'Credential revoked');
      if (
        c.expiresAt.getTime() <= this.now() ||
        (c.apiKey.expiresAt && c.apiKey.expiresAt.getTime() <= this.now()) ||
        (c.session && c.session.expiresAt.getTime() <= this.now())
      )
        throw new ApiFailure(401, 'AUTH_EXPIRED', 'Credential expired');
      return {
        tunnelId: c.tunnelId,
        generation: c.generation.toFixed(0),
        expiresAt: c.expiresAt.toISOString(),
      };
    });
  }
  mutation(
    p: Principal,
    method: string,
    path: string,
    raw: string,
    key: string | undefined,
    operation: () => Awaitable<MutationResult>,
    secretResult = false,
    resource?: string,
  ) {
    return this.database.work(true, async (tx) => {
      await this.check(tx, p);
      if (
        key !== undefined &&
        !/^[!#$%&'*+\-.^_\x60|~A-Za-z0-9]{8,255}$/.test(key)
      )
        invalid();
      await this.sweep(tx);
      const identifier =
          key === undefined
            ? undefined
            : digest(
                `${p.keyHash}:${p.sessionHash ?? ''}:${method}:${path}:${key}`,
              ),
        fingerprint = digest(raw);
      const prior = identifier
        ? await tx.idempotencyRecord.findUnique({ where: { id: identifier } })
        : null;
      if (prior) {
        if (prior.requestHash !== fingerprint)
          throw new ApiFailure(
            409,
            'IDEMPOTENCY_CONFLICT',
            'Idempotency key already used',
          );
        if (prior.resourceId && prior.resourceType === 'tunnel') {
          const tunnel = await this.scopedTunnel(tx, p, prior.resourceId);
          if (path.endsWith('/connect') && tunnel.status === 'REVOKED')
            throw new ApiFailure(409, 'TUNNEL_REVOKED', 'Tunnel revoked');
        } else if (
          prior.resourceId &&
          prior.resourceType === 'project' &&
          method !== 'DELETE'
        )
          await this.scopedProject(tx, p, prior.resourceId);
        if (prior.secret)
          throw new ApiFailure(
            409,
            'CREDENTIAL_ALREADY_ISSUED',
            'Credential already issued; use a new idempotency key',
          );
        return { status: prior.status, data: prior.response };
      }
      if (identifier)
        this.guard.capacity(
          await tx.idempotencyRecord.count(),
          this.guard.limits.idempotency,
        );
      const result = await operation();
      if (identifier) {
        const resultData = result.data as {
          project?: { id: string };
          tunnel?: { id: string };
        } | null;
        resource =
          resource ?? resultData?.project?.id ?? resultData?.tunnel?.id;
        const parent = await tx.apiKey.findUniqueOrThrow({
            where: { tokenHash: p.keyHash },
          }),
          session = p.sessionHash
            ? await tx.apiSession.findUniqueOrThrow({
                where: { tokenHash: p.sessionHash },
              })
            : null;
        await tx.idempotencyRecord.create({
          data: {
            id: identifier,
            apiKeyId: parent.id,
            sessionId: session?.id ?? null,
            requestHash: fingerprint,
            status: result.status,
            response:
              secretResult || result.data === null
                ? Prisma.DbNull
                : (result.data as Prisma.InputJsonValue),
            secret: secretResult,
            resourceId: resource ?? null,
            resourceType: resource
              ? path.includes('/projects')
                ? 'project'
                : 'tunnel'
              : null,
            expiresAt: new Date(this.now() + 600_000),
          },
        });
      }
      return result;
    });
  }
}
