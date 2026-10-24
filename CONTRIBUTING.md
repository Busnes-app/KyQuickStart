# Contributing

[Overview](README.md) · [Assurance case](docs/ASSURANCE-CASE.md) · [Verifying releases](docs/VERIFYING-RELEASES.md)

KyQuickStart's expansion is frozen: no new catalog apps and no new standalone orchestration
features. Compatibility, safety fixes, verification and the KyQuickStart-AI integration are
welcome ([redirect record](docs/redirect.md)).

## Reporting

- **Bugs and requests:** open a GitHub issue. Say what you ran, on which target type (Docker over
  SSH or Kubernetes), and what happened. Leave secrets and handover output out.
- **Vulnerabilities:** report privately through the repository's **Security** tab, not in an issue.
  The [Busnes-app security policy](https://github.com/Busnes-app/.github/blob/main/SECURITY.md)
  covers this repository and gives the response times.

## Changing the code

1. Read [AGENTS.md](AGENTS.md) and the `AGENTS.md` files on the way to what you change. They are
   the binding contracts for their directories. A change to a contract, workflow or durable
   structure updates the nearest one in the same pull request.
2. Open a pull request against `master`. The `main: green checks` ruleset requires a pull request
   and the `test (ubuntu-latest, stable)`, `test (ubuntu-latest, 1.26.x)` and `vulncheck` checks.
   A release also needs the whole `ci.yml` green, macOS legs, `e2e` and the runner image included
   (`release-image.yml`, `REQUIRED`).
3. Run `make ci` before you push. Run `make workflow-lint` (needs Docker) when you change a
   workflow. `make e2e` needs Docker and builds kind clusters (about 40 minutes); CI runs it in
   four shards, so you only need it locally for the part you changed.

### Tests

New behaviour and every bug fix come with a test that fails without the change, in the package
it changes. Refusals and other security-relevant behaviour get a test of the refused path, not
only the happy path. Work that touches targets also needs an e2e case against the disposable
sshd and kind fixtures (`test/e2e`); never against a live deployment.

### Coding standard

Contributions follow these style guides. CI enforces the parts a tool can check, so a style
failure is a red build, not a review comment.

| Language | Style guide | Enforced by |
|---|---|---|
| Go | [Effective Go](https://go.dev/doc/effective_go) and the [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) | `make ci` in the `test` job: `gofmt -l` must print nothing, `go vet ./...` and `go vet -tags e2e,goolm ./...`, and `go mod tidy -diff` |
| Commands run on targets | POSIX `sh`, every value quoted with `remote.Quote` | Not by a linter: review, plus `internal/remote` tests that run the SSH wrapper under sh, bash and fish login shells |
| Shell scripts (`scripts/`) | POSIX `sh` with `set -eu` | `cmd/kyquickstart/release_test.go` runs `scripts/release-binaries.sh` under `sh` |
| GitHub Actions workflows | Actions in the release workflows pinned by commit; each job holds only the permissions it uses | `make workflow-lint` in the `vulncheck` job: `actionlint` on every workflow, `zizmor` (medium and up) on `release-image.yml` and `dependency-release.yml` |

Beyond what the tools check: follow the engineering principles at the top of AGENTS.md
(validate at boundaries, separate before serialising shared state), keep to the comment density
and naming of the file you are editing, and use the standard library where it will do.
