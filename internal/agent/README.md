# agent

The Phase 2 client performs verified TLS and HELLO/AUTH negotiation, returns an
authenticated connection, and owns cancellation/closure. `Session.Wait` owns its
reader once; `Close` is idempotent. Phase 3 adds strict one-time registration with
ACK binding checks and private local generation reservations. Register and Wait
cannot share the reader concurrently; failed exchanges close the socket.
Phase 4's `ServeHTTP` owns the reader after an HTTP registration, dispatches
bounded streams to one fixed loopback port, and joins local forwarding workers
on cancellation or connection closure. Phase 5 requires flow-control negotiation
and consumes independent bounded queues with returned byte credit. See
[Phase 5](../../docs/PHASE_5.md).

Phase 6 handles negotiated heartbeat in registered diagnostic and HTTP sessions.
The CLI owns the reconnect loop; Backoff supplies capped exponential delays with
equal jitter and WaitReconnect observes cancellation. Security/protocol/state
errors are terminal. Every fresh registration reserves a higher generation;
active requests are closed and never replayed. See [Phase 6](../../docs/PHASE_6.md).

Phase 7 registers `graceful_shutdown`, receives strict GOAWAY and drains active
local work. `ServeHTTPGraceful`/`WaitGraceful` accept a shutdown signal separate
from the connection lifetime. The CLI signals drain, exits after worker cleanup
or its configured deadline, and reconnects on remote draining without replay.
`Close`, existing lifetime contexts and expiry still abort immediately.
See [Phase 7](../../docs/PHASE_7.md).

Phase 9's CLI bootstrap uses `internal/control` to reserve a minimum generation,
request a short-lived assignment and connect with a fresh in-memory credential.
It validates the relay's expiry/hostname/generation against the assignment. Lease
expiry retries bootstrap with normal backoff; authorization, TLS and malformed
assignment errors are terminal. Admitted public streams do not query the API.
See [Phase 9](../../docs/PHASE_9.md).
