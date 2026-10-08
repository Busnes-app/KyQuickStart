# Installer architecture

Date: 2026-10-06. Status: proposed, awaiting review.

How `kyquickstart` installs, configures, verifies, offboards and upgrades the Ky suite and the
third-party catalog (`2026-10-06-third-party-catalog-design.md`). The catalog spec decides what
is installed; this spec decides how.

## Decisions

- **Targets are bring-your-own.** Docker hosts reached over SSH, and optionally an existing
  Kubernetes cluster through a kubeconfig. An install may be Docker-only, Kubernetes-only or
  mixed. The installer checks prerequisites and reports gaps; it never installs Docker or
  Kubernetes.
- **Third-party apps run on Docker hosts only; Kubernetes hosts Ky products and the edge.** Most
  catalog projects publish Compose themselves, so one format per app stays close to upstream. Ky
  products are ours to package either way. The edge components (cloudflared, Nginx Proxy Manager,
  frp client) deploy to a Docker host or the cluster; the frp server runs on the VPS.
- **Catalog support has an exit.** An app leaving the catalog stops taking new installs after
  notice in a release set; existing installs are handed to the owner with `unmanage`.
- **Go**, one static binary run from the operator's workstation. It imports `ky-primitives`
  for sealing, pairing and key pinning.
- **SSH bootstrap, then KyYard.** The installer deploys directly, then enrolls every target in
  KyYard. KyYard starts upgrades through the shared upgrade module (see Upgrades); it does not
  otherwise edit installer-managed workloads.
- **Secrets live on their targets.** The workstation holds a secret-free plan and step results.
- **Each app is a manifest plus a Go adapter**, embedded in the binary. A binary release is a
  tested release set.
- **Ky products gain a container-local `apply-setup --file <bundle>` command.** It is the
  installer's only authority inside a Ky product.
- **A resident offboarding bridge** turns KyIdentity deactivations into per-app deprovisioning.

## Components

### CLI

| Command | Does |
|---|---|
| `plan` | Interactive wizard. Per product: purpose, install/skip, dependencies. Targets (SSH hosts, optional kubeconfig), placement of each app, edge (BYO Cloudflare or frp VPS), off-NAS backup host, owner's admin and everyday logins. Writes `stack.yaml` with no secrets. Re-running edits the saved plan |
| `preflight` | Read-only checks per target: SSH, Docker and Compose versions, Kubernetes RBAC, storage classes and Pod Security, DNS, ports, disk, image pulls. Reports every gap before any change |
| `apply` | Runs the plan as a dependency graph (see Install sequence). Resumable and idempotent |
| `verify` | Acceptance checks (see Verification) |
| `handover` | Prints URLs, admin and everyday logins, activation links, NPM admin credentials, and outstanding human steps |
| `offboard <user>` | Runs every app's `Deprovision` for one user; fallback for the bridge |
| `upgrade` | Runs the shared upgrade module against a newer release set |
| `restore <app>` | Restores one app to a point in time: capsule through the product's `restore`, then the matching restic snapshot (database first, then files). Shares on stdin only. Started directly or from the command KyRecovery's UI shows (catalog spec, Restore) |
| `uninstall <app>` | Removes workloads, keeps data. Data deletion is a separate command with typed confirmation |
| `unmanage <app>` | Hands an app to the owner: removes the `ky.managed-by` label so KyYard treats it as an ordinary workload, keeps its data and last backup, and records in the handover that the suite no longer upgrades it |

`plan` produces the file every other command consumes, so an unattended run is `apply` on an
existing `stack.yaml`. Placement is validated against dependencies (an app and its database must
be mutually reachable). Skipping an installed app never removes it.

### Catalog

Per app, embedded in the binary:

- **Manifest** (schema-checked): images by digest, Compose template (third-party apps and Ky products), Kubernetes objects (Ky
  products and edge components), ports, env,
  OIDC client (redirect, logout and back-channel URLs, role names), health endpoint, backup
  declaration (dump command, volumes, skips, capsule secrets, restore order), upgrade rules
  (minimum from-version, versions that cannot be skipped, whether it migrates data).
- **Go adapter** for what data cannot express: `Inspect`, `Apply`, `GrantAdmin`, `Deprovision`.

Ky products and third-party apps use the same format. The catalog and upgrade module are a Go
module importable by KyYard.

### Target drivers

- **Docker over SSH:** one Compose project per app under an installer-owned directory; secrets
  as mode-0600 files beside it. Host key pinned on first contact and shown for confirmation.
- **Kubernetes (Ky products and edge components):** `client-go` with typed objects built in Go,
  the way KyYard builds them; no Helm. One namespace per app; secrets as Kubernetes Secrets.
  Objects set readiness probes, resource requests and limits, and a restricted security context.

