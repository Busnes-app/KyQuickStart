# Purpose

Standalone resident offboarding bridge command.

## Ownership

- This command owns flag parsing, signal handling, exit codes and safe output.
- `internal/offboard` owns protected configuration, TLS serving and worker lifecycle.
- The root owns deployment, credential provisioning and catalog qualification.

## Local Contracts

- Require an absolute private target configuration via `--config`; credentials are never
  command-line values. `--check` reads/validates only and reports schema/status/app count.
- Help exits 0, usage errors exit 2 and configuration/service failures exit 1. Graceful signal
  cancellation exits 0. Diagnostics contain no supplied flags, paths, file bodies or tokens.
- This executable does not admit products, compose KyIdentity connectors or prove deployment.
- Accepted risk (Yoshi, 2026-10-10): any `bookstack`/`forgejo`/`immich` app in operator
  config is served without a code-level qualification check; the catalog gate is procedural.

## Work Guidance

## Verification

- `go test ./cmd/kyoffboard`: exit-code/diagnostic boundary and private input redaction.
- `internal/offboard` tests verify protected loading and serving behavior.

## Child DOX Index

None.
