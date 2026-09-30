# Portway Protocol

## Status

Protocol v1 framing and capability negotiation are implemented in Phase 1.
Session handshakes and transport ownership are implemented in Phase 2.

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

The header is exactly 16 bytes. Version must be `1`; zero is not a valid wire
version. Go's encoder accepts an unset version (`0`) as a convenience and writes
version `1`. All flags are reserved in v1 and must be zero. Unknown types,
nonzero reserved bytes, and invalid stream IDs are rejected before allocating
payload memory. Types `0x0B`–`0x0F` are intentionally unassigned.

Connection messages (`HELLO` through `PONG`, and `GOAWAY`) use stream ID `0`.
Stream messages (`OPEN_STREAM` through `RESET_STREAM`) require a nonzero ID.
The full unsigned 64-bit range is supported; JavaScript consumers must use
`bigint`, not `number`, for stream IDs.

The codec supports a smaller per-connection payload limit in the range
`1..4194304`. A decoded header exceeding that limit is rejected without reading
the body. The negotiated limit applies in both directions after `HELLO_ACK`.
The codec handles fragmented reads and partial writes. Any I/O or protocol error
is terminal for that connection; callers must not attempt to resynchronize or
retry a partially written frame.

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

The frame codec validates the envelope and treats payload bytes as opaque.
Message-specific codecs validate control payloads. Phase 1 defines the
`HELLO`/`HELLO_ACK` codecs below; other control schemas and session state
transitions are introduced with their implementation phases.

## Version and capabilities

`HELLO` is a connection frame with this JSON payload:

```json
{
  "version": 1,
  "capabilities": ["multiplexing", "flow_control"],
  "required_capabilities": ["multiplexing"],
  "max_payload_size": 4194304
}
```

The relay negotiates against its own offer with the same shape. `HELLO_ACK`
contains the selected version, shared capabilities, and smaller payload limit:

```json
{
  "version": 1,
  "capabilities": ["flow_control", "multiplexing"],
  "max_payload_size": 4194304
}
```

- Both offers must select v1. There is no automatic version downgrade.
- Shared capabilities are returned in ascending ASCII order.
- A required capability must also be advertised by that peer. All requirements
  from both peers must appear in the intersection; otherwise negotiation fails.
- Unknown optional capabilities are ignored unless both peers advertise them.
- Capability names match `[a-z][a-z0-9_]{0,63}`. Lists contain at most 32 unique
  entries. Duplicate entries are invalid.
- Initial defined names are `multiplexing`, `flow_control`, `heartbeat`, and
  `graceful_shutdown`. Advertising a capability promises its behavior; defining
  these names in Phase 1 does not enable their later-phase implementations.
- `version`, `capabilities`, and `max_payload_size` are required. An empty
  capabilities array is valid. `required_capabilities` may be absent in `HELLO`.
- Handshake payloads are capped at 4096 bytes. Null arrays, unknown fields,
  duplicate keys, incorrect JSON types, trailing JSON, and invalid values are
  rejected. Errors never include the raw payload.
- The agent validates `HELLO_ACK` against its offer: selected capabilities must
  be advertised, requirements must be met, and the limit cannot exceed its offer.

## I/O ownership

The synchronous codec accepts `io.Reader`/`io.Writer`; it does not own sockets,
background goroutines, or timers. The transport/session caller sets read/write
deadlines and cancels blocked I/O by closing its connection. Phase 1 tests this
boundary with `net.Pipe`, including deadline, cancellation, and peer closure.
Phase 2 connects this ownership to the agent and relay lifecycle.

Shared byte-level fixtures live in `tests/fixtures/protocol-v1.json`. Go tests
verify the codec against them; TypeScript tests verify shared constants and
64-bit stream IDs against the same fixtures.

## Invariants

- malformed frames are rejected without process panic
- a stream cannot be used before `OPEN_STREAM`
- a stream must be closed or reset exactly once
- stream IDs are unique per connection
- no message may exceed configured frame limits
- all I/O is cancelable
