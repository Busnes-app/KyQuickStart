# Purpose

Reconcile owned NPM routes, deploy Docker edge connectors, and verify product proxy behavior.

## Ownership

- NPM API client, host ownership and reconciliation: `npm.go`.
- Target-held connector credentials, pinned images and configuration: `connector.go`.
- Public HTTPS forwarded-header proof: `verify.go`.
- Operator configuration and supported scope: `../../docs/edge.md`.
- Before changing frp transport or NPM listeners, read
  `../../docs/superpowers/specs/2026-10-08-frp-edge-research.md` for the upstream constraints and proof.

## Local Contracts

- Read credentials from targets into memory; keep credential values out of hashes, state and errors.
- NPM management uses verified private HTTPS with pinned address resolution and no redirects.
- Reconcile only hosts carrying the expected installer marker and ownership metadata.
  Disable hosts before changing their configuration and enable only after persistence verification.
- Apply disables, never deletes, this target's owned hosts whose hostname left `routes`; verification
  fails while one is enabled. Deletion is the operator's choice.
- Each route trusts exactly its connector IPv4 /32. frp uses a separate TLS listener accepting
  PROXY v2; Cloudflare uses its connecting-IP header. Sanitize forwarding headers before the product.
- Optional connector deployment uses an existing Docker network and existing tunnel/server credentials.
  Containers run with a fixed address, read-only files, no published ports and no capabilities.
- Kubernetes connector deployment remains unavailable until source-address isolation is implemented.
- Public verification requires the product's G11 response, including resolved HTTPS scheme;
  an incomplete response cannot pass verification.

## Work Guidance

Use disposable fixtures for infrastructure tests. Preserve existing operator resources and credentials.

## Verification

- `make ci` covers unit tests, race checks and vet with the e2e tag.
- `make e2e` runs actual pinned NPM API/configuration, frps/frpc routing with client-IP
  preservation and cloudflared ingress parsing.
  The Cloudflare fixture does not prove a live account connection.

## Child DOX Index
