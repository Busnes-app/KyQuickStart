# Assurance case

[Overview](../README.md) · [Verifying releases](VERIFYING-RELEASES.md) · [Contributing](../CONTRIBUTING.md)

This is the argument that KyQuickStart meets its security requirements: what it protects, from
whom, where the trust boundaries are, and why the design and the code hold at each one. The
binding contracts live in [AGENTS.md](../AGENTS.md) (Local Contracts) and the
[installer architecture](superpowers/specs/2026-10-06-installer-architecture-design.md); this page
ties them together and points at the evidence. Paths are relative to the repository root.

## What KyQuickStart must protect

KyQuickStart runs on the operator's workstation (or as the suite runner in KyYard's pod) and
installs the Ky suite on Docker hosts over SSH and on Kubernetes clusters. Its security
requirements:

1. **It acts only on the hosts the operator chose.** It never talks to an impostor host or
   reaches a cluster other than the one a kubeconfig names.
2. **Commands mean what they say.** Plan values cannot change the shell command run on a
   target, whatever the target's login shell.
3. **Secrets stay where they are made.** Product secrets are generated on the target and never
   copied into the workstation's state directory, plans or ordinary output.
4. **It runs only the software it was released with.** Catalog images are pinned by digest, and
   pins move only to attested releases of the product's own repository.
5. **It changes only what it owns.** Workloads, edge hosts and DNS records belong to one
   installation, and nothing else is changed, adopted or deleted.
6. **Users can check what they run.** Release binaries and the runner image are attested and
   rebuild to the same bytes ([VERIFYING-RELEASES.md](VERIFYING-RELEASES.md)).

## Threat model

| Attacker | Can | Wants |
|---|---|---|
| Network attacker between workstation and target | Read and alter SSH or Kubernetes API traffic | Run commands on a target, steal secrets |
| Someone who reads a shared plan, state directory or log | Read `stack.yaml`, `<state>/`, command output | Product secrets, admin credentials |
| Malicious value in a plan or catalog field | Put shell metacharacters in names, paths, hosts | Run other commands on the target |
| Compromised upstream image or registry | Publish a new image or move a tag | Get it installed across the suite |
| A second installation or plan on the same target | Run `kyquickstart` against it | Overwrite or take over the first installation |
| A process beside the suite runner | Reach KyYard's pod or Docker host | Drive the runner's socket |
| Someone who tampers with a release download | Change bytes in transit or on a mirror | Get a modified binary run with the operator's SSH agent |

Out of scope: an attacker who controls the operator's workstation or SSH agent (they are the
operator), and a target host that is already compromised (it holds its own apps' secrets by
design). The handover output contains administrator logins on purpose; protecting it is the
operator's job ([README](../README.md#targets)).

## Trust boundaries

1. **Workstation → Docker target over SSH.** Host keys are pinned in `<state>/known_hosts`; an
   unknown key is pinned only after the operator confirms its fingerprint or passes
   `--trust-host-key`, and a changed key is always refused (`internal/remote/ssh.go`,
   `hostKeyCallback`; `TestChangedHostKeyIsRefused`). Authentication is the SSH agent or a
   private key file.
2. **Workstation → Kubernetes API.** Through the operator's kubeconfig, referenced by path and
   refused inside the state directory (`internal/cli/cli.go`). Each app gets namespace
   `kyq-<app>` with Pod Security `restricted` and a `default-deny-ingress` policy
   (`internal/kube/namespace.go`, `internal/kube/names.go`).
3. **Installer → product.** The installer's only authority inside a product is its
   container-local `apply-setup` command; everything it manages carries
   `ky.managed-by=kyquickstart` (`internal/managed/managed.go`).
4. **Catalog → registries.** Images are referenced only by digest, and catalog-follow moves a pin
   only after `gh attestation verify` against the product's release workflow at the tag
   (`scripts/catalog-follow/main.go`).
5. **KyYard → suite runner.** A Unix socket, mode 0600, in a 0700 state volume, as uid 10001
   (`internal/runner/server.go`, `internal/runner/state.go`). The runner acts only on targets
   marked with its own ID and never changes its own pod (`runner_host`).

## Secure design principles applied

