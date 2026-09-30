# agent

The Phase 2 client performs verified TLS and HELLO/AUTH negotiation, returns an
authenticated connection, and owns cancellation/closure. `Session.Wait` owns its
reader once; `Close` is idempotent. Registration and public forwarding remain
later phases. See [Phase 2](../../docs/PHASE_2.md).
