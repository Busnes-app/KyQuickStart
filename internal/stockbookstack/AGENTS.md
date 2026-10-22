# Purpose

The catalog BookStack's installer-side code: the offboarding receiver and the setup clients the
`kyoffboard bookstack-*` subcommands run. It talks to stock BookStack over its own HTTP
interfaces only and never runs inside BookStack.

## Ownership

- `api.go`: BookStack's REST API with the service account's token (read per call).
- `receiver.go`: KyIdentity's signed directory events, verified before parsing; removes roles only.
- `bootstrap.go`: the standard sign-in and settings forms of the one-off standard-auth
  BookStack: one token, Editor as the registration default, the token written last.
- `setup.go`: the token check and the admin role's mapping to the admin claim.
- `cmd/kyoffboard` owns flags, exit codes and diagnostics; `internal/cli` owns the steps and
  Compose operations; the root owns the catalog entry.

## Local Contracts

- Stock only: no module, theme, patch, `artisan tinker` or SQL. BookStack's API filter is
  case-insensitive, so matches are compared exactly here.
- Never grant a role, delete an account or touch the service account (`admin@admin.com`).
- Logs and errors carry no token, secret or request body.

## Verification

- `go test ./internal/stockbookstack`: the receiver against a fake BookStack API (signature,
  size, matching, removal read-back, demotion, 503, logs) and the role mapping.
- `make e2e` (`TestKubernetesBookStack`): against stock BookStack.

## Child DOX Index

None.
