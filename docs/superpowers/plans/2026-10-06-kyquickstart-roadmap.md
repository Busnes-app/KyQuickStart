# KyQuickStart Roadmap Plan

> **For agentic workers:** this is a roadmap, not an executable task list. Implementation waits
> until the suite products it depends on are further along (user decision, 2026-10-06). When a
> phase's gate opens, write that phase's detailed plan with superpowers:writing-plans, then
> execute it with superpowers:subagent-driven-development or superpowers:executing-plans.

**Goal:** Sequence the work that turns the two approved specs into a working `kyquickstart`
installer, and record what each phase waits for.

**Architecture:** A Go CLI drives bring-your-own Docker hosts over SSH (Kubernetes optional)
through a step engine of Inspect → Apply → Verify steps. Apps are embedded manifests plus Go
adapters; Ky products are configured through their container-local `apply-setup`; a resident
bridge handles offboarding; a shared module runs upgrades from the CLI or KyYard.

**Tech Stack:** Go 1.26.6 (suite floor), `golang.org/x/crypto/ssh` (+ `agent`, `knownhosts`),
`go.yaml.in/yaml/v3`, `client-go` (Kubernetes phase), `ky-primitives` (capsule,
recoveryclient, scim).

**Spec:**
- `docs/superpowers/specs/2026-10-06-third-party-catalog-design.md`
- `docs/superpowers/specs/2026-10-06-installer-architecture-design.md`

## Global Constraints

- Module `github.com/Busnes-app/kyquickstart`, `go 1.26.6`, matching the suite repositories.
- One static binary; no runtime dependency on the operator workstation beyond an SSH agent.
- No secret in `stack.yaml`, step results, logs or error output; secrets are created on targets
  and read back.
- Every installer-managed workload carries `ky.managed-by=kyquickstart` and its release-set ID.
- Images pinned by digest; nothing ships as `latest`. Target `linux/amd64`.
- Every step is Inspect → Apply → Verify and idempotent; stop at the first failure.
- A target-side lock for `apply`, `upgrade`, `uninstall`; a stale lock is reported, never broken.
- SSH host keys are pinned; a changed key is a hard failure.
- Tests and CI ship with every phase (`make ci`: tidy check, vet, race tests).

## Review Focus

- **A crashed run leaves the target lock behind.** Expect the next run to stop and name the
  holder and age, and never break the lock itself. Owner: Phase 1 lock tests.
- **A target's SSH host key changes** (reinstall or interception). Expect a hard failure even if
  a trust flag names some other fingerprint. Owner: Phase 1 SSH tests.
- **A command's error output echoes a secret** (for example `docker compose` printing env).
  Expect the secret to be redacted in results and on screen. Owner: Phase 1 engine tests.
- **An operator stops containers by hand between runs.** Expect a re-run to notice the failed
  Verify and redeploy, not skip. Owner: Phase 1 engine tests.
- **`stack.yaml` has a typo, an unknown key or a name containing shell metacharacters.** Expect
  it to be rejected at load, before any connection. Owner: Phase 1 stack tests.

---

## Product readiness gates

Phases name the gates they need. A gate is open when the product has shipped the capability,
with tests, in a tagged release.

| Gate | Product | Capability |
|---|---|---|
| G1 | KyIdentity-server | `kyidentity apply-setup --file`: identities, groups, OIDC clients, roles, *Assigned users only*, assignments, SCIM connector; idempotent, audited, JSON report |
| G2 | KyIdentity-server | Generic SCIM sends `active: false` on manual disable, account end date and lost assignment (verify, fix if not) |
| G3 | kyrecovery-server | `apply-setup` plus unattended in-container `pair generate --service` |
| G4 | kyPulse-server | `apply-setup` for targets and log sources |
| G5 | KyYard-Server | `apply-setup` for enrollment; honors target lock and labelled-workload refusal |
| G6 | KyYard-Server | Imports the shared upgrade module; Upgrade action |
| G7 | Each other Ky product in the release set | `apply-setup` for its own settings and recovery pairing |
| G8 | Holm (upstream or fork) | Back-channel logout or session recheck |
| G9 | KyNotes | OneNote-style notebooks (separate sub-project; not an installer blocker) |
| G10 | kyrecovery-server | Backup status (capsules and restic repositories, read-only) and restore-start UI; `kyrecovery-server/docs/plans/2026-10-07-backup-restore-ui-handoff.md` |
| G11 | Each Ky product behind an edge | Forwarded-header self-check (client address and scheme it resolved, as KyPost `/api/status`); trusted-proxy handling in KyVault, kynotes, kyrecovery and kydns (installer spec, Work in other repositories) |

