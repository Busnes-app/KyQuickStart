# Third-party adapters

## Purpose

App-specific native setup, administrator verification and offboarding for admitted external apps.

## Ownership

- `forgejo/`: native HTTPS source forms and administration API, exact account/source/subject
  inspection, native directory creation/receipts, profile/role projection, administrator-grant verification and
  disable/token revocation and scoped native teams/membership.
- `bookstack/`: receipt-owned ordinary creation, profile/role projection and retained-account
  offboarding through native commands and separate scoped HTTPS authorities; disposable
  qualification and missing-interface evidence.
- `immich/`: pinned native provider configuration/account inspection, actual KyIdentity OIDC,
  verified administrator/profile projection, community owned initial setup with a service
  administrator, ordinary account creation, retained-account revocation and reactivation, and
  the catalog server image. Built for the catalog; it ships once its admission gates close in
  `make e2e` and its images are published (H1).
- `librechat/`: native account inspection SDK, internal retained-revocation primitives and
  pinned lifecycle qualification; deployment waits for complete native revocation/setup proof.
- The root owns admission, release sets, deployment, target locks and orchestration.

## Local Contracts

- Use native CLIs/APIs; never add an authentication proxy or edit app databases.
- Pin native identity and authentication-source IDs plus OIDC subject before mutations.
  Username/email alone never authorizes adoption, grants or revocation.
- Credentials stay in memory or protected target files; errors and reports omit server bodies.
- Verified HTTPS, bounded responses/timeouts, no environment proxy and no redirects for APIs.
  The one exception is a bridge in the app's own Compose project reaching it by service name
  over its internal network (Immich's `NewInternal`), like the edge-to-app hop.
- Offboarding disables first and retains content; verify authority and access before success.
  Token deletion is bounded and resumable; an unknown mutation outcome requires inspection.
- A partial API adapter does not admit an app or satisfy OIDC/deployment/backup acceptance.

## Work Guidance

Reuse upstream claim-based administrator grants when available and verify their live effect.

## Verification

- `go test -race ./internal/adapters/...`: identity-response validation, redirect refusal
  and token-list validation before mutation.
- Per-product native fixtures and their limits live in the owning child DOX.

## Child DOX Index

- `forgejo/AGENTS.md`: release-qualified source forms, exact native identity bindings and
  real-container OIDC/offboarding verification.
- `bookstack/AGENTS.md`: native retained-account module, upstream offboarding qualification and fixture limits.
- `immich/AGENTS.md`: exact provider/subject receipts, supported native grants, reactivation and fixture limits.
- `librechat/AGENTS.md`: native inspection SDK/module, pinned lifecycle qualification and remaining interface gates.
