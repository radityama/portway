import { isIP } from 'node:net';
import { createHash, createHmac, randomBytes } from 'node:crypto';
import type {
  ApiKey,
  Assignment,
  Credential,
  Membership,
  Organization,
  Principal,
  Project,
  Relay,
  RelayKey,
  Seed,
  Tunnel,
  User,
} from './models.ts';
import {
  ApiFailure,
  fields,
  generation,
  hash,
  hostname,
  id,
  invalid,
  loopback,
  object,
  port,
  slug,
  text,
  timestamp,
  token,
} from './validation.ts';

export const digest = (value: string) =>
  createHash('sha256').update(value).digest('hex');
const opaque = (prefix: string) =>
  `${prefix}_${randomBytes(16).toString('hex')}`;
const secret = () => randomBytes(32).toString('base64url');
const emptySeed: Seed = {
  users: [],
  organizations: [],
  memberships: [],
  apiKeys: [],
  relays: [],
  relayKeys: [],
};
type Session = { keyHash: string; expiresAt: number; revoked: boolean };
type Idempotency = {
  fingerprint: string;
  expiresAt: number;
  status: number;
  data: unknown;
  secret: boolean;
  resource?: string;
};
export type Limits = {
  resources: number;
  credentials: number;
  sessions: number;
  idempotency: number;
  audit: number;
  buckets: number;
};
export type Audit = {
  action: string;
  resourceId: string;
  organizationId: string;
  userId: string;
  createdAt: string;
};

