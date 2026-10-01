# routing

Phase 3 ownership lives in `internal/relay/registry.go`: canonical hostname lookup,
strict generation replacement, and owner-aware cleanup. Phase 4 public ingress
lives in `internal/relay/http.go`; bounded stream transport lives in `internal/mux`.
Host/SNI select an active registered owner; no request chooses an upstream address.
Phase 5 independently queues stream bytes within fixed stream/connection credits.
Read [ROUTING.md](../../docs/ROUTING.md) for forwarding policy and limits.
