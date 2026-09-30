# Portway Protocol

## Status

Protocol v1 framing and capability negotiation are implemented in Phase 1.
Session handshakes and transport ownership are implemented in Phase 2.
Tunnel registration and generation ownership are implemented in Phase 3.

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
Message-specific codecs validate control payloads. `HELLO`/`HELLO_ACK` and
`AUTH`/`AUTH_OK`/`AUTH_ERROR` and registration are defined below; other control schemas are
introduced with their implementation phases.

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

## Authenticated connection (Phase 2)

The agent dials outbound TCP, verifies the relay certificate and hostname, and
negotiates TLS 1.3 with ALPN `portway/1`. Plain TCP and disabled certificate
verification are rejected. Certificate trust uses the configured CA bundle or
system roots. Each new TLS connection must authenticate; TLS resumption does not
skip authentication.

The only valid initial sequence is:

```text
TLS → HELLO → HELLO_ACK → AUTH → AUTH_OK → authenticated
                                   └──→ AUTH_ERROR → closed
```

All five handshake types have a 4096-byte payload cap. Phase 2 session offers
require a payload limit of at least 4096 bytes so every authentication message
fits the negotiated limit. Unexpected message types are rejected at the header
before reading their body. No capability is advertised until its behavior exists.

`AUTH` contains only the bearer credential:

```json
{ "token": "<opaque credential>" }
```

Tokens contain 32–512 printable ASCII bytes, with no whitespace. The relay checks
the SHA-256 hash of a high-entropy token, its `connect` scope, expiration, and
revocation through a credential-verifier interface. Phase 2 uses a bounded local
credential file mirroring existing `TunnelCredential` fields. Durable issuance
and control-plane revocation propagation are Phase 9 work; no DB access is added.

`AUTH_OK` identifies the authenticated connection and its credential deadline:

```json
{
  "connection_id": "con_0123456789abcdef0123456789abcdef",
  "expires_at": "2026-10-01T00:00:00Z"
}
```

Connection IDs use `con_` followed by 32 lowercase hexadecimal digits generated
from 16 cryptographically random bytes. An agent rejects an already-expired ACK.
`AUTH_ERROR` contains only a stable code:

```json
{ "code": "AUTH_INVALID" }
```

Allowed codes are `AUTH_INVALID`, `AUTH_EXPIRED`, and `AUTH_REVOKED`. Unknown
fields, duplicate keys, nulls, trailing JSON, and incorrect types are rejected.
Authentication failures and logs never echo a token or raw payload.

The deadline covers the whole TLS/HELLO/AUTH handshake (default 10s). Active
reads have a 120s idle timeout, writes a 5s timeout, and neither extends credential
expiration. Cancellation closes sockets immediately. The listener caps accepted
connections (default 128), including unauthenticated handshakes; excess sockets
are closed before starting a goroutine. Shutdown closes the listener and active
connections and joins the owned goroutines.

Authentication alone does not grant routing ownership. A diagnostic client may
close after AUTH_OK. Other clients must register within the registration timeout
(default 10s), bounded by credential expiry and cancellation.

## Tunnel registration (Phase 3)

```text
authenticated → REGISTER → REGISTER_OK → registered
                        └→ REGISTER_ERROR → closed
```

All three registration frames use stream ID 0 and have a 4096-byte payload cap.
Each connection may register exactly once. REGISTER before authentication, any
other frame while awaiting registration, and any frame after registration are
rejected at the header until later phases implement active-session messages.

```json
{ "tunnel_id": "tnl_local_dev", "generation": "1" }
```

Tunnel IDs are 1–128 ASCII letters, digits, underscores or hyphens, case sensitive.
The requested ID must exactly match the authenticated credential's tunnel ID.
Generation is a canonical decimal **string** representing uint64, from 1 through
18446744073709551615; leading zeroes, signs, numeric JSON values, and fractions
are invalid. Strings preserve all 64 bits in JavaScript.

REGISTER_OK echoes the binding and assigns a hostname:

```json
{
  "tunnel_id": "tnl_local_dev",
  "connection_id": "con_0123456789abcdef0123456789abcdef",
  "generation": "1",
  "public_hostname": "p-<first 32 lowercase hex digits of SHA-256(tunnel_id)>.portway.localhost"
}
```

The agent checks tunnel, connection, and generation against its request. Hostnames
are canonical lowercase DNS names, with no port, path, trailing dot or IP address.
The relay controls the base domain, never the peer; host assignment is stable
across relay-local generations. Hash collisions fail closed. Phase 3 assigns a
hostname without a public HTTP/HTTPS listener or forwarding; assignment does not
mean the public URL is ready.

REGISTER_ERROR contains only `code`, one of REGISTER_INVALID, REGISTER_FORBIDDEN,
REGISTER_STALE, REGISTER_CAPACITY, or REGISTER_CONFLICT. Invalid JSON, unknown
fields, case aliases, duplicate keys, nulls and trailing data are rejected. Errors
never echo raw payloads, credentials, or another tunnel's metadata.

Registration atomically replaces only a strictly smaller generation. Equal or
lower generations are rejected even after the prior session disconnects. The
previous socket is closed; its later cleanup cannot remove the new route. An
ACK write failure removes the new route but retains its generation watermark.
Host lookups return immutable metadata for active, unexpired owners only.

Watermarks are bounded by RELAY_MAX_TUNNELS (default 1024); they are retained until
relay process restart rather than evicted and made vulnerable to stale reuse.
Capacity rejects new IDs while allowing higher generations of known IDs.
Registration state is relay local; persistence, cross-relay coordination and
control-plane assigned domains remain later phases. The CLI reserves increasing
generations in a private local state directory before registration. Failed attempts
consume numbers; gaps are valid. Losing local state requires operator recovery to
a generation above the relay watermark; counters never silently reset on corruption.

## Invariants

- malformed frames are rejected without process panic
- a stream cannot be used before `OPEN_STREAM`
- a stream must be closed or reset exactly once
- stream IDs are unique per connection
- no message may exceed configured frame limits
- all I/O is cancelable