## Phases

### Phase 1: Installer core (no gate)

Deploys a catalog app to a Docker host over SSH, idempotently and resumably. No OIDC yet.

**Packages and responsibilities:**

| Path | Responsibility |
|---|---|
| `cmd/kyquickstart/main.go` | Subcommand dispatch: `preflight`, `apply`, `version` |
| `internal/cli/` | Command wiring; `Options{Catalog fs.FS; ReleaseSet string; In io.Reader; Out io.Writer}` so tests inject a fixture catalog; `--state` and `--trust-host-key` are flags |
| `internal/stack/` | `stack.yaml` schema v1, strict decode (unknown keys rejected), validation, defaults (port 22, root `/opt/kyquickstart`) |
| `internal/catalog/` | Manifest schema, embedded catalog (`//go:embed all:apps`), compose template parsed at load with `missingkey=error` |
| `internal/plan/` | `Order(apps, catalog)`: dependency order, missing-dependency and cycle errors, deterministic tie-break |
| `internal/engine/` | Step engine, per-step result files, redactor |
| `internal/remote/` | `Runner` interface, shell quoting, SSH runner (agent auth, pinned known_hosts) |
| `internal/dockerhost/` | Secrets, deploy, health steps; managed-label override; lock; preflight |
| `test/e2e/` | Build tag `e2e`: sshd container with the Docker socket, fixture `hello` app (`traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab`) |
| `Makefile`, `.github/workflows/ci.yml` | `make ci`; e2e job |

**Interfaces fixed here, used by every later phase:**

```go
// internal/engine
type Step interface {
	ID() string        // "<app>.<step>", unique; names the result file
	InputHash() string // changes force Apply even if a prior run succeeded
	Inspect(ctx context.Context) (done bool, err error)
	Apply(ctx context.Context) error
	Verify(ctx context.Context) error
}
var ErrTransient = errors.New("transient") // wrap to allow bounded retry

// Engine.Run: prior result ok with same hash and Verify passes -> "skipped".
// Otherwise Inspect -> Apply if not done; after a transient Apply error, Inspect again
// before retrying (a timeout is not a failure); then Verify. Stop at the first failure.

// internal/remote
type Runner interface {
	Run(ctx context.Context, cmd string, stdin []byte) (stdout []byte, err error)
}
func Quote(s string) string // POSIX single-quote escaping

// internal/dockerhost
func Steps(a App) []engine.Step // secrets, deploy, health
func Acquire(ctx context.Context, r remote.Runner, root, holder string) (release func(context.Context) error, err error)
func Preflight(ctx context.Context, r remote.Runner, t stack.Target) []Finding
```

**Fixed behaviors:**

- Secrets: 32 random bytes, base64url, sent on stdin, written with `umask 077; set -C` under
  `<root>/<app>/secrets/`; never overwritten; Verify reads back, checks mode 600, registers the
  value with the redactor.
- Deploy: compose and a generated `compose.kyq.yaml` override adding the managed labels to every
  service; `docker compose -p kyq-<app> pull` (transient on failure) and `up -d
  --remove-orphans`; the input hash stored in `<root>/<app>/.input-hash`.
- Health: `docker compose ps --format json` (NDJSON); healthy when every container is running and
  health is `healthy` or absent; polled until the manifest's `timeout_seconds`.
- Lock: `mkdir <root>/.lock` with a holder JSON on stdin; release only when the holder matches
  (`cmp -s -`). All targets in the run are locked before the first step.
- Host keys: unknown key accepted only on interactive confirmation or a matching
  `--trust-host-key <target>=SHA256:...`; a mismatch is never accepted.
- Preflight floors: Docker Engine 24.0, Compose 2.21, writable root.

