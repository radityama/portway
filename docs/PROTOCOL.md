# Portway Protocol

## Status

Draft v1 for the starter repository.

## Transport

MVP transport: TLS over TCP. QUIC is a later transport implementation behind a common interface.

## Frame

Every frame uses network byte order:

```text
0               7 8              15 16             23 24             31
+----------------+----------------+----------------+----------------+
| version        | type           | flags          | reserved       |
+----------------+----------------+----------------+----------------+
|                         stream_id (64)                            |
+-------------------------------------------------------------------+
|                       payload_length (32)                         |
+-------------------------------------------------------------------+
|                            payload ...                             |
+-------------------------------------------------------------------+
```

Constraints:

- version: `uint8`
- type: `uint8`
- flags: `uint8`
- reserved: `uint8`, must be zero
- stream_id: `uint64`
- payload_length: `uint32`
- maximum frame payload: 4 MiB by default

## Message types

```text
0x01 HELLO
0x02 HELLO_ACK
0x03 AUTH
0x04 AUTH_OK
0x05 AUTH_ERROR
0x06 REGISTER
0x07 REGISTER_OK
0x08 REGISTER_ERROR
0x09 PING
0x0A PONG
0x10 OPEN_STREAM
0x11 OPEN_STREAM_OK
0x12 OPEN_STREAM_ERROR
0x13 DATA
0x14 WINDOW_UPDATE
0x15 CLOSE_STREAM
0x16 RESET_STREAM
0x17 GOAWAY
```

Payload encoding is JSON for control messages in protocol v1 and raw bytes for `DATA`. This is deliberate for the starter implementation; a compact binary control codec may be introduced only with a versioned compatibility plan.

## Invariants

- malformed frames are rejected without process panic
- a stream cannot be used before `OPEN_STREAM`
- a stream must be closed or reset exactly once
- stream IDs are unique per connection
- no message may exceed configured frame limits
- all I/O is cancelable