- **Least privilege.** The runner image is distroless with no shell, runs as uid 10001 and serves
  read-only with no capabilities (CI's `Runner image build & run` job checks each). Release jobs hold only
  the permissions they use (`release-image.yml`). Kubernetes app namespaces run `restricted`;
  the two `baseline` exceptions are named in the catalog and in AGENTS.md.
- **Fail-safe defaults.** An unknown host key, an unpinned image, a kubeconfig inside the state
  directory, a plan marker naming another installation (`owned_by_other_plan`) or a newer plan
  (`plan_stale`) all stop the run instead of continuing.
- **Complete mediation.** Every run reads each target's plan marker under that target's lock before
  it acts (`internal/cli/cli.go`, `lockOne` and `guard`), so a stale or foreign plan is refused on
  each run, not only the first.
- **Economy of mechanism.** One quoting function for every remote word and one wrapper for every
  SSH command; one release build script (`scripts/release-binaries.sh`) for all four binaries.
- **Separation of privilege.** Handing the suite to KyYard (`handoff`) is the only way a marker's
  owner changes; the CLI and the runner each refuse plans the other owns.
- **Open design.** Only published algorithms, through Go's standard library and `x/crypto/ssh`
  (SSH, TLS, SHA-256 digests, the runner's ed25519 key); the code and this argument are public.

## Common weaknesses and how they are countered

| Weakness | Counter | Where |
|---|---|---|
| Shell injection on a target | Every value is one POSIX word via `remote.Quote`; each command reaches `sh` base64-encoded, so a fish or other login shell cannot reinterpret it; a test runs the wrapper under sh, bash and fish | `internal/remote/remote.go` (`Quote`), `internal/remote/ssh.go` (`wrap`), `internal/remote/ssh_test.go` |
| Man-in-the-middle on SSH | Pinned host keys, changed key refused, host asked only for the pinned key types | `internal/remote/ssh.go` (`hostKeyCallback`, `hostKeyAlgorithms`) |
| Weak or leaked secrets | 32 bytes from the target's `/dev/urandom`; on Docker written once into a private file (`ln` refuses replacement); read back only from a regular 0600 file owned by the user, under no symlinked directory and no parent others can write to without the sticky bit | `internal/dockerhost/secrets.go` (`generateSecret`), `internal/kube/generate.go`, `internal/remote/secret.go` (`ReadSecret`) |
| Secrets in output | Every secret read back is registered with the redactor; step errors and `logs` output pass through it | `internal/engine/redact.go`, `internal/cli/lifecycle.go` |
| Unintended network exposure | A Compose `ports:` entry must name the plan's forward address or `127.0.0.1`; no bare or wildcard publication | `internal/catalog/catalog.go` |
| Supply-chain substitution | Digest-pinned images; attestation-checked pin updates; release workflows pin every action by commit | `internal/catalog/catalog.go` (`imageRE`), `scripts/catalog-follow/main.go`, `.github/workflows/release-image.yml` |
| Tampered release | Attested binaries and image, verified before upload and again after download; repeatable binaries | [VERIFYING-RELEASES.md](VERIFYING-RELEASES.md) |
| Vulnerable dependencies | `govulncheck` on every push and pull request (`vulncheck` job); Dependabot; daily dependency releases | `.github/workflows/ci.yml`, `.github/workflows/dependency-release.yml` |

## Residual risk

- **First contact is trust on first use.** The first host key is accepted on the operator's word;
  the error message tells them to compare it with `ssh-keygen -lf` on the host.
- **Redaction covers registered secrets only.** A secret the installer never read back, or one
  shorter than 8 characters, is not redacted.
- **The handover prints credentials by design.** Anyone who sees that output can sign in as the
  owner's administrator.
- **`ci.yml` uses actions by tag, not commit** (Scorecard's Pinned-Dependencies alerts). The release
  workflows are pinned.
- **macOS binaries are not signed or notarized**; the attestation is the only check
  ([README](../README.md#install-a-release)).
- **No independent security audit** has been done.

## Evidence

- **Tests:** `make ci` runs the whole suite under the race detector on Linux and macOS, two Go
  versions each. The e2e suite (`make e2e`, four CI shards) installs onto real sshd containers
  and kind clusters, including drift, stale-lock and refusal cases.
- **Static analysis:** CodeQL (GitHub default setup, Go and Actions) on every push and pull
  request, `go vet` (with and without the `e2e,goolm` tags), `govulncheck`, and `actionlint`
  and `zizmor` on the workflows (`make workflow-lint`).
- **Dynamic analysis:** the race detector on the full suite and the e2e suite against real
  targets.
- **Review:** the `main: green checks` ruleset requires a pull request into `master` with
  `test (ubuntu-latest, stable)`, `test (ubuntu-latest, 1.26.x)` and `vulncheck` green; repository
  administrators can bypass it.
- **Releases:** attested binaries and runner image; binaries rebuild to the same bytes
  ([VERIFYING-RELEASES.md](VERIFYING-RELEASES.md)).

This case is updated when a trust boundary or a security requirement changes. A pull request that
moves one must update this page in the same change.
