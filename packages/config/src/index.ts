export type TunnelConfig = {
  apiUrl?: string;
  relay?: 'auto' | string;
  protocol?: 'auto' | 'tcp' | 'quic';
  reconnect?: boolean;
  maxStreams?: number;
  maxRequestBody?: number;
};
