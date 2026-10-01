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
