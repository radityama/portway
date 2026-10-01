export type Role = 'OWNER' | 'ADMIN' | 'MEMBER' | 'VIEWER';
export type TunnelStatus =
  | 'CREATED'
  | 'CONNECTING'
  | 'CONNECTED'
  | 'DISCONNECTED'
  | 'DRAINING'
  | 'REVOKED';
export type User = {
  id: string;
  email: string;
  displayName?: string | null;
  createdAt: string;
  updatedAt: string;
};
export type Organization = { id: string; name: string; slug: string };
export type Membership = { userId: string; organizationId: string; role: Role };
export type ApiKey = {
  id: string;
  userId: string;
  organizationId: string;
  tokenHash: string;
  expiresAt: string;
  revokedAt?: string | null;
};
export type Project = {
  id: string;
  organizationId: string;
  name: string;
  slug: string;
  createdAt: string;
  updatedAt: string;
};
export type Tunnel = {
  id: string;
  projectId: string;
  name: string;
  slug: string;
  type: 'EPHEMERAL' | 'PERSISTENT';
  status: TunnelStatus;
  protocol: 'http';
  localHost: string;
  localPort: number;
  publicHostname: string;
  relayId: string | null;
  generation: string;
  createdAt: string;
  updatedAt: string;
  lastConnectedAt: string | null;
};
export type Relay = {
  id: string;
  name: string;
  region: string;
  hostname: string;
  port: number;
  protocol: 'tls';
  status: 'HEALTHY' | 'DEGRADED' | 'DRAINING' | 'OFFLINE';
  lastSeenAt: string | null;
};
export type RelayKey = {
  relayId: string;
  tokenHash: string;
  expiresAt: string;
  revokedAt?: string | null;
};
export type Seed = {
  users: User[];
  organizations: Organization[];
  memberships: Membership[];
  apiKeys: ApiKey[];
  projects?: Project[];
  tunnels?: Tunnel[];
  relays: Relay[];
  relayKeys: RelayKey[];
};
export type Principal = {
  keyHash: string;
  sessionHash?: string;
  userId: string;
  organizationId: string;
  role: Role;
  expiresAt: number;
};
export type Credential = {
  id: string;
  tunnelId: string;
  scope: 'connect';
  issuedAt: string;
  expiresAt: string;
  tokenHash: string;
  revokedAt: string | null;
  relayId: string;
  generation: string;
  parentKeyHash: string;
  parentSessionHash?: string;
};
export type Assignment = {
  relay: Relay;
  credential: Pick<
    Credential,
    'id' | 'tunnelId' | 'scope' | 'issuedAt' | 'expiresAt'
  > & { token: string };
  generation: string;
  publicHostname: string;
};
