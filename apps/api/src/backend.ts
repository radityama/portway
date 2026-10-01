import type {
  Assignment,
  Organization,
  Principal,
  Project,
  Relay,
  Tunnel,
  User,
} from './models.ts';
export type Awaitable<T> = T | Promise<T>;
export type MutationResult = { status: number; data: unknown };
export type Page<T> = { items: T[]; nextCursor: string | null };
export type TunnelFilters = {
  projectId: string | null;
  relayId: string | null;
  status: string | null;
};
export interface ControlBackend {
  readonly storage: 'memory' | 'postgres';
  rate(key: string, limit?: number): void;
  ready(): Awaitable<boolean>;
  authenticate(bearer: string): Awaitable<Principal>;
  relayAccess(bearer: string, relayId?: string): Awaitable<void>;
  relayReport(
    bearer: string,
    relayId: string,
    body: Record<string, unknown>,
    update: boolean,
  ): Awaitable<import('./presence.ts').ReportAcknowledgement>;
  relayPolicy(
    bearer: string,
    relayId: string,
    drain: boolean,
  ): Awaitable<{ relay: Relay }>;
  recheck(p: Principal): Awaitable<void>;
  mutate(p: Principal, admin?: boolean): Awaitable<void>;
  profile(p: Principal): Awaitable<{
    user: User;
    organization: Organization;
    role: Principal['role'];
  }>;
  login(
    token: string,
  ): Awaitable<{ session: { accessToken: string; expiresAt: string } }>;
  logout(p: Principal): Awaitable<void>;
  project(p: Principal, id: string): Awaitable<Project>;
  tunnel(p: Principal, id: string): Awaitable<Tunnel>;
  relay(id: string): Awaitable<Relay>;
  listProjects(
    p: Principal,
    cursor: string | undefined,
    limit: number,
  ): Awaitable<Page<Project>>;
  listTunnels(
    p: Principal,
    filters: TunnelFilters,
    cursor: string | undefined,
    limit: number,
  ): Awaitable<Page<Tunnel>>;
  listRelays(
    p: Principal,
    cursor: string | undefined,
    limit: number,
  ): Awaitable<Page<Relay>>;
  createProject(
    p: Principal,
    body: Record<string, unknown>,
  ): Awaitable<{ project: Project }>;
  deleteProject(p: Principal, id: string): Awaitable<void>;
  createTunnel(
    p: Principal,
    body: Record<string, unknown>,
  ): Awaitable<{ tunnel: Tunnel }>;
  revoke(p: Principal, id: string): Awaitable<{ tunnel: Tunnel }>;
  connect(
    p: Principal,
    id: string,
    body: Record<string, unknown>,
  ): Awaitable<Assignment>;
  verify(
    bearer: string,
    relayId: string,
    hash: string,
  ): Awaitable<{ tunnelId: string; generation: string; expiresAt: string }>;
  mutation(
    p: Principal,
    method: string,
    path: string,
    raw: string,
    key: string | undefined,
    operation: () => Awaitable<MutationResult>,
    secretResult?: boolean,
    resource?: string,
  ): Awaitable<MutationResult>;
}