**Acceptance:** unit tests for every package; e2e: `apply` succeeds, a second `apply` reports
every step `skipped`, secrets are mode 600 and absent from the state directory, containers carry
the managed label, a pre-existing lock stops the run and survives untouched.

### Phase 2: Kubernetes driver (no gate)

Ky products and edge components (cloudflared, Nginx Proxy Manager, frp client); third-party
apps run on Docker hosts. A `client-go` driver implementing the
same steps with typed objects built in Go, as KyYard builds them (no Helm): kubeconfig client,
one namespace per app, Secrets generated once and read back, objects with readiness probes,
requests and limits and a restricted security context, a `Lease` as the target lock.
Preflight: RBAC, storage classes, Pod Security. Copy from `KyYard-Server/internal/runtime/kubernetes`
(MIT, not importable): rollout wait, access review, Pod Security check, `upsert`, and pod exec for
Phase 6; extract them to a shared module once both copies settle. Acceptance: Phase 1 e2e matrix
on a kind cluster.

### Phase 3: Identity (gates G1, G2)

OIDC client and roles per app through `kyidentity apply-setup`; owner admin and everyday
identities; `GrantAdmin` adapter hook; Verify that the everyday identity holds no admin role.
Acceptance: adapter integration harness (real app container + test KyIdentity) used by Phase 7.

### Phase 4: Edge and proxy (no gate)

NPM API driver (inspect, create disabled, verify, enable; forwarded-header override; never other
hosts or the database), install NPM when absent with admin LAN-only, acme-dns DNS-01, BYO
Cloudflare (two scoped tokens), frp on a VPS in `https` SNI mode. cloudflared, NPM and the frp
client deploy to a Docker host or, through the Phase 2 driver, the cluster; the cluster variants
land after Phase 2. One edge per target, one owner per hostname, and each product's trusted-proxy value set by the
installer (installer spec, Edge placement); `verify` checks the client address each product
sees (KyPost now, the rest at G11). Resolve the two unverified frp questions first. Acceptance includes a
scripted check that NPM's admin port is unreachable from the edge.

### Phase 5: Wizard, verify, handover (after Phase 3)

`plan` wizard writing `stack.yaml` (products with purpose and dependencies, placement, edge,
backup host, owner logins), as plain terminal prompts (command-line only, no GUI); `verify` acceptance framework; `handover` printout; `uninstall`
keeping data; `unmanage` handing an app to its owner.

### Phase 6: Ky product setup (gates G3, G4, G5, G7)

`apply-setup` steps for each Ky product; KyRecovery ceremony as a human gate that parks
dependent steps; in-container pairing; kyPulse registration; KyYard enrollment.

### Phase 7: Catalog apps (after Phase 3; Holm also G8)

One detailed plan per app, Docker hosts only, each passing the adapter integration harness: Forgejo, BookStack,
Vikunja, Immich, Home Assistant + hass-oidc-auth, Dolibarr, LibreChat, Holm. Each resolves its
unverified items from the catalog spec first.

### Phase 8: Backups (after Phase 6)

rest-server as a container on the KyRecovery host (`--append-only --private-repos`, TLS,
separate prune credential; preflight refuses NAS storage); K8up `Schedule` and
`k8up.io/backupcommand` per Ky product on Kubernetes; restic timers on Docker hosts running each
app's dump command; one repository and password per
app, the password on the target with a copy sealed in KyRecovery; check results to kyPulse;
`kyquickstart restore <app>`; restore drill in `verify`. KyRecovery status and restore-start UI
is gate G10.

### Phase 9: Offboarding bridge (gate G2, after Phase 7)

Resident SCIM target built on `ky-primitives/scim`, running each app's `Deprovision`; narrow
per-app credentials; network policy; kyPulse audit and alerts; `offboard <user>` fallback.

### Phase 10: Upgrades (gate G6, after Phase 7)

Shared upgrade module (rules, verified pre-upgrade capsule and restic snapshot, one app at a
time, no image rollback after a migration); `kyquickstart upgrade`; KyYard Upgrade action;
upstream release tracking in CI.

## Order

Phases 1, 2 and 4 can start without any product gate. Phase 3 opens most of the rest; Phase 7
can run per app in parallel once Phase 3 lands.
