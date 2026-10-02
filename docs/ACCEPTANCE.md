# Deployment and release acceptance

Use the committed revision, pinned tools and reviewed source/configuration. Phase
19 provides executable isolated checks; the following operator checks establish
readiness of a particular deployment. Record the revision, timestamps, target
platform, test results and rollback decision. Keep tokens, keys, request payloads
and database backups out of reports and Git history.

## Automated checks

```bash
make check
make fuzz FUZZTIME=5s
make native-installer
make release-container
# Longer isolated profile; bounded to 1–300 seconds:
PORTWAY_SOAK_SECONDS=120 make deployment-integration
```

Regular GitHub CI must pass for the same commit: both Go permission settings,
Node/process/browser suites, all-target release build/container runtime, and native
installer jobs on Linux/macOS/Windows. The native jobs validate installation and
CLI version execution, not every platform's networking or service manager.

Deployment acceptance creates only owned disposable containers/databases/private
directories. It restores a PostgreSQL custom-format archive into a separate fixture
database and verifies the restored API. It reruns migrations and restarts the
current API revision; that does not establish compatibility of arbitrary future
versions. Sustained forwarding results are metadata in
`.tmp/acceptance/phase19-deployment.json`. CI uses a short ten-second profile.

Run the **Release** workflow manually on a reviewed commit with an explicit
version such as `v0.0.0-validation`. This exercises release quality checks, native
installers and cross-builds and uploads workflow artifacts. Manual runs do not
attest or create draft releases. Tag-triggered runs additionally attest assets
and create a draft release; publishing remains a separate reviewed action.

## Operator staging acceptance

Provision a disposable control host and relay host using
[SELF_HOSTING.md](./SELF_HOSTING.md), real staging hostnames, publicly trusted
certificates, private production-style credentials and protected data services.
Use a disposable upstream application and test account. Protect the application's
public endpoints with its own authentication when required.

Record successful evidence for:

| Check                   | Required observation                                                                                                                                            |
| ----------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| DNS and TLS             | Real external clients resolve the assigned host and validate the certificate chain/SAN/expiry without bypassing TLS verification.                               |
| Control services        | HTTPS API readiness, dashboard login/logout and read-only `portway doctor` succeed; operator routes remain restricted.                                          |
| Application traffic     | Known HTTP upload/download contents, SSE flushes and WebSocket duplex frames survive the external route.                                                        |
| Recovery                | Interrupt the disposable agent/relay/network and observe higher-generation recovery, failed interrupted requests without replay, and bounded shutdown.          |
| Fleet routing           | If multiple relays are used, the actual ingress/DNS follows assignment and a drained relay receives no new work.                                                |
| Certificates            | Perform the issuer's renewal/deployment process, confirm trusted reload and verify a failed replacement preserves the last valid certificate.                   |
| Backup restoration      | Back up to the selected independent storage, restore into a separate database/host, and verify scoped policy/authentication. Never test restore over live data. |
| Upgrade and rollback    | Test the intended pair of revisions with compatible migrations, drain before replacement, verify readiness/public URLs, and exercise the documented rollback.   |
| Capacity and monitoring | Exercise the expected workload on target hardware, record errors/latency/resource trends, and verify scrape/alert delivery to the operator.                     |

Preserve logs without credentials or application payloads. A single-relay restart
has the documented interruption/incarnation-fence boundary; availability depends
on actual topology. An isolated fixture pass alone does not certify this table.

## Release decision

Require green CI for the exact commit and the relevant platform/deployment evidence.
Build from clean source, verify checksums/provenance and review the draft release.
Document remaining limitations before inviting users. Public DNS/TLS, ACME issuer
automation, server access and backup storage are operator inputs; they are not
provisioned by running the repository checks.
