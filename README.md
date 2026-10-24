# KyQuickStart

The deterministic execution layer for [KyQuickStart-AI](https://github.com/Busnes-app/KyQuickStart-AI),
the primary installation path, and a command-line tool that also runs unattended from a saved
`stack.yaml`. It installs the Ky suite on your own Docker hosts and Kubernetes clusters with
validated operations, owned credentials and verifiable results, and prints a handover of
administrator and everyday logins. New catalog apps and standalone features are frozen
([redirect record](docs/redirect.md)).

**Status:** pre-release. The catalog holds the ten Ky products, Euro-Office (KyDrive's optional editor, Kubernetes only) and Nginx Proxy Manager, the edge on
a Kubernetes target ([docs/edge.md](docs/edge.md)). Each product's placement and setup are in
[guided setup](docs/guided-setup.md); KyDrive, KyPost, KyDNS and KyMessages run on Docker only
([placement](docs/redirect.md#placement)). KyYard, with the suite runner in its pod, installs
from a [release binary](#install-a-release), which carries the digest of the runner image it was
released with; source builds refuse it ([KyYard](docs/guided-setup.md#catalog-kyyard-on-kubernetes)).
Docker apps back up to an off-host restic
rest-server with `kyquickstart backup` and come back with `kyquickstart restore`
([guided setup](docs/guided-setup.md#backups-and-restore-on-docker)); restoring an app whose
people come from KyIdentity needs KyIdentity v0.4.4 or later (the catalog's is v0.4.7).
Drive snapshot restores require native `restore-reset`, which the catalog's KyDrive v0.4.3
advertises; an image without it is refused before replacing data.
`integrate-product` connects already-running Ky products to KyIdentity. Open requirements are
listed under [Unfinished](docs/redirect.md#dispositions).

## Targets

- Docker hosts over SSH (pinned host keys, SSH agent authentication).
- Kubernetes clusters through an existing kubeconfig. App volumes use the cluster's default
  StorageClass, or the one a target names with `storage_class`.

Both are fully supported; see [Docker product setup](docs/guided-setup.md#catalog-ky-products-on-docker)
for routes, protected target credentials and KyYard's private resident runner.

The binary runs on your workstation and keeps its plan/state there. Target-side
secrets are referenced rather than copied into ordinary plan/log output. Protect the
operator account, target credentials and handover output; inspect the command-specific
secret/handover contract before exporting results.

## Independent installation and first run

You do not need an existing Ky suite to run KyQuickStart. You do need prepared target
infrastructure: Docker Engine/Compose and SSH agent access with a verified host key for
Docker targets, or an existing Kubernetes cluster, working kubeconfig and persistent
StorageClass for Kubernetes. KyQuickStart does **not** install Docker or Kubernetes.
Supply DNS/HTTPS and edge configuration as described in the guided setup.

Releases are on the [GitHub releases page](https://github.com/Busnes-app/KyQuickStart/releases);
[install one](#install-a-release), or clone this repository and use `make build` with the Go
version in `go.mod`. A source binary cannot install the KyYard runner because it has no released
image pin.

Run `./kyquickstart plan --demo --state ./demo` to explore without installing an app.
Saving a plan does not deploy it. Follow [guided setup](docs/guided-setup.md) to select a
real target/catalog, run preflight, review the plan, apply, and verify the handover.
A demo pass is not a production catalog or disaster-recovery acceptance result.

`make ci` is the core source check. `make e2e` additionally needs disposable Docker/SSH/kind
infrastructure; it does not replace per-product live mail, phone MFA, custodian-card or
offboarding acceptance. Test each configured integration before calling the suite ready.

## Install a release

Each release `vX.Y.Z` (or `vX.Y.Z.N`, a daily dependency release; substitute the whole version
below) publishes `kyquickstart-X.Y.Z-linux-amd64`, `kyquickstart-X.Y.Z-linux-arm64`,
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

[Verifying releases](docs/VERIFYING-RELEASES.md) checks the commit each one was built from and
shows how to rebuild the binaries to the same bytes.

The macOS binaries are not signed or notarized. `gh release download` leaves no quarantine flag;
a browser download gets one and Gatekeeper blocks the binary. After the attestation verifies, clear
it with `xattr -d com.apple.quarantine kyquickstart-X.Y.Z-darwin-<arch>`.

## Build

Requires Go 1.26.6 or later. A source build pins no runner image, so it cannot install KyYard.

```sh
make build
make ci             # tidy check, vet, race tests
make e2e            # needs Docker; real SSH, kind and edge containers
make workflow-lint  # needs Docker; actionlint and zizmor on the release workflows
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
| `stop`, `start`, `restart`, `logs` | Control one installed app on Docker or Kubernetes (suspend/resume there); `status` adds its live state ([guided setup](docs/guided-setup.md#managing-apps)) |
| `integrate-identity`, `integrate-product`, `integrate-stack` | Configure existing Ky products against KyIdentity |
| `review` | Show the saved plan |
| `version` | Print the release set (`internal/appversion/VERSION` in a source build); `--runner-image` prints the runner image a release binary pins |

## Documentation

- [KyQuickStart-AI, the primary installation path](https://github.com/Busnes-app/KyQuickStart-AI)
- [Guided setup](docs/guided-setup.md)
- [Edge and reverse proxy](docs/edge.md)
- [Redirect record: decision, dispositions, edge compatibility](docs/redirect.md)
- [Suite runner (KyYard manages the suite)](docs/superpowers/specs/2026-10-16-suite-runner-design.md)
- [Third-party catalog and admission rules](docs/superpowers/specs/2026-10-06-third-party-catalog-design.md)
- [Installer architecture](docs/superpowers/specs/2026-10-06-installer-architecture-design.md)
- [Verifying releases](docs/VERIFYING-RELEASES.md): check a release's attestation and source commit, and rebuild its binaries to the same bytes
- [Assurance case](docs/ASSURANCE-CASE.md): threat model, trust boundaries, and why the security requirements hold
- [Contributing](CONTRIBUTING.md): process, tests and coding standard

## License

MIT. See [LICENSE.txt](LICENSE.txt).