export class ControlStore {
  readonly users = new Map<string, User>();
  readonly organizations = new Map<string, Organization>();
  readonly memberships: Membership[] = [];
  readonly keys = new Map<string, ApiKey>();
  readonly projects = new Map<string, Project>();
  readonly tunnels = new Map<string, Tunnel>();
  readonly relays = new Map<string, Relay>();
  readonly relayKeys = new Map<string, RelayKey>();
  readonly credentials = new Map<string, Credential>();
  readonly sessions = new Map<string, Session>();
  readonly idempotency = new Map<string, Idempotency>();
  readonly audit: Audit[] = [];
  readonly buckets = new Map<string, { start: number; count: number }>();
  readonly cursorKey = randomBytes(32);
  readonly limits: Limits;
  readonly baseDomain: string;
  readonly credentialTTL: number;
  readonly now: () => number;
  constructor(
    options: {
      seed?: Seed;
      baseDomain?: string;
      credentialTTL?: number;
      now?: () => number;
      limits?: Partial<Limits>;
    } = {},
  ) {
    this.now = options.now ?? Date.now;
    this.baseDomain = hostname(options.baseDomain ?? 'portway.localhost');
    if (
      this.baseDomain.length > 218 ||
      isIP(this.baseDomain) ||
      !this.baseDomain.includes('.')
    )
      invalid();
    this.credentialTTL = options.credentialTTL ?? 300_000;
    if (
      !Number.isInteger(this.credentialTTL) ||
      this.credentialTTL < 1000 ||
      this.credentialTTL > 900_000
    )
      invalid();
    this.limits = {
      resources: 4096,
      credentials: 4096,
      sessions: 1024,
      idempotency: 4096,
      audit: 4096,
      buckets: 4096,
      ...options.limits,
    };
    if (
      Object.values(this.limits).some(
        (n) => !Number.isInteger(n) || n < 1 || n > 100_000,
      )
    )
      invalid();
    this.load(options.seed ?? emptySeed);
  }
  load(seed: Seed) {
    const source = object(structuredClone(seed));
    fields(source, [
      'users',
      'organizations',
      'memberships',
      'apiKeys',
      'projects',
      'tunnels',
      'relays',
      'relayKeys',
    ]);
    const array = (name: string, optional = false) => {
      const value = source[name] ?? (optional ? [] : undefined);
      if (!Array.isArray(value) || value.length > this.limits.resources)
        return invalid();
      return value.map(object);
    };
    for (const u of array('users')) {
      fields(u, ['id', 'email', 'displayName', 'createdAt', 'updatedAt']);
      id(u.id);
      text(u.email, 254);
      if (!String(u.email).includes('@')) invalid();
      if (u.displayName != null) text(u.displayName);
      timestamp(u.createdAt);
      timestamp(u.updatedAt);
      this.insert(this.users, u.id as string, u as User);
    }
    for (const o of array('organizations')) {
      fields(o, ['id', 'name', 'slug']);
      id(o.id);
      text(o.name);
      slug(o.slug);
      this.insert(this.organizations, o.id as string, o as Organization);
    }
    for (const m of array('memberships')) {
      fields(m, ['userId', 'organizationId', 'role']);
      if (
        !this.users.has(id(m.userId)) ||
        !this.organizations.has(id(m.organizationId)) ||
        !['OWNER', 'ADMIN', 'MEMBER', 'VIEWER'].includes(String(m.role)) ||
        this.memberships.some(
          (old) =>
            old.userId === m.userId && old.organizationId === m.organizationId,
        )
      )
        invalid();
      this.memberships.push(m as Membership);
    }
    for (const k of array('apiKeys')) {
      fields(k, [
        'id',
        'userId',
        'organizationId',
        'tokenHash',
        'expiresAt',
        'revokedAt',
      ]);
      id(k.id);
      hash(k.tokenHash);
      timestamp(k.expiresAt);
      if (k.revokedAt != null) timestamp(k.revokedAt);
      if (
        !this.memberships.some(
          (m) => m.userId === k.userId && m.organizationId === k.organizationId,
        )
      )
        invalid();
      this.insert(this.keys, k.tokenHash as string, k as ApiKey);
    }
    for (const r of array('relays')) {
      fields(r, [
        'id',
        'name',
        'region',
        'hostname',
        'port',
        'protocol',
        'status',
        'lastSeenAt',
      ]);
      id(r.id);
      text(r.name);
      text(r.region);
      hostname(r.hostname);
      port(r.port);
      if (
        r.protocol !== 'tls' ||
        !['HEALTHY', 'DEGRADED', 'DRAINING', 'OFFLINE'].includes(
          String(r.status),
        )
      )
        invalid();
      if (r.lastSeenAt != null) timestamp(r.lastSeenAt);
      this.insert(this.relays, r.id as string, r as Relay);
    }
    for (const k of array('relayKeys')) {
      fields(k, ['relayId', 'tokenHash', 'expiresAt', 'revokedAt']);
      if (!this.relays.has(id(k.relayId)) || this.keys.has(hash(k.tokenHash)))
        invalid();
      timestamp(k.expiresAt);
      if (k.revokedAt != null) timestamp(k.revokedAt);
      this.insert(this.relayKeys, k.tokenHash as string, k as RelayKey);
    }
    for (const p of array('projects', true)) {
      fields(p, [
        'id',
        'organizationId',
        'name',
        'slug',
        'createdAt',
        'updatedAt',
      ]);
      id(p.id);
      text(p.name);
      slug(p.slug);
      timestamp(p.createdAt);
      timestamp(p.updatedAt);
      if (
        !this.organizations.has(id(p.organizationId)) ||
        [...this.projects.values()].some(
          (old) =>
            old.organizationId === p.organizationId && old.slug === p.slug,
        )
      )
        invalid();
      this.insert(this.projects, p.id as string, p as Project);
    }
    for (const t of array('tunnels', true)) {
      fields(t, [
        'id',
        'projectId',
        'name',
        'slug',
        'type',
        'status',
        'protocol',
        'localHost',
        'localPort',
        'publicHostname',
        'relayId',
        'generation',
        'createdAt',
        'updatedAt',
        'lastConnectedAt',
      ]);
      id(t.id);
      slug(t.slug);
      text(t.name);
      loopback(t.localHost);
      port(t.localPort);
      generation(t.generation, true);
      timestamp(t.createdAt);
      timestamp(t.updatedAt);
      if (
        !this.projects.has(id(t.projectId)) ||
        t.protocol !== 'http' ||
        !['EPHEMERAL', 'PERSISTENT'].includes(String(t.type)) ||
        ![
          'CREATED',
          'CONNECTING',
          'CONNECTED',
          'DISCONNECTED',
          'DRAINING',
          'REVOKED',
        ].includes(String(t.status)) ||
        t.publicHostname !== this.publicHostname(t.id as string) ||
        (t.relayId != null && !this.relays.has(id(t.relayId))) ||
        [...this.tunnels.values()].some(
          (old) => old.projectId === t.projectId && old.slug === t.slug,
        )
      )
        invalid();
      if (t.lastConnectedAt != null) timestamp(t.lastConnectedAt);
      this.insert(this.tunnels, t.id as string, t as Tunnel);
    }
  }
  insert<T>(map: Map<string, T>, key: string, value: T) {
    if (map.has(key)) invalid();
    this.capacity(map.size, this.limits.resources);
    map.set(key, value);
  }
  ready() {
    const now = this.now();
    return (
      [...this.keys.values()].some(
        (k) =>
          !k.revokedAt &&
          timestamp(k.expiresAt) > now &&
          this.memberships.some(
            (m) =>
              m.userId === k.userId &&
              m.organizationId === k.organizationId &&
              m.role !== 'VIEWER',
          ),
      ) &&
      [...this.relays.values()].some(
        (r) =>
          r.status === 'HEALTHY' &&
          [...this.relayKeys.values()].some(
            (k) =>
              k.relayId === r.id &&
              !k.revokedAt &&
              timestamp(k.expiresAt) > now,
          ),
      )
    );
  }
  capacity(size: number, limit: number) {
    if (size >= limit)
      throw new ApiFailure(
        503,
        'CAPACITY_REACHED',
        'Control-plane capacity reached',
      );
  }
  publicHostname(tunnel: string) {
    return `p-${digest(tunnel).slice(0, 32)}.${this.baseDomain}`;
  }
  iso() {
    return new Date(this.now()).toISOString();
  }
  authenticate(bearer: string): Principal {
    if (!/^[A-Za-z0-9_-]{32,512}$/.test(bearer))
      throw new ApiFailure(401, 'AUTH_INVALID', 'Authentication failed');
    const hashed = digest(bearer);
    const session = this.sessions.get(hashed);
    if (session?.revoked)
      throw new ApiFailure(401, 'AUTH_REVOKED', 'Authentication revoked');
    if (session && session.expiresAt <= this.now())
      throw new ApiFailure(401, 'AUTH_EXPIRED', 'Authentication expired');
    const keyHash = session?.keyHash ?? hashed;
    const key = this.keys.get(keyHash);
    if (!key)
      throw new ApiFailure(401, 'AUTH_INVALID', 'Authentication failed');
    if (key.revokedAt)
      throw new ApiFailure(401, 'AUTH_REVOKED', 'Authentication revoked');
    if (timestamp(key.expiresAt) <= this.now())
      throw new ApiFailure(401, 'AUTH_EXPIRED', 'Authentication expired');
    const membership = this.memberships.find(
      (m) => m.userId === key.userId && m.organizationId === key.organizationId,
    );
    if (!membership) throw new ApiFailure(403, 'FORBIDDEN', 'Access denied');
    return {
      keyHash,
      ...(session ? { sessionHash: hashed } : {}),
      userId: key.userId,
      organizationId: key.organizationId,
      role: membership.role,
      expiresAt: Math.min(
        timestamp(key.expiresAt),
        session?.expiresAt ?? Infinity,
      ),
    };
  }
  recheck(p: Principal) {
    const key = this.keys.get(p.keyHash);
    const session = p.sessionHash
      ? this.sessions.get(p.sessionHash)
      : undefined;
    if (
      !key ||
      key.revokedAt ||
      (p.sessionHash && (!session || session.revoked))
    )
      throw new ApiFailure(401, 'AUTH_REVOKED', 'Authentication revoked');
    if (p.expiresAt <= this.now())
      throw new ApiFailure(401, 'AUTH_EXPIRED', 'Authentication expired');
    const membership = this.memberships.find(
      (m) => m.userId === p.userId && m.organizationId === p.organizationId,
    );
    if (!membership || membership.role !== p.role)
      throw new ApiFailure(403, 'FORBIDDEN', 'Access denied');
  }
  mutate(p: Principal, admin = false) {
    this.recheck(p);
    if (p.role === 'VIEWER' || (admin && !['OWNER', 'ADMIN'].includes(p.role)))
      throw new ApiFailure(403, 'FORBIDDEN', 'Access denied');
  }
  project(p: Principal, projectId: string) {
    this.recheck(p);
    const project = this.projects.get(projectId);
    if (!project || project.organizationId !== p.organizationId)
      throw new ApiFailure(404, 'PROJECT_NOT_FOUND', 'Project not found');
    return project;
  }
  tunnel(p: Principal, tunnelId: string) {
    this.recheck(p);
    const t = this.tunnels.get(tunnelId);
    const project = t && this.projects.get(t.projectId);
    if (!t || !project || project.organizationId !== p.organizationId)
      throw new ApiFailure(404, 'TUNNEL_NOT_FOUND', 'Tunnel not found');
    return t;
  }
  relay(relayId: string) {
    const relay = this.relays.get(relayId);
    if (!relay) throw new ApiFailure(404, 'RELAY_NOT_FOUND', 'Relay not found');
    return relay;
  }
  login(raw: string) {
    token(raw);
    const p = this.authenticate(raw);
    if (p.sessionHash)
      throw new ApiFailure(401, 'AUTH_INVALID', 'An API key is required');
    this.sweep();
    this.capacity(this.sessions.size, this.limits.sessions);
    const accessToken = secret();
    const expiresAt = Math.min(this.now() + 3600_000, p.expiresAt);
    this.sessions.set(digest(accessToken), {
      keyHash: p.keyHash,
      expiresAt,
      revoked: false,
    });
    return {
      session: { accessToken, expiresAt: new Date(expiresAt).toISOString() },
    };
  }
  logout(p: Principal) {
    this.recheck(p);
    if (p.sessionHash) this.sessions.get(p.sessionHash)!.revoked = true;
    else this.keys.get(p.keyHash)!.revokedAt = this.iso();
  }
  createProject(p: Principal, body: Record<string, unknown>) {
    this.mutate(p);
    fields(body, ['name', 'slug']);
    const name = text(body.name);
    const projectSlug = slug(body.slug);
    if (
      [...this.projects.values()].some(
        (old) =>
          old.organizationId === p.organizationId && old.slug === projectSlug,
      )
    )
      throw new ApiFailure(
        409,
        'PROJECT_CONFLICT',
        'Project slug already exists',
      );
    this.capacity(this.projects.size, this.limits.resources);
    const project: Project = {
      id: opaque('prj'),
      organizationId: p.organizationId,
      name,
      slug: projectSlug,
      createdAt: this.iso(),
      updatedAt: this.iso(),
    };
    this.projects.set(project.id, project);
    this.record(p, 'project.create', project.id);
    return { project };
  }
  deleteProject(p: Principal, projectId: string) {
    this.project(p, projectId);
    this.mutate(p, true);
    if ([...this.tunnels.values()].some((t) => t.projectId === projectId))
      throw new ApiFailure(409, 'PROJECT_NOT_EMPTY', 'Project has tunnels');
    this.projects.delete(projectId);
    this.record(p, 'project.delete', projectId);
  }
  createTunnel(p: Principal, body: Record<string, unknown>) {
    this.mutate(p);
    fields(body, [
      'projectId',
      'name',
      'slug',
      'type',
      'protocol',
      'localHost',
      'localPort',
    ]);
    const project = this.project(p, id(body.projectId));
    const name = text(body.name);
    const localHost = loopback(body.localHost);
    const localPort = port(body.localPort);
    if (
      !['EPHEMERAL', 'PERSISTENT'].includes(String(body.type)) ||
      body.protocol !== 'http'
    )
      invalid();
    const tunnelId = opaque('tnl');
    const tunnelSlug =
      body.slug === undefined ? tunnelId.replace('_', '-') : slug(body.slug);
    if (
      [...this.tunnels.values()].some(
        (t) => t.projectId === project.id && t.slug === tunnelSlug,
      )
    )
      throw new ApiFailure(
        409,
        'TUNNEL_CONFLICT',
        'Tunnel slug already exists',
      );
    this.capacity(this.tunnels.size, this.limits.resources);
    const tunnel: Tunnel = {
      id: tunnelId,
      projectId: project.id,
      name,
      slug: tunnelSlug,
      type: body.type as Tunnel['type'],
      protocol: 'http',
      localHost,
      localPort,
      status: 'CREATED',
      publicHostname: this.publicHostname(tunnelId),
      generation: '0',
      relayId: null,
      createdAt: this.iso(),
      updatedAt: this.iso(),
      lastConnectedAt: null,
    };
    this.tunnels.set(tunnel.id, tunnel);
    this.record(p, 'tunnel.create', tunnel.id);
    return { tunnel };
  }
  revoke(p: Principal, tunnelId: string) {
    const tunnel = this.tunnel(p, tunnelId);
    this.mutate(p);
    tunnel.status = 'REVOKED';
    tunnel.updatedAt = this.iso();
    for (const cred of this.credentials.values())
      if (cred.tunnelId === tunnelId) cred.revokedAt = this.iso();
    this.record(p, 'tunnel.revoke', tunnelId);
    return { tunnel };
  }
  connect(
    p: Principal,
    tunnelId: string,
    body: Record<string, unknown>,
  ): Assignment {
    const tunnel = this.tunnel(p, tunnelId);
    this.mutate(p);
    fields(body, ['minimumGeneration']);
    const minimum =
      body.minimumGeneration === undefined
        ? 1n
        : BigInt(generation(body.minimumGeneration));
    if (tunnel.status === 'REVOKED')
      throw new ApiFailure(409, 'TUNNEL_REVOKED', 'Tunnel revoked');
    const relay = [...this.relays.values()].find(
      (r) =>
        r.status === 'HEALTHY' &&
        r.protocol === 'tls' &&
        [...this.relayKeys.values()].some(
          (k) =>
            k.relayId === r.id &&
            !k.revokedAt &&
            timestamp(k.expiresAt) > this.now(),
        ),
    );
    if (!relay)
      throw new ApiFailure(503, 'RELAY_UNAVAILABLE', 'Relay unavailable');
    const next = BigInt(tunnel.generation) + 1n;
    const selected = next > minimum ? next : minimum;
    if (selected > 18446744073709551615n)
      throw new ApiFailure(
        409,
        'GENERATION_EXHAUSTED',
        'Tunnel generation exhausted',
      );
    this.sweep();
    this.capacity(this.credentials.size, this.limits.credentials);
    if (
      [...this.credentials.values()].filter(
        (c) =>
          c.tunnelId === tunnelId &&
          !c.revokedAt &&
          timestamp(c.expiresAt) > this.now(),
      ).length >= 16
    )
      throw new ApiFailure(
        503,
        'CAPACITY_REACHED',
        'Tunnel credential capacity reached',
      );
    const raw = secret();
    const expiresAt = new Date(
      Math.min(this.now() + this.credentialTTL, p.expiresAt),
    ).toISOString();
    const cred: Credential = {
      id: opaque('cred'),
      tunnelId,
      scope: 'connect',
      issuedAt: this.iso(),
      expiresAt,
      tokenHash: digest(raw),
      revokedAt: null,
      relayId: relay.id,
      generation: selected.toString(),
      parentKeyHash: p.keyHash,
      ...(p.sessionHash ? { parentSessionHash: p.sessionHash } : {}),
    };
    this.credentials.set(cred.tokenHash, cred);
    tunnel.generation = cred.generation;
    tunnel.relayId = relay.id;
    tunnel.status = 'CONNECTING';
    tunnel.updatedAt = this.iso();
    this.record(p, 'tunnel.connect', tunnelId);
    return {
      relay: structuredClone(relay),
      credential: {
        id: cred.id,
        tunnelId,
        scope: 'connect',
        issuedAt: cred.issuedAt,
        expiresAt,
        token: raw,
      },
      generation: cred.generation,
      publicHostname: tunnel.publicHostname,
    };
  }
  verify(relayBearer: string, relayId: string, tokenHash: string) {
    const key = this.relayKeys.get(digest(relayBearer));
    if (!key)
      throw new ApiFailure(401, 'AUTH_INVALID', 'Relay authentication failed');
    if (key.revokedAt)
      throw new ApiFailure(401, 'AUTH_REVOKED', 'Relay authentication revoked');
    if (timestamp(key.expiresAt) <= this.now())
      throw new ApiFailure(401, 'AUTH_EXPIRED', 'Relay authentication expired');
    if (key.relayId !== relayId)
      throw new ApiFailure(403, 'FORBIDDEN', 'Relay access denied');
    const cred = this.credentials.get(tokenHash);
    if (!cred || cred.relayId !== relayId)
      throw new ApiFailure(401, 'AUTH_INVALID', 'Credential invalid');
    const tunnel = this.tunnels.get(cred.tunnelId);
    const parent = this.keys.get(cred.parentKeyHash);
    const session = cred.parentSessionHash
      ? this.sessions.get(cred.parentSessionHash)
      : undefined;
    if (
      !tunnel ||
      tunnel.status === 'REVOKED' ||
      cred.revokedAt ||
      !parent ||
      parent.revokedAt ||
      (cred.parentSessionHash && (!session || session.revoked)) ||
      !this.memberships.some(
        (m) =>
          m.userId === parent.userId &&
          m.organizationId === parent.organizationId &&
          m.role !== 'VIEWER',
      )
    )
      throw new ApiFailure(401, 'AUTH_REVOKED', 'Credential revoked');
    if (
      timestamp(cred.expiresAt) <= this.now() ||
      timestamp(parent.expiresAt) <= this.now() ||
      (session && session.expiresAt <= this.now())
    )
      throw new ApiFailure(401, 'AUTH_EXPIRED', 'Credential expired');
    return {
      tunnelId: cred.tunnelId,
      generation: cred.generation,
      expiresAt: cred.expiresAt,
    };
  }
  record(p: Principal, action: string, resourceId: string) {
    if (this.audit.length >= this.limits.audit) this.audit.shift();
    this.audit.push({
      action,
      resourceId,
      organizationId: p.organizationId,
      userId: p.userId,
      createdAt: this.iso(),
    });
  }
  sweep() {
    const now = this.now();
    for (const [k, v] of this.sessions)
      if (v.expiresAt <= now) this.sessions.delete(k);
    for (const [k, v] of this.credentials)
      if (timestamp(v.expiresAt) <= now) this.credentials.delete(k);
    for (const [k, v] of this.idempotency)
      if (v.expiresAt <= now) this.idempotency.delete(k);
    for (const [k, v] of this.buckets)
      if (v.start + 60_000 <= now) this.buckets.delete(k);
  }
  rate(key: string) {
    const now = this.now();
    const old = this.buckets.get(key);
    if (old && old.start + 60_000 > now) {
      if (++old.count > 120)
        throw new ApiFailure(429, 'RATE_LIMITED', 'Request rate exceeded');
      return;
    }
    this.sweep();
    this.capacity(this.buckets.size, this.limits.buckets);
    this.buckets.set(key, { start: now, count: 1 });
  }
  mutation(
    p: Principal,
    method: string,
    path: string,
    raw: string,
    key: string | undefined,
    operation: () => { status: number; data: unknown },
    secretResult = false,
    resource?: string,
  ) {
    this.recheck(p);
    if (
      key !== undefined &&
      !/^[!#$%&'*+\-.^_\x60|~A-Za-z0-9]{8,255}$/.test(key)
    )
      invalid();
    this.sweep();
    const identifier =
      key === undefined
        ? ''
        : `${p.keyHash}:${p.sessionHash ?? ''}:${method}:${path}:${key}`;
    const fingerprint = digest(raw);
    const prior = identifier ? this.idempotency.get(identifier) : undefined;
    if (prior) {
      if (prior.fingerprint !== fingerprint)
        throw new ApiFailure(
          409,
          'IDEMPOTENCY_CONFLICT',
          'Idempotency key already used',
        );
      if (prior.resource?.startsWith('tnl_')) {
        const tunnel = this.tunnel(p, prior.resource);
        if (tunnel.status === 'REVOKED' && path.endsWith('/connect'))
          throw new ApiFailure(409, 'TUNNEL_REVOKED', 'Tunnel revoked');
      } else if (prior.resource?.startsWith('prj_') && method !== 'DELETE')
        this.project(p, prior.resource);
      if (prior.secret)
        throw new ApiFailure(
          409,
          'CREDENTIAL_ALREADY_ISSUED',
          'Credential already issued; use a new idempotency key',
        );
      return { status: prior.status, data: structuredClone(prior.data) };
    }
    if (identifier)
      this.capacity(this.idempotency.size, this.limits.idempotency);
    const result = operation();
    const resultData = result.data as {
      project?: { id: string };
      tunnel?: { id: string };
    } | null;
    resource = resource ?? resultData?.project?.id ?? resultData?.tunnel?.id;
    if (identifier)
      this.idempotency.set(identifier, {
        fingerprint,
        expiresAt: this.now() + 600_000,
        status: result.status,
        data: secretResult ? null : structuredClone(result.data),
        secret: secretResult,
        ...(resource ? { resource } : {}),
      });
    return result;
  }
  page<T extends { id: string }>(
    p: Principal,
    kind: string,
    values: T[],
    cursor: string | undefined,
    limit: number,
    filters: string,
  ) {
    this.recheck(p);
    const binding = `${p.keyHash}:${p.sessionHash ?? ''}:${kind}:${filters}`;
    let after = '';
    if (cursor !== undefined) {
      if (
        cursor.length > 1024 ||
        !/^[A-Za-z0-9_-]+\.[a-f0-9]{64}$/.test(cursor)
      )
        invalid();
      const [payload, signature] = cursor.split('.');
      if (
        createHmac('sha256', this.cursorKey)
          .update(binding + ':' + payload)
          .digest('hex') !== signature
      )
        invalid();
      let decoded: unknown;
      try {
        decoded = JSON.parse(Buffer.from(payload!, 'base64url').toString());
      } catch {
        return invalid();
      }
      const value = object(decoded);
      fields(value, ['id', 'expiresAt']);
      after = id(value.id);
      if (
        typeof value.expiresAt !== 'number' ||
        value.expiresAt <= this.now() ||
        !values.some((v) => v.id === after)
      )
        invalid();
    }
    values = values.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
    const selected = values.filter((v) => v.id > after);
    const items = selected.slice(0, limit);
    let nextCursor: string | null = null;
    if (selected.length > limit) {
      const payload = Buffer.from(
        JSON.stringify({
          id: items.at(-1)!.id,
          expiresAt: this.now() + 600_000,
        }),
      ).toString('base64url');
      nextCursor =
        payload +
        '.' +
        createHmac('sha256', this.cursorKey)
          .update(binding + ':' + payload)
          .digest('hex');
    }
    return { items: structuredClone(items), nextCursor };
  }
}
