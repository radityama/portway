# control

Bounded authenticated control-plane calls for CLI relay assignment and relay AUTH
verification. HTTPS uses verified TLS 1.3; loopback development HTTP is allowed.
Requests never follow redirects or retry internally, and responses have explicit
size, header, nesting, duplicate-key, type and binding validation. Pools and
deadlines are bounded and cancellation is propagated. Errors never echo response
bodies, tokens or authorization headers. Application traffic does not use this
package. See [Phase 9](../../docs/PHASE_9.md).
