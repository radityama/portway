export const PROTOCOL_VERSION = 1 as const;
export const FRAME_HEADER_SIZE = 16 as const;
export const MAX_PAYLOAD_SIZE = 4 * 1024 * 1024;
export const MAX_HANDSHAKE_PAYLOAD_SIZE = 4096 as const;
export const MAX_CAPABILITIES = 32 as const;
export const MAX_CAPABILITY_NAME_SIZE = 64 as const;

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
