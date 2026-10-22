# Purpose

Reconcile owned NPM routes, deploy Docker edge connectors (including cloudflared routing
straight to the apps, `cloudflare-direct`), and verify product proxy behavior.

## Ownership

- NPM API client, host ownership and reconciliation: `npm.go`. `RouteApp` names a route step's
  app, so apply can run an app's setup before its route.
- NPM's own administrator (setup-mode creation, login, verification, rotation) for bootstrap: `admin.go`.
- Let's Encrypt DNS-01 certificates, their per-email NPM user and route name resolution: `certs.go`.
- KyDNS LAN A records and their ownership ledger: `kydns.go`.
- Target-held connector credentials, pinned images and configuration: `connector.go`, including
  a `cloudflare-direct` edge's ingress (each hostname to its route's `Origin`, then 404) and its
  dedicated-network check.
- Public HTTPS forwarded-header proof: `verify.go`.
- Operator configuration and supported scope: `../../docs/edge.md`.
- Before changing frp transport or NPM listeners, read
  `../../docs/superpowers/specs/2026-10-08-frp-edge-research.md` for the upstream constraints and proof.

## Local Contracts

- Read credentials from targets into memory; keep credential values out of hashes, state and errors.
- NPM management uses verified private HTTPS with pinned address resolution and no redirects, or
  for `kube://` plain HTTP inside the Kubernetes driver's port-forward, never a local listener.
- Reconcile only hosts carrying the expected installer marker and ownership metadata, including
  the installation ID. A non-primary route's marker carries its role (`<app>:<role>:<hostname>`).
  A route's catalog `Paths` are the only paths its host proxies; `DenyPaths` answer 404. Legacy target-only marks are claimed and rewritten only with `Adopt`.
  Disable hosts before changing their configuration and enable only after persistence verification.
- A certificate is this installation's only when its owner is the NPM user marked
  `kyquickstart:<installation>:<target>` for its email; never reuse or change another owner's.
  The DNS credential goes to NPM only in the certificate request body, registered with the
  redactor first. A failed issuance is not retried within a run (Let's Encrypt rate limits).
- KyDNS records carry no owner: a record is ours only while the ledger in
  `kyq-edge/kyq-credentials` lists its ID, name and address. Save the ledger after every KyDNS
  change; refuse any name another record or service publishes. Show KyDNS error codes, never bodies.
- Apply disables, never deletes, this target's owned hosts whose hostname left `routes`; verification
  fails while one is enabled. Deletion is the operator's choice.
- Each route trusts exactly its connector IPv4 /32. frp uses a separate TLS listener accepting
  PROXY v2; Cloudflare uses its connecting-IP header. Sanitize forwarding headers before the product.
- A `lan` edge has no connector and trusts no forwarding header: `set_real_ip_from unix:;`
  overrides NPM's `nginx.conf` trust of `X-Real-IP` from every private range, then each
  `allow_cidrs` prefix is allowed, everyone else denied, and the SNI check kept. Its
  verification runs from an allowed client, on `client_port`.
- Optional connector deployment uses an existing Docker network and existing tunnel/server credentials.
  Containers run with a fixed address, read-only files, no published ports and no capabilities.
- A `cloudflare-direct` edge has no NPM: its connector is required, routes go to
  `http://kyq-<app>:<port>` on its network (the CLI sets `Route.Origin`), and `CheckConnector`
  refuses an IPv6-enabled network and one holding any container without the managed label. Its apps trust exactly
  `connector_cidr`. Never add a TLS bypass or route to another host.
- Kubernetes connector deployment, `cloudflare-direct` included, remains unavailable until
  source-address isolation is implemented.
- Public verification requires the product's G11 response, including resolved HTTPS scheme;
  an incomplete response cannot pass verification.

## Work Guidance

Use disposable fixtures for infrastructure tests. Preserve existing operator resources and credentials.

## Verification

- `make ci` covers unit tests, race checks and vet with the e2e tag.
- `make e2e` runs actual pinned NPM API/configuration, frps/frpc routing with client-IP
  preservation and cloudflared ingress parsing. The kind NPM test also covers a `lan` route.
  `TestCloudflaredDirectContainer` matches a direct ingress's hostnames with the pinned
  cloudflared and proves, on a real reserved network, the app sees exactly the connector's
  address and a foreign container is refused. The Cloudflare fixtures do not prove a live
  account connection.

## Child DOX Index
