# auth

Private-file verification supports hashed, scoped, expiring development credentials.
Phase 9 also supplies a control-plane verifier: it reloads a private relay-scoped
key, sends only the tunnel credential hash during AUTH and receives a bounded
tunnel/generation/expiry lease. Registration enforces that exact generation.
Public requests and active sessions use local state without API calls.
Credential tokens and development policy files must be bounded private regular
files. The final path component must not be a symlink; the opened file must match
the checked identity and permissions. Use a real file in a directory controlled
by the service account. POSIX permission checks do not replace Windows ACLs.
See [Phase 2](../../docs/PHASE_2.md) and [Phase 9](../../docs/PHASE_9.md).
