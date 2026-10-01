# config

Validated agent/relay environment configuration: addresses, TLS/credential paths,
bounded durations, frame/connection/registry limits, base domain, tunnel ID, local
state directory and recovery generation. Phase 4 adds public TLS/listener settings,
public connection and stream limits, and stream deadlines. Errors do not echo
environment values. See [Phase 4](../../docs/PHASE_4.md).

Phase 9 adds opt-in API URLs, separate API trust, private user/relay API-key paths,
relay ID and API request timeouts up to 30 seconds (default 5s). Relay API URL/key/ID
must be configured together. Clients validate HTTPS or loopback development HTTP
and refuse redirects. See [Phase 9](../../docs/PHASE_9.md).
