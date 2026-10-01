# agent

The Phase 2 client performs verified TLS and HELLO/AUTH negotiation, returns an
authenticated connection, and owns cancellation/closure. `Session.Wait` owns its
reader once; `Close` is idempotent. Phase 3 adds strict one-time registration with
ACK binding checks and private local generation reservations. Register and Wait
cannot share the reader concurrently; failed exchanges close the socket.
Phase 4's `ServeHTTP` owns the reader after an HTTP registration, dispatches
bounded streams to one fixed loopback port, and joins local forwarding workers
on cancellation or connection closure. See [Phase 4](../../docs/PHASE_4.md).
