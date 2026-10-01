# mux

One reader owns frames on each authenticated HTTP tunnel connection. DATA uses
fixed stream/connection byte credits, bounded reusable receive pages, and one
owned worker for credit updates and queued reset/rejection frames. Workers are
joined by `Run`; `Close` cancels all streams and releases buffers. Senders wait
for credit before taking the shared frame writer, and release that writer after
each bounded frame. FIN drains queued bytes; RESET discards and returns connection
credit. See [Phase 5](../../docs/PHASE_5.md) and
[PROTOCOL.md](../../docs/PROTOCOL.md) for the wire and memory bounds.

`Conn.mu` owns all credit counters, page queues, admission, and notifications.
`Stream.mu` owns lifecycle flags and terminal reasons. When both are needed,
lock connection before stream; release stream locks before parent operations.
`Stream.writeMu` serializes its DATA/FIN, but no connection lock is held during
network I/O or a credit wait. Control queue saturation and invalid peer credit
fail the connection rather than growing storage.

Phase 6 heartbeat uses this same reader and serialized writer. One owned worker
tracks a single cryptographic probe and a monotonic deadline; matching PONG
updates the read-only liveness snapshot. PONG replies use the bounded control
queue and require no DATA credit. Diagnostic mode accepts only negotiated
heartbeat messages, without enabling stream routing. All workers join on exit.

Phase 7 adds one owned drain worker and a separate shutdown signal. `Shutdown`
closes admission, writes SHUTDOWN after admitted OPEN writes, and exchanges
DRAINED after streams and acceptance workers finish. Peer DRAINED prevents
closing over queued response data. The first deadline cannot be extended.
`Close` and lifetime cancellation still abort immediately. Legacy accepting
endpoints wait for peer closure/deadline because they lack completion proof.
See [Phase 7](../../docs/PHASE_7.md).

Phase 8 adds one owned idle timer worker for negotiated streaming peers. Activity
is guarded by Conn.mu, refreshed on DATA writes/receipts/consumption and never
by heartbeat or credit updates. An expired stream is marked under the same lock
before RESET, preventing later activity from reviving it. Legacy peers keep their
whole-stream context deadlines. Upgraded sessions use the same stream and page
bounds; an upgrade marker without negotiated WebSocket support is terminal input.
See [Phase 8](../../docs/PHASE_8.md).
