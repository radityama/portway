# agent

The Phase 2 client performs verified TLS and HELLO/AUTH negotiation, returns an
authenticated connection, and owns cancellation/closure. `Session.Wait` owns its
reader once; `Close` is idempotent. Phase 3 adds strict one-time registration with
ACK binding checks and private local generation reservations. Register and Wait
cannot share the reader concurrently; failed exchanges close the socket. Public
forwarding remains Phase 4. See [Phase 3](../../docs/PHASE_3.md).