Every installer-managed workload carries `ky.managed-by=kyquickstart` and its release-set ID.

### Edge placement

Ky products believe forwarded headers (client IP, scheme) only from a trusted-proxy address list
(`TRUSTED_PROXY_CIDRS`, `KY_TRUSTED_PROXIES` and kin). Behind NAT or on the wrong network every
visitor shares the proxy's address: one lockout bucket, wrong audit IPs, cookies not Secure, and
KyYard refuses to start. The installer therefore places proxies and sets those values itself.

- **One edge per target.** Each target serving HTTP gets its own Nginx Proxy Manager, and with
  Cloudflare its own tunnel (more connectors in one tunnel only for redundancy, since every
  connector must reach every origin); with frp, one client per target behind the VPS server. An
  existing NPM may serve as a target's edge when it meets the rules below.
- **One owner per hostname.** `plan` rejects a hostname claimed by two edges.
- **Trusted proxy, set by the installer, never `0.0.0.0/0` or a default bridge:**
  - Docker host: NPM joins each app's Compose network with a pinned address; the app trusts that
    /32.
  - Proxy on another host: the app trusts that host's stable LAN /32, with no NAT on the path
    (NodePort with `externalTrafficPolicy: Local`, source-restricted).
  - Kubernetes: pod addresses move, so the app trusts the pod network and a NetworkPolicy admits
    ingress to it only from the edge pods.
- **Verified, not assumed.** `verify` requests each product through its edge and checks the
  client address and scheme the product reports (KyPost `GET /api/status`: `clientIp`,
  `proxyHeadersTrusted`; the other products through gate G11).
- **Certificates per edge.** Each NPM issues its own by DNS-01 with its own acme-dns
  registration. The handover lists every NPM admin login.

### Product setup

- **Ky products:** `<product> apply-setup --file <bundle>`, run with `docker exec` over SSH or
  `kubectl exec`. Contract: creates only what is missing, never overwrites existing settings,
  writes one audit row per change, reports what it did as JSON, exposes no network credential.
  Container access already implies full control of the product, so it grants nothing new.
- **KyIdentity's bundle:** owner's admin and everyday identities, groups, OIDC clients, app roles,
  *Assigned users only* access, assignments, the bridge's SCIM connector.
- **Third-party apps:** their own CLIs and local APIs through the adapter (for example
  `forgejo admin auth add-oauth`, `vikunja user set-admin`).
- **Human steps stay human:** the KyRecovery custodian ceremony. A pairing code is generated
  in-container with `kyrecovery pair generate` when the same operator runs KyRecovery; otherwise
  the code is entered by hand.

### Offboarding bridge

A resident Go service built from this repository, deployed by the installer.

- A generic SCIM 2.0 target registered in KyIdentity. On a user's deactivation it runs every
  app's `Deprovision` adapter: disable the account, revoke sessions and app-issued tokens.
- Holds one credential per app, the narrowest each app allows, stored on the bridge's own target.
- Network policy: inbound only from KyIdentity; outbound only to the catalog apps' APIs and
  kyPulse. No other ingress.
- Every action and failure is audited to kyPulse. Failed deprovisions retry with backoff and
  raise a kyPulse alert.

## State and secrets

- **Workstation:** a state directory with `stack.yaml` and one result file per step (status,
  input hash, time, redacted diagnostics). Separate files per step; no shared state file.
- **Targets:** every generated secret (database passwords, OIDC client secrets, the restic
  password, bridge credentials) is created once where it is used. On re-run the installer reads
  the existing secret instead of generating a new one.
- **Recovery:** each app's secrets are sealed into its KyRecovery capsule.
- **Handover:** printed once at the end; not written to the state directory.
- A lost workstation leaks no secret.

## Install sequence

Nothing changes on a target until `plan` and `preflight` pass. `apply` then runs:

1. **Edge and proxy:** existing or new NPM, certificates (acme-dns or Cloudflare), frp on the VPS
   or cloudflared. Hostnames must exist before OIDC redirect URLs are registered.
2. **KyIdentity:** deploy, `bootstrap-admin`, `apply-setup` for identities, groups and roles. The
   owner enrolls MFA at first sign-in through activation links in the handover.
3. **KyRecovery:** deploy; **human gate: custodian ceremony.** Steps needing the pinned key wait;
   independent steps continue; waiting steps resume once the key is pinned.
4. **kyPulse, then KyYard.**
5. **Each remaining app, in dependency order:** OIDC client and roles in KyIdentity → secrets on
   target → deploy → health → `apply-setup` or adapter `Apply` → `GrantAdmin` for the admin
   identity, and a check that the everyday identity has none → NPM host → kyPulse target →
   backup schedule (K8up or restic timer) → KyRecovery pairing.
