# Phase 13 — Dashboard

## Context

Phase 12 is committed as `60a029d54aebab5085018649fcf6c7f389d806e9`, with complete
history in `/workspace/portway-phase12.bundle`. This phase builds the existing
Next.js dashboard against API.md section 20 and the established scoped API.
ARCHITECTURE defines the browser adapter; PRD and IMPLEMENTATION define the pages.

## Plan

1. Define the cookie/session, fixed destination, Origin, bounded API and honest
   metadata boundaries before implementation. Reuse durable ApiSession policy.
2. Implement login, overview, tunnel list/detail, domains, relays, logs and settings
   in that order, with a responsive accessible shell and clear failure/empty states.
3. Add role-aware project/tunnel/domain forms, one-time proof display, confirmation
   for revocation/disable, cursor pagination and fixed CLI instructions.
4. Test browser workflows against real PostgreSQL/DNS/API, tenant/viewer rules,
   secret handling, logout/revocation, CSRF and API failures; run quality gates.
5. Document local/production configuration and the Phase 13 handoff.

Invariants: outbound agent and fixed loopback upstream; no dashboard/DB/API
application-byte path; existing tenant/role enforcement; no secret logs or browser
storage; fixed validated API destination; bounded work/deadlines; no request replay.

## Changes

- Eight dashboard pages consume the existing scoped control API, with a responsive
  green/off-white shell, SVG icons, keyboard focus/skip navigation, empty states,
  safe error messages and bounded real metadata. No synthetic traffic is shown.
- `apps/dashboard/lib/transport.ts` and the browser-local route handlers fix the
  API destination, validate exact mutation Origin/content type, cap bodies/replies
  and concurrent calls, enforce complete deadlines, cancel abandoned work, reject
  redirects and forward only allowlisted user metadata operations.
- Login exchanges an explicitly supplied user key for a durable ApiSession sealed
  in an authenticated encrypted HttpOnly cookie. A separate private server key
  prevents edited cookies from substituting parent API keys. No bearer is returned
  to browser JavaScript, browser storage or URLs. Logout revokes that session and
  clears the local cookie during API outages with an explicit revocation status.
- Role-aware forms create projects/tunnels and revoke tunnels with typed
  confirmation. Domain forms display a one-time TXT value, verify actual DNS,
  activate policy, replace challenges and disable with confirmation. Mutation keys
  survive explicit retries of the same operation; requests never silently replay.
- Tunnel details show fixed loopback CLI instructions without allocating credentials
  or generations. Relays show actual read-only health/capacity. Settings shows
  organization, user and role. Logs reports the API's current 501 honestly.
- Signed cursors paginate tunnel/domain lists. Successful mutations invalidate
  dashboard views and reload the document, with shared pending state preventing
  overlapping changes. One-time proof forms retain their value until acknowledgement
  or close, then reload authoritative metadata. Pages use uncached API reads,
  explicit refresh and no link prefetch/polling. Authentication failures return to login; API outages keep
  durable cookies, and cursor failures retain first-page recovery.
- Setup creates and preserves the private dashboard key. `make dev` injects the
  actual configured API/origin ports. `.env.example` and README document separate
  production key provisioning, HTTPS origins, Node CA trust and quota sizing.
- Six transport unit tests and real Chromium/PostgreSQL/Redis/DNS/API/production
  Next.js browser workflows cover session tampering, CSRF, size/deadline/admission
  limits, tenant/viewer rules, mutations, pagination and outage/restart/logout.
  Bootstrap also verifies dashboard login/profile/logout using randomized ports.
- `make check` and CI include the browser gate; CI installs Chromium. Playwright
  1.63.0 is a pinned development-only dependency (maintained, Apache-2.0); its
  browser automation is not supplied by the standard library. Dependency audit
  checks report no known vulnerabilities.
- The runtime log ignore rule now applies to the root log directory so the
  dashboard's Logs page is included in source control and formatting checks.

No persistent schema, relay protocol or application traffic path changed.
Phase 13 is committed before starting Phase 14. No remote push was performed.

## Verification

Passed on 2026-10-01 with Go 1.26.8, Node 24.19.0 and pnpm 10.12.1:

```bash
make fmt
DASHBOARD_CHROMIUM_PATH=/usr/bin/chromium make check
pnpm test:bootstrap
pnpm db:validate
pnpm audit
git diff --check
git bundle verify /workspace/portway-phase12.bundle
```

`make check` passed formatting, Go unit/race/vet gates, ESLint, TypeScript types,
production builds, all 49 Node unit tests (8 tooling, 25 API, 10 protocol and 6
dashboard), and the real control/database/fleet/domain/dashboard integrations.
The final browser gate passed 9/9 reported tests; three preceding standalone runs
also passed 9/9 each after the final document-reload implementation. They cover
project/tunnel creation, generation-safe detail/CLI instructions, DNS ownership,
activation/disable/renewed proof display, viewer and tenant rules, CSRF, oversized
bodies, blocked internal/credential routes, revocation, signed cursor pagination,
API restart, outage isolation and both remote/local logout. No browser page errors
or API/relay bearer values appeared in captured process output.

The development bootstrap passed real startup/login/profile/logout with randomized
ports, duplicate-start rejection and interrupt cleanup. PostgreSQL data volumes
and existing development credentials were preserved. Unit transport tests exercise
private key permissions, encrypted cookie tampering/origin/expiry, malformed input,
actual size limits, redirects, slow peers, 32-call admission and cancellation.

OpenAPI YAML parsed with all 177 local references resolved. Prisma validation
passed without schema changes. Full dependency audit found no known vulnerabilities.
Desktop (1440px) and mobile (390px) screenshots were visually inspected, with mobile
overflow checked in Chromium; ignored artifacts are `.tmp/screenshots/phase13-overview.png`
and `.tmp/screenshots/phase13-mobile.png`.

Phase 12's bundle was verified as complete history through `60a029d`. Phase 13
is committed before starting Phase 14; no remote push or deployment was performed.

## Risks and next phase

- Request recording, traffic metrics and audit browsing require supported API
  contracts in Phase 14. Account edits, key administration and relay operator
  commands remain outside this user-key dashboard.
- Counts describe up to 100 resources per kind; creation/filter selectors show the
  first 100 accessible projects/tunnels. Stored connection states describe policy,
  not real-time tunnel presence. Refresh is explicit; cursors may require First
  page after API restart.
- ACTIVE domain policy does not prove certificate readiness. Operators still
  provision ingress/DNS, public TLS trust and certificates as documented in Phase
  12; the dashboard does not automate external ACME accounts.
- Production requires an exact HTTPS origin, a private regular session-key file
  shared across dashboard instances, and trusted API TLS. Key replacement requires
  a server restart and invalidates browser cookies. The API's per-IP quota sees
  the dashboard server; size it together with the 32-call per-process admission.
- API outages can prevent remote logout revocation; the browser cookie is removed,
  the result explains the limitation, and the API session keeps its normal bounded
  expiry. API sessions do not automatically renew from parent user keys.
- CI wiring is checked locally through the equivalent gates; no GitHub workflow
  run or remote push was performed in this task.
