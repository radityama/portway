# Releases and installation

Use the pinned toolchains in `.go-version`, `.node-version` and `package.json`.
Run `make check`, `make fuzz` and fresh bootstrap before preparing a tag. The
release builder accepts an explicit `vMAJOR.MINOR.PATCH` (optional prerelease),
requires clean committed source by default and refuses an existing output path:

```bash
node scripts/release.mjs --version v0.1.0
```

Outputs live in ignored `dist/releases/v0.1.0`: raw `portway`, `portway-relay` and
`portway-cert` executables for linux/darwin/windows × amd64/arm64; `manifest.json`
records version, full revision, source cleanliness, Go version and artifact
hashes/sizes; `SHA256SUMS` covers binaries and manifest. Windows assets end in
`.exe`. Builds disable CGO/VCS injection, trim source paths and stamp CLI version
and revision. No private files, `.env`, keys, runtime state or Node dependency tree
are included. Repeated builds of the same tree/toolchain must produce identical
assets. Development checks may opt into `--allow-dirty` and `--targets linux/amd64`;
their manifest identifies dirty source and they are not publication candidates.

`.github/workflows/release.yml` validates a release tag, runs checks, builds all
targets, attests the exact binaries/manifest/checksums using GitHub OIDC, and
uploads a **draft** release for tag-triggered runs. Manual workflow dispatch
accepts an explicit version and validates/builds without attesting or creating a
release. Both paths require native installer acceptance on Linux/macOS/Windows.
Review verification evidence and provenance before
publishing the draft. The workflow grants release/attestation permissions only
to that job. This implementation does not create a tag or release remotely.

Obtain and review the installer from the repository at the chosen tag, save it
locally, then run it. Never execute a downloaded script through a pipe:

```bash
sh install/install.sh --version v0.1.0 --dir "$HOME/.local/bin"
# Optional independently obtained SHA256SUMS hash:
sh install/install.sh --version v0.1.0 --dir "$HOME/.local/bin" --checksums-sha256 <64-hex-digest>
```

On Windows, review `install/install.ps1` and run it with `-Version v0.1.0` and an
explicit `-Directory`. The installers fetch only fixed-repository HTTPS assets
with bounded downloads, validate the selected artifact's SHA-256 and replace
the executable atomically after verification. They never run it, elevate, edit
PATH or write credentials. POSIX destinations must be account-owned directories without group/other writes
and existing executable symlinks are rejected. Windows destinations must have
appropriate user ACLs and must not be reparse points. Windows targets can be
cross-built on Linux, but native PowerShell execution requires a Windows runner.

The minimum authenticity boundary is HTTPS to `github.com/radityama/portway`.
A checksum downloaded beside a binary is not an independent signature. For a
stronger verification boundary, pin the checksum-file digest through a trusted
channel and/or run `gh attestation verify <artifact> --repo radityama/portway`
before installation. The installers accept a local `--assets-dir`/`-AssetsDirectory`
for independently verified/offline assets; all checksum checks still apply.

See [SELF_HOSTING.md](./SELF_HOSTING.md) for deployment and rollback. Release
assets currently distribute Go binaries; API/dashboard use the locked source
workspace and pinned Node/pnpm on the control host.

See [ACCEPTANCE.md](./ACCEPTANCE.md) for Phase 19 validation commands and the
required staging evidence before publication.