6. **Offboarding bridge:** deploy; register its SCIM connector in KyIdentity.
7. **KyYard enrollment** of every target.
8. **`verify`, then `handover`.**

## Failure handling

- **Every step is Inspect → Apply → Verify.** Inspect reads real state; Apply creates only what is
  missing; Verify proves the result.
- **Stop at the first failure**, showing the failing command or request and redacted output. A
  re-run skips steps whose input hash and verified result are unchanged.
- **A timeout is not a failure.** Inspect real state before retrying. Bounded retries with backoff
  only for transient errors (image pulls, 429, 503).
- **No automatic rollback of data.** A failed workload is left for inspection.
- **One writer per target.** A lock on the target (a Lease in the cluster, a lock file on the
  host) held for `apply`, `upgrade` and `uninstall`, by the CLI or KyYard. A stale lock is
  reported with its holder and age; it is never broken automatically.

## Upgrades

The catalog and a gated upgrade procedure form a shared Go module, imported by `kyquickstart`
and by KyYard.

- **Start points:** `kyquickstart upgrade`, and an **Upgrade** action in KyYard showing the
  installed release set against the newest one, for one app or the whole suite.
- **Procedure:** check each app's upgrade rules → take a KyRecovery capsule and a restic snapshot
  and verify both (digest match, `restic check`) → upgrade one app at a time in dependency order
  → health and acceptance checks → stop at the first failure.
- **No image rollback after a data migration.** Recovery is the product's restore workflow from
  the pre-upgrade backup. Apps that declare no migration may return to previous digests.
- **KyYard's generic image edits and update policies refuse labelled workloads** and point to the
  Upgrade action. Unlabelled workloads behave as today.
- **Upstream tracking:** CI watches upstream releases for every catalog app. Security releases
  get a fast-tracked release set. Nothing ships as `latest`.

## Verification

`verify` proves, per app:

- sign-in through KyIdentity as the admin and the everyday identity;
- admin granted to the admin identity and refused to the everyday identity, in the UI and API;
- offboarding: disabling a test user ends that user's sessions and tokens;
- health reported in kyPulse;
- a backup reaches KyRecovery and the restic repository, and a disposable restore drill passes.

Running pods alone is not completion.

## Testing and CI

- **Unit:** plan graph, manifest schema, release-set diff, secret redaction, as pure functions.
- **Golden:** rendered Compose for every app and Kubernetes objects for every Ky product.
- **Adapter integration, per app:** real app container against a test KyIdentity: OIDC sign-in,
  admin versus everyday role, `Deprovision`, then prove old sessions and tokens fail.
- **End to end:** disposable kind cluster plus an SSH-reachable Docker host: full `apply`, a
  second `apply` that changes nothing, `apply` killed mid-run then resumed, `verify`, `upgrade`
  across two release sets, `uninstall` keeping data, restore drill.
- **Security checks:** no secret in the state directory; host secret files mode 0600; bridge
  network policy denies everything off its allowlist; NPM admin unreachable from the edge.

## Work in other repositories

| Repository | Work |
|---|---|
| Each Ky product | `apply-setup --file` command with the contract above |
| KyIdentity-server | Bundle covering identities, groups, OIDC clients, roles, access, assignments, SCIM connector |
| KyYard-Server | Import the shared module; Upgrade action; refuse generic edits and update policies on labelled workloads |
| kyPulse-server | Accept target and log-source registration through `apply-setup` |
| Holm (upstream or fork) | Back-channel logout or session recheck (catalog spec) |
| Every Ky product behind an edge | Forwarded-header self-check reporting the client address and scheme it resolved (as KyPost `/api/status`) |
| KyVault-server, kynotes-server | Trust `X-Forwarded-Proto` only from trusted proxies; kynotes defaults to all RFC1918 ranges |
| kyrecovery-server | Trusted-proxy support for its rate limiter (all clients share the proxy address today) |
| kydns-server | Secure admin cookie behind a TLS proxy |

## Unverified

- KyIdentity's generic SCIM sends `active: false` on every path that ends access: manual disable,
  account end date, and losing app assignment.
- `kyrecovery pair generate` works unattended inside the container for a named service.
- KyYard's agent can honor a target-side lock and labelled-workload refusal without broader RBAC.
- `client-go` lock behavior with a Lease held across a long `apply`.

## Out of scope

- Installing Docker or Kubernetes.
- Natural-language prompt front end (layered on `stack.yaml` later).
- High availability; a single-instance deployment is not described as HA.
