# Portway CLI contract — Phase 17

This contract extends the existing numeric-port and diagnostic commands. Existing
connection JSON events and exit codes remain: 0 success/clean shutdown, 1 operation
failure, 2 invalid usage. `--json` and `PORTWAY_JSON=1` produce one JSON object per
line without ANSI or human output on stdout. No command prints a session/key.

## Commands

| Command                                                       | Behavior                                                                                                                                                           |
| ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `portway [port]`, `portway start [port]`                      | Run a foreground tunnel; detect the local service when the port is omitted                                                                                         |
| `portway stop [tunnel-id]`                                    | Request graceful stop of an authenticated owned local agent; never signal a saved PID                                                                              |
| `portway status [tunnel-id]`                                  | Read scoped API tunnel metadata and/or owned local process state; durable CONNECTING metadata is not proof of a live agent                                         |
| `portway list`                                                | One cursor page of scoped tunnels; `--project`, `--limit`, `--cursor` select the page                                                                              |
| `portway login --token-file <private-file>`                   | Exchange a provisioned key for a private, expiring session; `--token-stdin` supports piped input                                                                   |
| `portway logout`                                              | Clear the saved session and revoke that session at its bound API; report an unconfirmed remote revocation during outages                                           |
| `portway config [show]`                                       | Show non-secret saved settings and authentication presence                                                                                                         |
| `portway config set <key> <value>`                            | Validate and atomically save an allowlisted setting                                                                                                                |
| `portway create --project <id> --name <name> [--port <port>]` | Create a persistent tunnel, optionally select it with `--use`                                                                                                      |
| `portway delete <id>`                                         | Revoke the scoped tunnel and future credentials; retain server history                                                                                             |
| `portway domain add <hostname> [--tunnel <id>]`               | Create a scoped domain and return its one-time public DNS TXT proof                                                                                                |
| `portway domain verify/activate/challenge/remove <id>`        | Operate existing domain policy; ACTIVE does not certify TLS deployment                                                                                             |
| `portway logs [tunnel-id] [--limit 1..4]`                     | Read recent scoped request metadata; no body/header/path logging or streaming history                                                                              |
| `portway doctor [port]`                                       | Check local configuration, credential availability, API identity/readiness and verified relay reachability without allocating a credential or registering a tunnel |
| `portway version`                                             | Return build version, revision, Go version and OS/architecture                                                                                                     |
| `portway connect/register [--once]`                           | Preserve existing authentication/registration diagnostics                                                                                                          |

Global options: `--json`, `--config-dir`, `--api-url`, `--api-ca-file`,
`--relay-ca-file`, `--project`, `--tunnel`. Options override environment, which
overrides saved settings. Existing direct private-file mode stays available.
Configuration/authentication uses `PORTWAY_CONFIG_DIR` or the OS user config
directory's `portway` subdirectory. Private directories/files use 0700/0600 on
POSIX, reject symlinks at controlled state boundaries, cap sizes, reject malformed
JSON and use exclusive mutation locks and atomic replacement. Windows deployments
require appropriate account ACLs. No server database schema is extended.

Saved settings contain API URL/trust, relay trust, default project/tunnel/local
port and local generation-state directory. Stored sessions are bound to the exact
API base and trust selection; changing those settings requires logout first. A
saved bearer must never be forwarded to an override API. Explicit external API
key files continue to work, but logout only revokes the CLI's managed session.

## Local service discovery and bootstrap

An explicit numeric port wins, then `PORTWAY_LOCAL_PORT`/saved port. Otherwise
inspect bounded local `package.json`, recognized framework dependencies/dev
scripts and npm/pnpm/yarn/bun lockfiles without executing commands. An explicit
framework dev-script port is preferred; recognized frameworks supply candidates.
A configured `PORTWAY_DEV_LOG_FILE` can supply a bounded recent localhost URL from
development output. Last, probe a small documented loopback-only candidate set.
Each probe has a deadline and bounded worker count. Refuse zero/multiple reachable
candidates; print a clear instruction to specify the port. Never infer an arbitrary
remote target or launch an untrusted development script.

When a managed-session invocation has no selected tunnel, create an
ephemeral tunnel in the selected project (or the sole visible project). Multiple
projects require explicit selection. A supplied project must stay within API
authorization scope. Keep the new identity for every reconnect and revoke that
owned ephemeral record on orderly exit when the API is reachable. No failed
request or management mutation is automatically replayed. Persistent tunnel IDs
remain selected explicitly and use the existing generation/lease flow.
External API-key file mode preserves its existing `tnl_local_dev` default;
choose a different existing tunnel explicitly with `--tunnel`.

## Local process control and diagnostics

Foreground agents expose a small management listener only on numeric loopback,
with a random private instance secret and bounded HTTP sockets/headers/deadlines.
Private per-tunnel runtime metadata identifies that listener. `status`/`stop`
verify its secret and instance/tunnel binding; stale metadata cannot kill a reused
PID or unrelated listener. The listener carries no application data or provisioned
credentials. Stop cancels the same graceful lifecycle as Ctrl+C, and joins owned
workers. Runtime cleanup compares ownership before removing state. A crashed
agent's refused endpoint can be reclaimed under an exclusive state lock.

Doctor performs bounded reads, identity/readiness and verified TLS checks; it does
not issue/revoke credentials, register a tunnel, rotate private files or repair
state automatically. Findings use stable check names and actionable messages.
Check the CLI-relevant configuration even when Docker is not installed: Docker
is a platform development prerequisite, not a requirement for an installed agent.
