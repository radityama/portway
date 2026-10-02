# Phase 19 — Deployment Acceptance and Release Validation

## Context

Phase 18 is committed as `14a5660`. Its GitHub CI run
[36967272569](https://github.com/radityama/portway/actions/runs/36967272569)
passed the Node and release-build jobs but failed the Go job during private
credential fixture setup. The workspace uses umask `0077`; normal runners use
`0022`. Go's test temporary directory was passed directly to a helper that
correctly rejects group/other-readable credential directories.

This phase covers portable verification, native installation and isolated
deployment acceptance. No staging server/domain has been supplied. Outbound TLS,
bounded forwarding, generation fencing, control/data separation, private files,
and mutation non-replay remain the existing contracts. No production networking,
API, schema or protocol behavior changes.

## Plan

1. Make credential/state test fixtures explicitly private and verify both umasks.
2. Check native offline installation, replacement and tamper rejection on Linux,
   macOS and Windows in regular CI and before release builds.
3. Verify admitted forwarding through API restart, idempotent migrations, actual
   PostgreSQL custom-format backup/restore and bounded sustained traffic.
4. Provide a manual release workflow validation path that does not publish.
5. Run local quality gates and verify GitHub jobs for the final pushed revision.
6. Record operator acceptance steps and outstanding real-infrastructure evidence.

## Changes

Test fixtures create private child directories explicitly; production permission
validation is retained. A regression test verifies refusal of an unsafe existing
directory without changing its permissions, credential or contents, even with
explicit rotation requested. CI tests/race/fuzz run under both `0022` and `0077`.

Reusable native installer CI builds the native target on Ubuntu, macOS and Windows,
checks artifact hashes and installed version/revision, exercises valid atomic
replacement, and verifies corrupt/duplicate/pinned checksum rejection preserves
the installed executable. Fixtures and compiled assets are temporary.

Deployment acceptance starts its own PostgreSQL/Redis/API/relay/agent processes.
It verifies migrations/restart preserve policy and admitted traffic, dumps the
fixture database, restores into a separate fixture database, reruns migrations,
and authenticates against a restored API to confirm point-in-time data. It never
restores over the source database. A 10-second default sustained profile checks
byte integrity, zero retained streams and bounded heap/workers. Operators may
select 1–300 seconds with `PORTWAY_SOAK_SECONDS`. Results contain metadata only
under ignored `.tmp/acceptance/phase19-deployment.json`.

The Release workflow accepts manual versioned validation. Manual runs execute
native checks, quality gates and builds and upload workflow artifacts; attestation
and draft-release creation remain tag-only. No remote tag/release is created by
these changes. See [ACCEPTANCE.md](./ACCEPTANCE.md).

## Verification

- The original permission failure reproduced under `0022` and passed under `0077`.
- After fixture fixes, `go test ./...` passed under `0022`.
- Linux native installer acceptance passed, including hash/version/revision,
  replacement and rejection preservation. Log: `/tmp/portway-phase19-native.log`.
- Isolated deployment acceptance passed 4/4 checks, including actual backup restore,
  restored API authentication and a 10-second sustained profile. Log:
  `/tmp/portway-phase19-deployment.log`.
- Final `make check` passed under `0022`: formatting, Go units/race/vet,
  Node units/lint/types/build and every process/browser/security/load/chaos/CLI
  suite, including deployment acceptance. The initial run lacked the Playwright
  browser download; the final run used the installed Chromium through the
  documented `DASHBOARD_CHROMIUM_PATH`. Log:
  `/tmp/portway-phase19-check-final.log`.
- Focused affected Go suites passed uncached with the race detector under `0077`.
  All six fuzz targets passed under `0022` with `FUZZTIME=5s`. Logs:
  `/tmp/portway-phase19-{private-race,fuzz}.log`.
- The longer isolated acceptance profile passed 4/4: 4,260 verified requests
  over 120 seconds, zero final active streams, 17 final relay goroutines and
  approximately 2.8 MB relay heap. These are local measurements. Log:
  `/tmp/portway-phase19-soak.log`.
- The packaged non-root/read-only relay container acceptance passed 1/1. Fresh
  bootstrap passed 1/1 from copied source with new private fixtures and a separate
  owned Compose project; its workspace, containers and volume were removed. Logs:
  `/tmp/portway-phase19-{container,bootstrap}.log`.
- Workflow YAML parsing and `actionlint` v1.7.7 passed. No remote workflow dispatch,
  release or tag was performed. Hosted CI remains pending the final push.

## Risks and outstanding evidence

GitHub reads work, but the current connection rejects pushes with `403`. The final
commit must reach GitHub before hosted CI can be observed. A local pass is not a
hosted CI pass. Native macOS/Windows verification requires their hosted runners.

The acceptance fixtures use loopback services and temporary development trust;
they do not verify public DNS, an external ACME issuer, production service activation,
assignment-aware ingress, an external backup destination or real server capacity.
The bounded sustained profile catches gross resource retention; it is not an SLA
or a multi-day soak. Version compatibility and production upgrade/rollback require
operator evidence on the actual deployment. OIDC attestation/publication requires
an authorized tagged release. Application visitor authentication, ACME automation,
audit browsing and other documented post-MVP features retain their existing scope.
