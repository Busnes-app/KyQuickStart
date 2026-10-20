# KyQuickStart

A command-line installer for the supported catalog of Ky products and selected third-party apps across
your own servers: choices, prerequisite checks, installation, preconfiguration and a printed
handover of administrator and everyday logins.

**Status:** pre-release. The production catalog holds Nginx Proxy Manager as the edge on a
Kubernetes target ([docs/edge.md](docs/edge.md)), KyIdentity behind it
([docs/guided-setup.md](docs/guided-setup.md#catalog-kyidentity-on-kubernetes)), and KyRecovery,
kyPulse, KyNotes and KyCalendar signing in through it ([docs/guided-setup.md](docs/guided-setup.md#catalog-kyrecovery-on-kubernetes),
[kyPulse](docs/guided-setup.md#catalog-kypulse-on-kubernetes), [KyNotes](docs/guided-setup.md#catalog-kynotes-on-kubernetes),
[KyCalendar](docs/guided-setup.md#catalog-kycalendar-on-kubernetes)), with KyDrive, KyPost, KyDNS and the
KyMessages chat stack on a Docker host routed through that edge
([KyDrive](docs/guided-setup.md#catalog-kydrive-on-docker), [KyPost](docs/guided-setup.md#catalog-kypost-on-docker),
[KyDNS](docs/guided-setup.md#catalog-kydns-on-docker), [KyMessages](docs/guided-setup.md#catalog-kymessages-on-docker));
otherwise only a disposable demo app installs. KyYard, with the suite runner in its pod, installs
from a [release binary](#install-a-release), which carries the digest of the runner image it was
released with; source builds refuse it ([KyYard](docs/guided-setup.md#catalog-kyyard-on-kubernetes)).
Docker apps back up to an off-host restic
rest-server with `kyquickstart backup` and come back with `kyquickstart restore`
([guided setup](docs/guided-setup.md#backups-and-restore-on-docker)); restoring an app whose
people come from KyIdentity needs KyIdentity v0.4.4 or later (the catalog's is v0.4.5). The offboarding bridge's image
(`ghcr.io/busnes-app/kyoffboard`) has a release workflow but no published version yet.
`integrate-product` connects already-running Ky products to KyIdentity. See
[the deployment plan](docs/superpowers/plans/2026-10-10-deployment-plan.md) for what remains.

## Targets

- Docker hosts over SSH (pinned host keys, SSH agent authentication).
- Kubernetes clusters through an existing kubeconfig. App volumes use the cluster's default
  StorageClass, or the one a target names with `storage_class`.

The installer binary runs on your workstation and keeps its plan/state there. Target-side
secrets are referenced rather than copied into ordinary plan/log output. Protect the
operator account, target credentials and handover output; inspect the command-specific
secret/handover contract before exporting results.

## Independent installation and first run

You do not need an existing Ky suite to run the installer. You do need prepared target
infrastructure: Docker Engine/Compose and SSH agent access with a verified host key for
Docker targets, or an existing Kubernetes cluster, working kubeconfig and persistent
StorageClass for Kubernetes. KyQuickStart does **not** install Docker or Kubernetes.
Supply DNS/HTTPS and edge configuration as described in the guided setup.

No GitHub release is published as of 2026-10-20. The release-download recipe below becomes
usable once a release exists; placeholders such as `vX.Y.Z` are not real download names.
Until then, clone this repository and use `make build` with the Go version in `go.mod`.
A source binary cannot install the KyYard runner because it has no released image pin.

Run `./kyquickstart plan --demo --state ./demo` to explore without installing an app.
Saving a plan does not deploy it. Follow [guided setup](docs/guided-setup.md) to select a
real target/catalog, run preflight, review the plan, apply, and verify the handover.
A demo pass is not a production catalog or disaster-recovery acceptance result.

`make ci` is the core source check. `make e2e` additionally needs disposable Docker/SSH/kind
infrastructure; it does not replace per-product live mail, phone MFA, custodian-card or
offboarding acceptance. Test each configured integration before calling the suite ready.

## Install a release

Each release `vX.Y.Z` publishes `kyquickstart-X.Y.Z-linux-amd64`, `kyquickstart-X.Y.Z-linux-arm64`,
`kyquickstart-X.Y.Z-darwin-amd64` and `kyquickstart-X.Y.Z-darwin-arm64`, each attested and
pinned to the attested `kyquickstart-runner:X.Y.Z` image. Download one, verify it before running
it, then verify the runner image it pins (POSIX shell):

```sh
gh release download vX.Y.Z --repo Busnes-app/KyQuickStart --pattern 'kyquickstart-X.Y.Z-<os>-<arch>'
gh attestation verify kyquickstart-X.Y.Z-<os>-<arch> --repo Busnes-app/KyQuickStart \
  --cert-identity https://github.com/Busnes-app/KyQuickStart/.github/workflows/release-image.yml@refs/tags/vX.Y.Z
chmod +x kyquickstart-X.Y.Z-<os>-<arch>
./kyquickstart-X.Y.Z-<os>-<arch> version                  # X.Y.Z
./kyquickstart-X.Y.Z-<os>-<arch> version --runner-image   # ghcr.io/busnes-app/kyquickstart-runner:X.Y.Z@sha256:…
pin="$(./kyquickstart-X.Y.Z-<os>-<arch> version --runner-image)"
gh attestation verify "oci://ghcr.io/busnes-app/kyquickstart-runner@${pin#*@}" --repo Busnes-app/KyQuickStart \
  --cert-identity https://github.com/Busnes-app/KyQuickStart/.github/workflows/release-image.yml@refs/tags/vX.Y.Z
```

The macOS binaries are not signed or notarized. `gh release download` leaves no quarantine flag;
a browser download gets one and Gatekeeper blocks the binary. After the attestation verifies, clear
it with `xattr -d com.apple.quarantine kyquickstart-X.Y.Z-darwin-<arch>`.

## Build

Requires Go 1.26.6 or later. A source build pins no runner image, so it cannot install KyYard.

```sh
make build
make ci             # tidy check, vet, race tests
make e2e            # needs Docker; real SSH, kind and edge containers
make workflow-lint  # needs Docker; actionlint and zizmor on the release workflow
```

## Try it

```sh
./kyquickstart plan --demo --state ./demo
```

`plan` is interactive and needs a terminal; running `./kyquickstart` with no arguments
starts the same guided setup.
Choosing `finish` saves the plan without touching any server.

| Command | Purpose |
| --- | --- |
| `plan` | Guided questions; writes `stack.yaml` |
| `preflight` | Read-only target checks |
| `apply` | Install and verify the planned apps |
| `install`, `uninstall`, `unmanage`, `upgrade`, `status` | Manage one app at a time after the first install ([guided setup](docs/guided-setup.md#managing-apps)) |
| `handoff`, `pull-plan` | Hand app management to KyYard's suite runner, or copy its plan back ([guided setup](docs/guided-setup.md#hand-off-to-kyyard)) |
| `backup`, `restore` | Snapshot a Docker app to its target's rest-server, or restore one ([guided setup](docs/guided-setup.md#backups-and-restore-on-docker)) |
| `verify-edge` | Check public routing and forwarded headers |
| `integrate-identity`, `integrate-product`, `integrate-stack` | Configure existing Ky products against KyIdentity |
| `review` | Show the saved plan |
| `version` | Print the release set; `--runner-image` prints the runner image a release binary pins |

## Documentation

- [Guided setup](docs/guided-setup.md)
- [Edge and reverse proxy](docs/edge.md)
- [Offboarding bridge](docs/offboarding.md)
- [Suite runner (KyYard manages the suite)](docs/superpowers/specs/2026-10-16-suite-runner-design.md)
- [Third-party catalog and admission rules](docs/superpowers/specs/2026-10-06-third-party-catalog-design.md)
- [Installer architecture](docs/superpowers/specs/2026-10-06-installer-architecture-design.md)

## License

MIT. See [LICENSE.txt](LICENSE.txt).

All catalog Ky products support Docker targets; see [Docker product setup](docs/guided-setup.md#catalog-ky-products-on-docker) for routes, protected target credentials and KyYard's private resident runner.
