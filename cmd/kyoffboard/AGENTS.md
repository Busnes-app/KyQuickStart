# Purpose

Standalone resident offboarding bridge command.

## Ownership

- This command owns flag parsing, signal handling, exit codes and safe output.
- `internal/offboard` owns protected configuration, TLS serving and worker lifecycle.
- The root owns deployment, credential provisioning and catalog qualification.

## Local Contracts

- Require an absolute private target configuration via `--config`; credentials are never
  command-line values. `--check` reads/validates only and reports schema/status/app count.
  `--init-tls` creates the missing bridge key/leaf (no output; the image has no shell).
  `--healthcheck` probes the local listener for the Compose healthcheck: exit 0 silently, or
  1 with one fixed line. The modes are exclusive (exit 2) and none writes state.
- Help exits 0, usage errors exit 2 and configuration/service failures exit 1. Graceful signal
  cancellation exits 0. Diagnostics contain no supplied flags, paths, file bodies or tokens.
- This executable does not admit products, compose KyIdentity connectors or prove deployment.
- Accepted risk (Yoshi, 2026-10-10): any `bookstack`/`forgejo`/`immich` app in operator
  config is served without a code-level qualification check; the catalog gate is procedural.
- Image: `Dockerfile` here (build context is the repo root), static on distroless `nonroot`,
  uid 65532, no shell, OCI source/revision/version labels from build args. ci.yml's
  "kyoffboard image build & run" proves `--check` and TLS serving read-only with no capabilities.
- Publication: a pushed `kyoffboard-vX.Y.Z` tag runs `kyoffboard-image.yml`, which publishes only
  `ghcr.io/busnes-app/kyoffboard:X.Y.Z` after every ci.yml job passed for the tagged commit, then
  attests and verifies it. Pushing the tag is Yoshi's step. Pin only the attested digest; no
  source in this repo pins a kyoffboard digest yet.

## Work Guidance

## Verification

- `go test ./cmd/kyoffboard`: exit-code/diagnostic boundary and private input redaction;
  `--init-tls` twice, then serve and `--healthcheck` before and while serving.
- `internal/offboard` tests verify protected loading and serving behavior.
- `workflow_test.go`: `kyoffboard-image.yml` trigger, permissions, SHA-pinned actions, CI guard
  naming every ci.yml job, attestation identity and gitCommit check; no kyoffboard digest pin.

## Child DOX Index

None.
