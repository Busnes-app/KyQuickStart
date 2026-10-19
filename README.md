# KyQuickStart

One command-line installer that stands up the Ky suite and selected third-party apps across
your own servers: choices, prerequisite checks, installation, preconfiguration and a printed
handover of administrator and everyday logins.

**Status:** pre-release. The production catalog holds Nginx Proxy Manager as the edge on a
Kubernetes target ([docs/edge.md](docs/edge.md)), KyIdentity behind it
([docs/guided-setup.md](docs/guided-setup.md#catalog-kyidentity-on-kubernetes)), and KyRecovery,
kyPulse, KyNotes and KyCalendar signing in through it ([docs/guided-setup.md](docs/guided-setup.md#catalog-kyrecovery-on-kubernetes),
[kyPulse](docs/guided-setup.md#catalog-kypulse-on-kubernetes), [KyNotes](docs/guided-setup.md#catalog-kynotes-on-kubernetes),
[KyCalendar](docs/guided-setup.md#catalog-kycalendar-on-kubernetes)), with KyDrive on a Docker host
routed through that edge ([KyDrive](docs/guided-setup.md#catalog-kydrive-on-docker));
otherwise only a disposable demo app installs.
`integrate-product` connects already-running Ky products to KyIdentity. See
[the deployment plan](docs/superpowers/plans/2026-10-10-deployment-plan.md) for what remains.

## Targets

- Docker hosts over SSH (pinned host keys, SSH agent authentication).
- Kubernetes clusters through an existing kubeconfig. App volumes use the cluster's default
  StorageClass, or the one a target names with `storage_class`.

Nothing is installed on your workstation beyond the binary. Secrets are generated on the
target and never written to local state, logs or command output.

## Build

Requires Go 1.26.6 or later.

```sh
make build
make ci    # tidy check, vet, race tests
make e2e   # needs Docker; real SSH, kind and edge containers
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
| `verify-edge` | Check public routing and forwarded headers |
| `integrate-identity`, `integrate-product`, `integrate-stack` | Configure existing Ky products against KyIdentity |
| `review` | Show the saved plan |
| `version` | Print the release set |

## Documentation

- [Guided setup](docs/guided-setup.md)
- [Edge and reverse proxy](docs/edge.md)
- [Offboarding bridge](docs/offboarding.md)
- [Suite runner (KyYard manages the suite)](docs/superpowers/specs/2026-10-16-suite-runner-design.md)
- [Third-party catalog and admission rules](docs/superpowers/specs/2026-10-06-third-party-catalog-design.md)
- [Installer architecture](docs/superpowers/specs/2026-10-06-installer-architecture-design.md)

## License

MIT. See [LICENSE.txt](LICENSE.txt).
