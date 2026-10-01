export type TunnelReadyEvent = {
  event: 'ready';
  local_url: string;
  public_url: string;
  relay: string;
};

/** Phase 9 JSON generations are decimal strings, never JavaScript numbers. */
export type ApiEnvelope<T> = {
  data: T | null;
  error: { code: string; message: string } | null;
  meta: { nextCursor?: string | null };
};
export type RelayAssignment = {
  relay: {
    id: string;
    name: string;
    region: string;
    hostname: string;
    port: number;
    protocol: 'tls';
    status: 'HEALTHY' | 'DEGRADED' | 'DRAINING' | 'OFFLINE';
    lastSeenAt: string | null;
  };
  generation: string;
  publicHostname: string;
  credential: {
    id: string;
    tunnelId: string;
    scope: 'connect';
    issuedAt: string;
    expiresAt: string;
    token: string;
  };
};
