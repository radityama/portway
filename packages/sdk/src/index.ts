export type TunnelReadyEvent = {
  event: 'ready';
  local_url: string;
  public_url: string;
  relay: string;
};
