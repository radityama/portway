# Load and chaos tests

These bounded profiles run real API, CLI and relay processes with disposable
PostgreSQL 17 and Redis 8 containers. They verify correctness and cleanup under
local load; their throughput and latency measurements are not a production SLA.

```bash
make load-test
make chaos-test
```

Both targets build first and run in `make check`. After `make build`, use
`pnpm test:load` or `pnpm test:chaos` for a focused process test. Go's normal and
race gates also exercise 512 multiplexed stream lifetimes across eight workers,
including 64 resets of stalled consumers, and the impairment bridge's disconnect
and cancellation paths.

## Profiles and assertions

| Profile           | Workload                                                                       | Assertions                                                                                                                  |
| ----------------- | ------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------- |
| Mixed load        | 256 requests, 12 workers; GET and known/chunked POST bodies up to 256 KiB      | SHA-256 identity, exactly one upstream receipt, no reconnect/generation change                                              |
| Large bodies      | 16 MiB known/chunked uploads; 64 MiB streamed response                         | Byte count and digest; bounded chunk generation and response storage                                                        |
| Saturation        | 32 held streams on one tunnel plus excess work                                 | Excess request returns 503 before upstream; every admission slot is reusable                                                |
| Network           | 0.5% random TCP loss with 5 ms delay; 100 ms delay spike; 100% loss for 300 ms | Intact bytes, observed latency, positive drop counter for the complete-loss burst, successful retransmission without replay |
| API/Redis outages | 64 requests across eight workers during each outage                            | Admitted forwarding continues; new Redis-dependent assignments fail without credential/generation allocation                |
| Agent crash       | SIGKILL during a received POST                                                 | Interrupted request fails, local work cancels, offline admission fails, manual restart reserves a higher generation         |
| Reconnect storm   | Eight agents, two relay SIGKILL cycles with a healthy standby                  | Recovery under ten seconds per cycle, increasing generations, stable hostnames, bounded equal jitter, no POST replay        |

Latency summaries include p50/p95/p99/max and achieved requests per second. Load
records relay heap/goroutine gauges before/after, peak upstream concurrency, stream
capacity and byte counters. Stream/request/socket gauges must return to zero;
final heap must stay below 128 MiB and goroutines within 16 of baseline. These
generous regression bounds do not certify absence of all long-term leaks.

Successful process profiles write metadata-only results to ignored
`.tmp/load/phase16-load.json` and `.tmp/load/phase16-chaos.json`. No application
payloads or credentials are stored. A failed run does not overwrite prior results.

## Isolated network impairment

Chaos requires a Linux Docker daemon with `NET_ADMIN` allowed inside a disposable
container. The fixture publishes its bridge only on loopback, uses a fixed relay
destination, caps accepted sockets at 32, and passes TLS through without decrypting
it. Copies use bounded buffers and a three-minute maximum connection lifetime;
cancellation and either peer's disappearance close both legs and join workers.

The preferred backend is `tc netem` on the fixture's `eth0`. If the kernel reports
that `netem` is unavailable, the fallback drops real TCP packets using an owned
iptables chain and delays ordered byte delivery in the bridge. That delivery
delay is not kernel packet latency. The result records `netem` or `packet-filter`
so the two measurements are distinguishable. Missing capabilities fail the test;
there is no host-network fallback or silent skip. Random loss may drop zero packets
in a short sample; the complete-loss burst requires observed kernel drops.

Only the dedicated namespace is modified. Processes, sockets, temporary data and
owned containers are cleaned up; development credentials and volumes are preserved.
Docker caches `portway-test-netem:<hash>` images for repeat runs. The build uses the
existing `postgres:17-alpine` base and adds `iproute2`/`iptables`. Set
`PORTWAY_NETEM_BASE_IMAGE` to an authorized mirror if needed. A configured
`PORTWAY_NETEM_CA_FILE` (default system CA bundle when present) is supplied as a
BuildKit secret for package download trust, never retained in an image layer.

## Recovery boundaries

The ten-second target requires a healthy alternative relay and API/Redis access
for a new admission. Restarting the same relay ID while its crashed incarnation's
15-second presence lease remains fresh is rejected and drains the replacement.
The storm test checks that fence, keeps serving on the standby while the lease
expires, then restores the original relay for the second failover. It does not
shorten the lease or bypass authentication to obtain a faster result.

Local relays use separate public ports; tests use the newly emitted URL after
failover and require its hostname to stay stable. Operators still supply ingress
and DNS routing to the assigned relay. Longer hardware-sized soaks, high-loss
disconnect storms and production capacity measurements remain operational work.
