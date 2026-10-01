export const PROTOCOL_VERSION = 1 as const;
export const FRAME_HEADER_SIZE = 16 as const;
export const MAX_PAYLOAD_SIZE = 4 * 1024 * 1024;
export const MAX_HANDSHAKE_PAYLOAD_SIZE = 4096 as const;
export const MAX_CAPABILITIES = 32 as const;
export const MAX_CAPABILITY_NAME_SIZE = 64 as const;
export const MIN_TOKEN_SIZE = 32 as const;
export const MAX_TOKEN_SIZE = 512 as const;
export const MAX_TUNNEL_ID_SIZE = 128 as const;
export const MAX_DATA_SIZE = 16 * 1024;
export const MAX_OPEN_PAYLOAD_SIZE = 64 * 1024;
export const MAX_HTTP_HEADER_SIZE = 32 * 1024;
export const MAX_REQUEST_BODY_SIZE = 16 * 1024 * 1024;
export const MAX_RESPONSE_BODY_SIZE = 64 * 1024 * 1024;
export const INITIAL_STREAM_WINDOW = 64 * 1024;
export const INITIAL_CONNECTION_WINDOW = 1024 * 1024;
export const WINDOW_UPDATE_SIZE = 4 as const;
export const HEARTBEAT_INTERVAL_MS = 15000 as const;
export const HEARTBEAT_TIMEOUT_MS = 45000 as const;
export interface Heartbeat {
  nonce: string;
  timestamp: string;
}
export interface OpenStream {
  method: string;
  target: string;
  host: string;
  headers: [string, string][];
  content_length: number;
}
export const STREAM_ERROR_CODES = {
  UNAVAILABLE: 'UPSTREAM_UNAVAILABLE',
  LIMIT: 'STREAM_LIMIT',
  TIMEOUT: 'STREAM_TIMEOUT',
  CANCELLED: 'STREAM_CANCELLED',
  BODY_LIMIT: 'BODY_LIMIT',
  INVALID: 'STREAM_INVALID',
} as const;

export const REGISTER_ERROR_CODES = {
  INVALID: 'REGISTER_INVALID',
  FORBIDDEN: 'REGISTER_FORBIDDEN',
  STALE: 'REGISTER_STALE',
  CAPACITY: 'REGISTER_CAPACITY',
  CONFLICT: 'REGISTER_CONFLICT',
} as const;

// uint64 generation uses a decimal string, never a JavaScript number.
export interface Register {
  tunnel_id: string;
  generation: string;
  protocol?: 'http';
}
export interface RegisterOK extends Register {
  connection_id: string;
  public_hostname: string;
  public_url?: string;
}
export interface RegisterError {
  code: (typeof REGISTER_ERROR_CODES)[keyof typeof REGISTER_ERROR_CODES];
}

export const AUTH_ERROR_CODES = {
  INVALID: 'AUTH_INVALID',
  EXPIRED: 'AUTH_EXPIRED',
  REVOKED: 'AUTH_REVOKED',
} as const;

export interface Auth {
  token: string;
}
export interface AuthOK {
  connection_id: string;
  expires_at: string;
}
export interface AuthError {
  code: (typeof AUTH_ERROR_CODES)[keyof typeof AUTH_ERROR_CODES];
}

// Names are shared contracts; advertising one requires its implementation.
export const CAPABILITIES = {
  MULTIPLEXING: 'multiplexing',
  FLOW_CONTROL: 'flow_control',
  HEARTBEAT: 'heartbeat',
  GRACEFUL_SHUTDOWN: 'graceful_shutdown',
} as const;

export interface Hello {
  version: typeof PROTOCOL_VERSION;
  capabilities: string[];
  required_capabilities?: string[];
  max_payload_size: number;
}

export interface HelloAck {
  version: typeof PROTOCOL_VERSION;
  capabilities: string[];
  max_payload_size: number;
}

export const FRAME_TYPES = {
  HELLO: 0x01,
  HELLO_ACK: 0x02,
  AUTH: 0x03,
  AUTH_OK: 0x04,
  AUTH_ERROR: 0x05,
  REGISTER: 0x06,
  REGISTER_OK: 0x07,
  REGISTER_ERROR: 0x08,
  PING: 0x09,
  PONG: 0x0a,
  OPEN_STREAM: 0x10,
  OPEN_STREAM_OK: 0x11,
  OPEN_STREAM_ERROR: 0x12,
  DATA: 0x13,
  WINDOW_UPDATE: 0x14,
  CLOSE_STREAM: 0x15,
  RESET_STREAM: 0x16,
  GOAWAY: 0x17,
} as const;
