# Phase 18 — Release and Self-Hosting

## Context

Phase 17 is committed as `8e94aa7`. The original implementation roadmap ends at
Phase 16; this phase implements its release/distribution requirements and a
concrete self-hosting baseline. API, database, relay protocol and request routing
remain unchanged. Public application bytes stay outside the control plane.

## Plan

1. Build deterministic, versioned CLI/relay/certificate binaries for the six
   documented OS/architecture targets, with checksums and revision metadata.
2. Provide reviewed local installers with an explicit version and checksum
   verification before atomic replacement; never pipe remote shell into execution.
3. Add a tag-based draft-release workflow with provenance attestations.
4. Supply non-root relay container and hardened Linux service/ingress examples,
   documenting private provisioning, DNS/TLS, health, draining, backup and rollback.
5. Verify cross-builds, checksum tampering/reproducibility, installer behavior,
   container runtime and service configuration. Record actual limits and checks.

## Changes

Implemented the release and self-hosting baseline. `scripts/release.mjs` builds
three static binaries on six targets with canonical checksums, full revision,
source cleanliness and pinned Go metadata. It refuses dirty source/existing output
unless development explicitly opts into dirty builds, joins bounded compiler
workers, and replaces the output only after all assets succeed.

Reviewed POSIX/PowerShell installers require a pinned version, bounded HTTPS or
verified offline assets, checksum verification and an account-controlled destination.
They atomically install only the verified CLI and never execute it, elevate or
change PATH. Optional checksum-file pins provide independent trust. Windows checks
validate ownership, writable ACLs and reparse points.

Tag CI checks native Windows installation/tamper rejection before building,
attesting and uploading a draft release. Regular CI cross-builds all targets and
exercises the image. The relay Dockerfile uses a digest-pinned CA-bearing runtime,
explicit executable permissions and non-root ownership. Linux service units,
control HTTPS ingress and the operator guide cover private provisioning,
DNS/certificates, readiness, draining, backup, upgrades and rollback.

The older control outage/revocation fixture now sets explicit two-second graceful
timeouts to fit its existing twelve-second deadline. Production defaults were not
modified. The first full Phase 18 run hit that deadline; an immediate isolated
rerun passed, then the final full run passed with explicit fixture bounds.

Phase 18 is committed in this change. No remote tag, release, push, service installation
or production deployment was performed. No API/schema/protocol change or new
package dependency was introduced.

## Verification

- Final `make check` passed: formatting, Go test/race/vet, Node units/lint/types,
  production build and every existing process/browser/security/load/chaos/CLI gate.
  Log: `/tmp/portway-phase18-check-final.log`.
- New release unit tests passed (2/2): explicit version/target validation,
  reproducible native assets, exact version/revision stamp, checksums, refusal of
  existing output, corrupt/duplicate/pinned checksums, preserved installed binary,
  symlinks and writable destinations. They run in `pnpm test` and `make check`.
- All 18 binaries cross-built successfully for linux/darwin/windows × amd64/arm64.
  Every binary and manifest hash matched `SHA256SUMS`. Local development artifacts
  are in `dist/releases/v0.0.0-phase18-final`, explicitly marked dirty and not a
  publication candidate. Log: `/tmp/portway-phase18-crossbuild-final.log`.
- Final `pnpm test:release-container` passed (1/1): default non-root version probe,
  packaged relay with read-only root/dropped capabilities/bounded PIDs and memory,
  private fixture files mapped to the actor's non-root UID, verified public HTTPS
  forwarding, owned graceful CLI stop and secret omission. Fixture containers,
  images and private workspaces were removed. Log:
  `/tmp/portway-phase18-container-final.log`.
- `sh -n install/install.sh` and workflow YAML parsing passed. All three systemd
  units passed `systemd-analyze verify` in an isolated root with placeholder
  executable paths (syntax/dependency validation, not service activation).
  The Nginx example passed `nginx -t` in a constrained container using temporary
  certificates. Logs: `/tmp/portway-phase18-{units,nginx}.log`.
- Phase 17's fresh isolated bootstrap passed earlier in this task before its
  commit. All 13 original private/config files still match their preservation
  snapshot and the original PostgreSQL volume remains. The complete-history
  `/workspace/portway-phase17.bundle` is verified through `8e94aa7`.

## Risks

Checksums detect corruption; a trusted HTTPS release origin or independently
verified provenance supplies authenticity. POSIX and PowerShell installers require
a reviewed local script, explicit release and controlled destination. Linux units
are examples for an operator-reviewed deployment, not automatic provisioning.
Production DNS, public certificates/renewal, backup storage and secrets belong to
the operator. A single relay retains its documented availability boundary.
Only Linux native execution was verified here; other platforms were cross-built.
Native Windows/PowerShell checks are configured in CI and await execution there.
GitHub OIDC attestations/draft upload are implemented as workflow steps and have
not been run from this workspace. API/dashboard deployment uses reviewed locked
source and pinned Node/pnpm rather than released container images. Deployment
examples need actual users, paths, secrets, ingress allowlists and DNS/TLS before
activation. Configuration parser checks do not certify a production installation.
