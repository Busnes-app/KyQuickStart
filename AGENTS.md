# Engineering principles

**Boundary discipline.** Validate, narrow types, and handle errors at system boundaries — CLI
args, config files, network and wire formats, external APIs. Inside the boundary, trust the
types: no redundant nil checks deep in call chains. Business logic lives in pure functions the
transport layer just calls. Do not re-export transport, storage, or framework types through a
public surface. Test: "is this data crossing a boundary right now?" If no, the validation is
redundant.

**Separate before serializing shared state.** When two concurrent actors might write the same
file, branch, key, or object, first eliminate the sharing — give each its own owned file or
key and merge at the read boundary. Two workers writing their own field into one `state.json`
is still shared mutation; `indexer-state.json` + `metrics-state.json` is not. Reach for a lock,
sequential phase, or single-writer actor only when one shared write target is a real invariant.
Treat "we need a lock" as a smell to check, not the default answer.

# DOX framework

- DOX is highly performant AGENTS.md hierarchy installed here
- Agent must follow DOX instructions across any edits

## Core Contract

- AGENTS.md files are binding work contracts for their subtrees
- Work products, source materials, instructions, records, assets, and durable docs must stay understandable from the nearest applicable AGENTS.md plus every parent AGENTS.md above it

## Read Before Editing

1. Read the root AGENTS.md
2. Identify every file or folder you expect to touch
3. Walk from the repository root to each target path
4. Read every AGENTS.md found along each route
5. If a parent AGENTS.md lists a child AGENTS.md whose scope contains the path, read that child and continue from there
6. Use the nearest AGENTS.md as the local contract and parent docs for repo-wide rules
7. If docs conflict, the closer doc controls local work details, but no child doc may weaken DOX

Do not rely on memory. Re-read the applicable DOX chain in the current session before editing.

## Update After Editing

Every meaningful change requires a DOX pass before the task is done.

Update the closest owning AGENTS.md when a change affects:

- purpose, scope, ownership, or responsibilities
- durable structure, contracts, workflows, or operating rules
- required inputs, outputs, permissions, constraints, side effects, or artifacts
- user preferences about behavior, communication, process, organization, or quality
- AGENTS.md creation, deletion, move, rename, or index contents

Update parent docs when parent-level structure, ownership, workflow, or child index changes. Update child docs when parent changes alter local rules. Remove stale or contradictory text immediately. Small edits that do not change behavior or contracts may leave docs unchanged, but the DOX pass still must happen.

## Hierarchy

- Root AGENTS.md is the DOX rail: project-wide instructions, global preferences, durable workflow rules, and the top-level Child DOX Index
- Child AGENTS.md files own domain-specific instructions and their own Child DOX Index
- Each parent explains what its direct children cover and what stays owned by the parent
- The closer a doc is to the work, the more specific and practical it must be

## Child Doc Shape

- Create a child AGENTS.md when a folder becomes a durable boundary with its own purpose, rules, responsibilities, workflow, materials, or quality standards
- Work Guidance must reflect the current standards of the project or user instructions; if there are no specific standards or instructions yet, leave it empty
- Verification must reflect an existing check; if no verification framework exists yet, leave it empty and update it when one exists

Default section order:
- Purpose
- Ownership
- Local Contracts
- Work Guidance
- Verification
- Child DOX Index

## Style

- Keep docs concise, current, and operational
- Document stable contracts, not diary entries
- Put broad rules in parent docs and concrete details in child docs
- Prefer direct bullets with explicit names
- Do not duplicate rules across many files unless each scope needs a local version
- Delete stale notes instead of explaining history
- Trim obvious statements, repeated rules, misplaced detail, and warnings for risks that no longer exist

## Closeout

1. Re-check changed paths against the DOX chain
2. Update nearest owning docs and any affected parents or children
3. Refresh every affected Child DOX Index
4. Remove stale or contradictory text
5. Run existing verification when relevant
6. Report any docs intentionally left unchanged and why

## KyRecovery integration

Every product in the suite backs up to `kyrecovery-server`, a blind store: it keeps sealed
`kycap/3` containers it cannot open, pins the suite recovery public key, and hands that key
to each product at pairing. The wire contract lives in
`kyrecovery-server/zero_code_pairing_handoff_spec.md` (v2.0.0). Older copies of that file in
product repos describe a retired plaintext push; delete them, do not implement against them.
The product-side implementation is moving into `ky-primitives/recoveryclient` (decision 14,
2026-09-04): client, key pin, sealed pairing record, local copies, schedule, unified run,
drill runner, decrypt-guard test helper. It landed as ky-primitives v0.5.0; `kysignon-server/internal/backup` is the reference
adapter (payload, drill checks, store/key/config glue over the lib). Do not copy lib code. Products keep config wiring, what to seal,
handlers, UI and docs.

### Sequence

1. **Ceremony, once per suite.** A kyrecovery admin opens `/admin/ceremony`, picks k of n,
   prints the n custodian cards, and the page posts only the public key. Until this has run,
   every pairing claim is refused with 409 and the code is not consumed.
2. **Pairing code.** A kyrecovery admin runs `POST /api/pairing/generate` (dashboard) or
   `kyrecovery pair generate --service <name>` and passes the six-digit code to the product
   admin out of band. TTL 15 minutes, 60 at most, single use.
3. **Claim.** The product posts `{pairing_code, service_name, app_name}` to
   `POST /api/pairing/claim` and receives `api_token`, `recovery_public_key` (standard
   base64, 1216 bytes), `threshold` and `total_shares`. Send `service_name` explicitly. The
   value sent is what kyrecovery pins for that token and checks every deposit against;
   omitting it pins `generic` and every later deposit is refused with 403.
4. **Pin.** Persist the token, the public key and the topology write-once. A second pairing
   to a different key must fail, not overwrite. `recoverykey.ParsePublicKey` on the decoded
   key gives the value `capsule.Seal` takes. A claim response without a key is a failed
   pairing.
5. **Seal.** `capsule.Seal(serviceName, appVersion, files, deps, recipe, threshold,
   totalShares, publicKey)` from `ky-primitives/capsule`. `serviceName` must equal the
   claimed `service_name` byte for byte and match `[A-Za-z0-9][A-Za-z0-9_.-]{0,63}`. Files
   must be non-empty; per-file, expanded and container caps are the library's exported
   constants. Include every secret a restore needs, such as the database encryption key:
   only k custodians together can open the container, which is what the key is for.
6. **Deposit.** `POST /api/backup/deposit` with `Authorization: Bearer <api_token>` and
   `Content-Type: application/octet-stream`, body the container. 201 returns
   `{capsule_id, digest, size_bytes, deposited_at}`; 200 is an idempotent re-send of the same
   bytes and also success. Compare `digest` with your own SHA-256 of the container before
   treating the deposit as durable, and keep the receipt: a restore compares `capsule_id`
   and `created_at` against kyrecovery's record, which is where freshness comes from. Budget:
   60 deposits per token per 15 minutes, 4 in flight server-wide, 384 MiB per container.
   Retry 429 and 503 with backoff.
7. **Schedule it, and let the admin set it.** Backups run on a timer, not only when an
   operator clicks. The interval is an admin setting in the product's own UI (off, or at
   least 15 minutes), with the product's env var as the default; the loop polls the setting
   so a change needs no restart. Count the next run from the last attempt, successful or
   not, so a dead destination is retried once per interval, not every tick. Show the next
   run time and the last result on the screen.
7b. **Somewhere to go without kyrecovery.** A product must also accept the suite public key
   pasted by hand (the ceremony page shows it; k-of-n typed beside it), write-once like
   pairing, and must offer a local backup directory (`<PRODUCT>_BACKUP_DIR`, keep newest N)
   so an instance with no kyrecovery still has backups. One `RunBackup` seals once and
   delivers to every configured destination. A pinned key with no destination is a
   precondition failure the screen explains, not a silent no-op. `kysignon-server`
   `internal/backup` (`schedule.go`, `local.go`, `RunBackup`) is the reference.
8. **Restore is the product's.** Download the `.kycap` with a kyrecovery operator session,
   run the product's `restore` command, and type k custodian shares from their cards on
   stdin. Never accept shares in argv. Check the unverified manifest's service name before
   combining shares. `capsule.Open` proves integrity and binding to the pinned key.

### Invariants

- Unpairing is two steps by two admins. The product offers an Unpair action (step-up,
  audited) that deletes its URL and sealed token and nothing else: the key pin stays, so a
  later pairing is accepted only to the same key; receipts and the local directory stay.
  The kyrecovery admin revokes the product's token in the dashboard (`POST
  /api/pairing/revoke`); the product token cannot reach that route. Each side's UI names
  the other step.

- The product token reaches exactly one route, `/api/backup/deposit`. It cannot list,
  download or verify capsules.
- No product ever holds the recovery private key, a seed or a share. Restore drills seal to
  a throwaway key generated and discarded inside the drill.
- kyrecovery URLs must be HTTPS; the client refuses redirects and, by default, private,
  loopback and link-local targets. TLS is not for the capsule: it protects the public key
  that arrives at pairing (trust on first use), the token and the receipts. A product must
  offer an explicit opt-in (`<PRODUCT>_BACKUP_ALLOW_PRIVATE_RECOVERY`, off by default,
  recorded on the pairing audit row) for a kyrecovery on the operator's own network behind
  a TLS proxy; loopback stays refused and HTTP is never accepted. Docs must tell the
  operator to pin the key by hand or compare fingerprints, which defeats a swapped key
  regardless of the wire. Compose files expose a `<PRODUCT>_DNS` so a container can resolve
  names that exist only on the LAN.
- Audit every pairing and deposit locally, success and failure, with the key ID or capsule
  ID and digest. Never log the token.

### Per-product status

`kysignon-server` is the reference. `ky_server_base` #22 includes the recoveryclient
adapter, pin by hand, sealed local copies, UI schedule, unpair, DNS override and restore
runbook. Check each product's current code and shared handoff before porting; older status
snapshots are not implementation blockers. Products wire `ky-primitives/recoveryclient`
and retain their own identity, sealer label and collection adapters.

## User Preferences

- Deployment goal: an end-user stack installer guides requirements and connections to every target server, installs the selected suite software, and builds its initial configuration. The primary path is `KyQuickStart-AI` (owner, 2026-10-22): a local AI agent plans, converses and diagnoses, and `KyQuickStart`, a Go binary run from the operator workstation, is its deterministic execution layer, enforcing validated operations, ownership, credentials and verifiable results; it also runs unattended from the command line. Docker hosts over SSH (Compose) and Kubernetes clusters are both fully supported, bring-your-own; it never installs Docker or Kubernetes. KyQuickStart expansion is frozen: no new catalog apps or standalone orchestration features, only compatibility, safety fixes, verification and the AI integration (`KyQuickStart/docs/redirect.md`). When designing suite installation, read `SINGLE_PROMPT_STACK_RESEARCH.md` for the assessed gaps and proposed sequence; recommendations there are not selected implementation contracts.

- Installer product selection: list available products with a short purpose and an explicit install/skip choice for each. Explain required dependencies and validate the selected combination before deployment; show every required component in the installation plan. Save selections for resume and configure and verify the selected products. Skipping an already installed product does not authorize uninstalling it or deleting its data.

- Installer ownership and handover: let the installation owner choose a dedicated KyIdentity administrator login and give that identity verified administrator access across every installed product. Provision a separate everyday identity for ordinary suite use. Fix inconsistent acceptance of KyIdentity administrators across products; successful SSO alone is insufficient. Provide a printable record of installed product URLs and administrator logins/credentials for the owner to save securely. Administrator access does not change product-specific content confidentiality or recovery-custody contracts. The grant mechanism remains unselected.

- Administrator separation: application administrator identities are dedicated to administration and recovery; everyday identities have no application administrator grants. Enforce this separation in identity assignments and product authorization, including API access, rather than relying on labels or operator discipline. Dedicated administrator identities cannot use ordinary mail, calendar, chat, personal drive, notes or vault workflows. Administrative management of those services remains available within the product's confidentiality contract. Existing mixed-use accounts require a migration that preserves their content and verified owner access.

- Suite components must be open source, including required editor and collaboration features; proprietary editions and required proprietary extensions are excluded.

- Document management must integrate with centralized suite user management through a single administrative interface. Evaluate account lifecycle, groups, document access and offboarding; SSO alone does not satisfy this requirement. SCIM is a possible integration mechanism, not a selected implementation contract.

- Document architecture placement: Euro-Office may run in production on Docker or Kubernetes; the NAS hosts the document-management/storage container and persistent document data. Connect them through authenticated HTTPS file-transfer and save endpoints.

- The installation owner must administer KyDrive through their KyIdentity account. Explicit application administrator grants are separate from directory provisioning; an undisclosed local recovery login is not sufficient owner access.

- KyDrive provides both a private personal workspace per account and group-owned shared workspaces. Put workspace destinations directly in the main sidebar. Users can create blank documents, spreadsheets and presentations in the selected workspace and open them in Euro-Office.

- The NAS service is a general drive backend for suite applications, with Euro-Office as its first integration. Shared files belong to organization workspaces and survive employee offboarding. Applications use authenticated service APIs for file access. Initial scope: upload/download, folders, group-owned workspaces, permissions, versions, trash and quotas. Support the same API/data model on NAS storage or a Kubernetes persistent volume; desktop sync, WebDAV, SMB access and advanced search are deferred.

- Internal infrastructure, including NAS and Kubernetes hosts, is x86-64. Target `linux/amd64` for the initial document-stack containers.

- For drive implementation or deployment work, read `DRIVE_IMPLEMENTATION_PLAN.md` for the agreed scope, proposed phases and acceptance gates; unresolved implementation choices remain proposals.

- KyDrive people and groups are managed in KyIdentity; group permissions belong to KyDrive. Initial development is build-and-test local: NAS target: the pilot's Unraid host / KyYard `hluswcdata01`; recovery target: the pilot's KyRecovery instance (pilot hostnames are kept out of the repositories). The user authorized the live deployment pilot on 2026-10-04. Internal HTTPS origins are the pilot's `kydrive` and `office` hosts; `office` replaces the proposed euro-office hostname. Independent bulk backups were deferred for the KyDrive pilot; the suite installer's bulk path is restic to an append-only rest-server beside KyRecovery, off the NAS; restores run on the operator workstation and nothing decrypts inside KyRecovery (`KyQuickStart` catalog spec). NAS-local backup copies must not be described as independent. Preserve existing suite services and credentials; verify each pilot gate before declaring readiness.

- Nextcloud is excluded from document management and file storage. ONLYOFFICE products are excluded for geopolitical reasons. Evaluate Euro-Office's independent governance, build and release chain rather than rejecting it solely for inherited code; it is selected for the KyDrive local integration; production release requires source/build provenance verification. CryptPad's ONLYOFFICE-derived browser editors must remain explicit in comparisons.

- KyPasswords is replaced by Vaultwarden (decided 2026-10-06). The installer offered Vaultwarden only (confirmed 2026-10-09) until the owner dropped it, with Forgejo and BookStack, from installation on 2026-10-22; existing installations stay untouched. Exclude the older KeePass-based KyVault from new installs and adapters. Existing KyVault installations and data stay untouched. KyAuth does not become a Bitwarden client and becomes authenticator-only (TOTP, Push MFA, KyIdentity sign-on and passkey); Android users use the official Bitwarden app against Vaultwarden for passwords and passkeys. See `kyauth-android/docs/superpowers/specs/2026-10-06-vaultwarden-replaces-kypasswords.md`.

- Third-party apps installed with the suite must pass the admission rules in `KyQuickStart/docs/superpowers/specs/2026-10-06-third-party-catalog-design.md` (approved 2026-10-06): open source with SSO in the free edition, native OIDC against KyIdentity, admin from a claim or an unattended grant, and an offboarding path. No auth-proxy gate is built. Monitoring stays with kyPulse; forms, video calls and video streaming are out of scope.

- KyNotes is retained: no off-the-shelf OneNote replacement passes the admission rules. It gains OneNote-style notebooks (notebooks, sections and pages, freeform canvas pages, ink) as its own sub-project.

- Product branding uses the Busnes.app-site Systems stamp icon masters and platform-sized local exports. Busnes Light/Dark reached web and `kypost-android`, whose default is now Busnes Light; the remaining native clients keep their existing defaults until their own pass.

- Web themes default to the Busnes.app cream/light and charcoal/dark palettes with orange accents, following the OS until a browser-local choice is saved. Preserve existing named themes and saved choices.

- KyQuickStart places every catalog Ky product on Docker hosts, including a separate resident runner for KyYard, and on Kubernetes where the product has a manifest. Products keep their documented placement restrictions: KyDrive, KyPost, KyDNS and KyMessages are Docker only (`KyQuickStart/docs/redirect.md`). Euro-Office (`euro-office`, KyDrive's optional editor) is Kubernetes only.

- KyPost owns the shared `kypost-net` Docker network; standalone consumers join it as external. Containerized Nginx Proxy Manager is supported with a fixed address trusted as one `/32` (IPv4) or `/128` (IPv6). Preserve existing manually owned networks as explicit legacy exceptions.

When the user requests a durable behavior change, record it here or in the relevant child AGENTS.md

# KyQuickStart

Everything above this heading is the shared suite contract, copied from the busnes.app workspace
root so this repository stands alone. Keep it in step with the workspace copy.

## Purpose

The deterministic execution layer for KyQuickStart-AI, the primary installation path, and an
unattended command-line tool. It plans, checks, installs, preconfigures and verifies the Ky
catalog on Docker hosts over SSH (Compose) and Kubernetes clusters, both fully supported, and
prints the owner's handover. The AI plans, converses and diagnoses; `kyquickstart` enforces
validated operations, ownership, credentials and verifiable results. Decision, dispositions and
edge compatibility: `docs/redirect.md`.

## Ownership

- Public overview, build and command summary: `README.md`.
- Third-party catalog, admission rules, edge and reverse-proxy choices, and the app-aware bulk
  backup path: `docs/superpowers/specs/2026-10-06-third-party-catalog-design.md`.
- Redirect record (decision, freeze, Keep/Simplify/Retire/Unfinished dispositions, placement,
  per-product edge compatibility): `docs/redirect.md`.
- Installer architecture (CLI, catalog format, targets, state, setup, upgrades, testing): `docs/superpowers/specs/2026-10-06-installer-architecture-design.md`.
- Current edge configuration and supported deployment paths: `docs/edge.md`.
- Guided planning, demo deployment, existing installation references and explicit KyIdentity setup: `docs/guided-setup.md`.
- Suite runner (KyYard manages the suite after install): ownership, hand-off, runner protocol v1,
  HTTP status table and image: `docs/superpowers/specs/2026-10-16-suite-runner-design.md`;
  operator steps in `docs/guided-setup.md` ("Hand-off to KyYard"); compose example
  `docs/kyyard-runner.compose.yaml`, pod manifest `docs/kyyard-runner.k8s.yaml`; image `Dockerfile`, published by
  `.github/workflows/release-image.yml`. That workflow runs `publish` (image, attested and
  verified), `binaries` (`scripts/release-binaries.sh`, the only home of the release `-ldflags`,
  stamps `main.runnerImage` with the verified digest; the four binaries are attested, uploaded
  and re-verified after download) and `promote` (`:stable`), each with only the permissions it uses.
- The release set has one source: `internal/appversion/VERSION` (`X.Y.Z`, the release line). Go
  code calls `appversion.String()`; release builds (the image's `VERSION` build arg and
  `scripts/release-binaries.sh`) set the tag's full version through
  `-ldflags -X …/internal/appversion.release`, so releases report `X.Y.Z` or `X.Y.Z.N` and source
  builds report the line.
- Publishing a GitHub release `vX.Y.Z` or `vX.Y.Z.N` runs `release-image.yml` (a dispatch on a
  published release's tag runs it too). It refuses a malformed tag, a tag whose `X.Y.Z` disagrees
  with `internal/appversion/VERSION`, and any commit on which a ci.yml job in its `REQUIRED` list
  did not pass; attestations are pinned to `release-image.yml@refs/tags/<tag>`, and `:stable` moves
  only to the highest non-prerelease, four-part ones included.
- `dependency-release.yml` runs daily at 23:00 UTC: if Dependabot commits landed on `master` since
  the newest release on the current line and the push CI run on `master`'s tip succeeded, it
  publishes the next `vX.Y.Z.N` (one per day however many PRs merged) and dispatches
  `release-image.yml` on it, since a `GITHUB_TOKEN` release raises no release event. Run by hand,
  it releases `master` without the Dependabot condition. It waits while the line in `VERSION` has
  no `vX.Y.Z` release: bump `VERSION` and publish `vX.Y.Z` by hand for a new line.

## Local Contracts

- `plan` supports an explicitly selected disposable demo catalog while product setup gates remain
  closed. Saved existing-product references do not authorize adoption or imply completed integration.
- `stack.yaml`'s `integrations` selects product-owned setup independently of `existing`
  references. Each selection names a declared product/target, one container (namespace/pod
  on Kubernetes) and a protected container-local bundle path, clean/absolute and free of
  control characters. Validate the complete batch before connecting. No inline credentials are
  accepted. Optional typed `notes` settings prepare KyNotes' secret-free native bundle
  through `apply-setup --stage` stdin, restricted to its private `<data>/installer/setup.json`.
  Inspect the retained bundle before configuring; never replace changed or unsafe input.
  KyNotes credential references still require protected files on the target. Typed `identity`
  metadata prepares KyIdentity's private `<data>/installer/<setup UUID>/setup.json` and
  target-held credentials through its native staging interface. Immutable intent and
  credential receipts refuse changes/loss; no credential enters local state. Destination
  SCIM tokens remain existing protected target references. `integrate-stack` runs
  these selections explicitly; `apply` retains deployment scope and does not configure
  recorded existing products. An entry for an installed catalog app names no workload:
  `apply` runs it after the app's health step against the app's one ready pod, and
  `integrate-stack` skips it. Today only KyIdentity (from its `identity` settings) and the
  installed Ky apps with a catalog `oidc` block and a product adapter: KyRecovery, kyPulse,
  KyNotes, KyCalendar and KyYard on Docker or Kubernetes; KyDrive, KyPost and KyMessages (its
  whole chat stack, with a second, everyday-only `chat_client` for MAS) on Docker, and KyDNS on Docker (its managed `app` service, through `docker exec` over SSH; KyDNS's shell-less image gets its setup files placed from the host). An installed KyNotes' entry is exactly `bundle: /data/installer/setup.json`
  and its `identity_client` (no `notes` block): apply derives its native preparation, and its
  setup runs after the edge routes KyIdentity and before KyNotes' own route; a routed KyNotes
  without that entry is refused. Apply adds each such app's client, derived from its `oidc`
  block and route, to KyIdentity's first setup intent. When the running KyIdentity's setup ran without it and the
  KyIdentity advertises `setup-amendments` (0.4.3+, KyIdentity-server#110), apply records the
  app's `identity_amendment` (UUID derived from parent setup and client) in the plan and
  `kyidentity.amend.<app>` applies that amendment; otherwise it names #110 and the client.
  `kyidentity.retire-bootstrap` disables `kyq-bootstrap` through `disable-user` when advertised,
  else apply prints that it stays active. An app whose `oidc` names `scim_token_file` (KyDrive)
  gets `kyidentity.scim.<app>`, after its setup and the edges: its target's token copied
  privately into KyIdentity's container, never replaced, and a pass of the setup that registered its
  client with `scim` added (outside the immutable preparation directories, so no reset).
  The catalog KyIdentity sets `KYIDENTITY_ALLOW_PRIVATE_CALLBACKS`, since routes resolve to
  LAN or pod addresses; loopback, link-local and metadata stay refused. Before any step, apply refuses
  (`issuer_changed`) a plan that moves the issuer KyIdentity's workload runs with, or the URLs
  of an added client its setup exported, once that setup has run (read-only
  `--check`/`--export-client` on the running pod; unreadable refuses). Each app's setup
  transfers its client's secrets in memory, across targets too, into private files in the app's
  pod or container, never replacing one.
  KyCalendar's bundle also names the owner's everyday account, its subject from KyIdentity's
  read-only check of its first setup; a plan whose owner usernames KyCalendar would refuse is
  refused before connecting.
  KyPost's bundle is its own native version-1 JSON (KyIdentity sign-in with `autoProvision`, the
  client secret in a file beside it), placed and applied as `kypost`; it has no directory, SCIM or
  generated secrets, and its container runs as `kypost` with no capabilities.
  KyRecovery's custodian ceremony stays human and apply prints it as outstanding. kyPulse's
  bundle registers every other routed app's readiness path as a monitoring target; that bundle
  is replaced as targets change, since kyPulse keeps omitted targets and refuses edited ones.
- Command-line only: no GUI, web UI or full-screen terminal UI. `plan` asks its questions as
  plain terminal prompts, and every command also runs unattended from `stack.yaml`.
- A Notes integration's explicit `identity_client` selects one declared prepared KyIdentity
  client for private credential transfer. Validate the graph before connecting; export only
  through native authority, resolve the dedicated owner subject, stage metadata and import
  credentials in memory, inspect destination and recheck source before ordinary setup.
  Saved plans hold no credential values or guessed owner subjects. Disposable SSH and kind
  acceptance verify native TLS configuration, owner binding and transfer. SSH additionally
  qualifies native HTTP sign-on and owner/everyday Notes access; browser UI, MFA, Kubernetes
  sign-on and full lifecycle acceptance remain open.
- The catalog takes no new apps (freeze, `docs/redirect.md`). Owner amendments, 2026-10-22:
  `euro-office`, KyDrive's optional editor on Kubernetes, and the `cloudflare-direct` edge. The catalog spec's admission rules
  stay for KyQuickStart-AI's labelled third-party test installs; record each rejection there.
- Third-party apps run on Docker hosts only. Kubernetes hosts Ky products and the edge
  components (cloudflared, Nginx Proxy Manager, frp client), deployed with `client-go` typed
  objects (no Helm); cloudflared and the frp client also deploy to Docker hosts, NPM does not. An app leaving the catalog stops taking new installs after
  notice, and existing installs are handed to their owner with `unmanage`.
- Every installed app signs in through KyIdentity with native OIDC, uses *Assigned users only*,
  and offboards through KyIdentity's SCIM or signed directory webhook into the product's own
  handling. The installer builds no auth-proxy gate and never adopts a foreign account.
- Reverse proxy is Nginx Proxy Manager, except a `cloudflare-direct` edge (Docker only): cloudflared
  routes each listed hostname straight to its app on a dedicated Docker network. Only KyIdentity,
  KyCalendar, KyPost and KyDrive are eligible; Kubernetes is refused (`docs/edge.md`). On an existing NPM, touch only installer-created hosts,
  certificates and certificate users, through its API, never its database. LAN DNS records in
  KyDNS are touched only while listed in the installer's ledger (`docs/edge.md`). NPM admin is LAN-only; its credentials go in the handover.
  A host is this installation's only with `stack.yaml`'s `installation_id` in its marks; hosts
  with the older target-only marks change only under `apply --adopt-edge-hosts`, once. On
  Kubernetes the catalog's `npm` app keeps its admin API on a ClusterIP Service reached through
  the driver's port-forward (`kube://`); bootstrap replaces NPM's first or default administrator
  with a cluster-generated one (`docs/edge.md`).
- One edge (NPM and a tunnel or frp client, or a `cloudflare-direct` cloudflared) per target and one owner per hostname. A route's
  app is on the edge's target or on a Docker target, never in another cluster; a cluster edge's
  egress admits such an upstream's `forward_host` (an IP) and port alone. A product's
  trusted-proxy value is unset or loopback only (forwarded headers ignored), or exactly where its edge
  connects from (`{{edge_address}}`: `egress_address`, or a `cloudflare-direct` edge's
  `connector_cidr`), never `0.0.0.0/0` or a Docker gateway; `verify-edge` checks
  the client address the product reports. Per-product status: `docs/redirect.md`.
- Each route is internal (`allow_cidrs` clients only) or external (the public connection);
  unmarked takes its edge's (`lan` internal, tunnel external). A tunnel edge may carry both, never
  only internal ones, and its connector never carries or reaches an internal route. Catalog
  `exposure: internal` apps (KyRecovery, kyPulse, KyDNS, KyYard) are never external, and an
  external sign-in app needs an external KyIdentity route. Optional plan `domains` keeps internal
  names under `internal` and external names under `external` and outside it (`docs/edge.md`).
- Bulk data goes to restic on an operator-run, append-only (`--append-only --private-repos`)
  TLS rest-server off the app's host, one repository (`<repository>/<app>`) and password per
  app. `kyquickstart backup APP` (Docker apps only, no schedule, no prune) snapshots the app's
  dump or, without a dump command, its stopped bind directories, plus its `capsule_secrets`,
  tagged with app and installation. The password (`restic_password`) is generated on the app's
  target, never leaves it through the installer and is never in a snapshot; the owner records
  it once with the printed `ssh … cat` command. Sealing it in KyRecovery and K8up on
  Kubernetes come later; the rest-server host never holds one.
- Restores run in `kyquickstart restore`, never inside KyRecovery: typed confirmation, refusal
  of a running app and, without `--replace` (which moves data aside, never deletes), of any
  existing data. An app whose people come from KyIdentity restores only when the plan's
  KyIdentity is v0.4.4 or later, replays removals with `resync-system` as soon as the app is
  healthy (or after its `backup.replay_after` service, before the later `restore_order`
  services start) and keeps the app stopped until that is delivered (`restore_incomplete` otherwise), so
  a removed person is readmitted only for the measured seconds between the app's start and the
  replay; until that replay is delivered (`.restore-pending`), every command but `restore` that
  touches the app (`apply`, `install`, `upgrade`, `backup`, `unmanage`) refuses.
  A snapshot's symlinked volume, secret, dump or any directory above them is refused.
- Secrets are generated once on their targets and read back on re-run; the workstation state
  directory never holds a secret.
  Docker generation uses the target's `/dev/urandom` and private temporary files, with
  complete-read checks and no-clobber publication; preflight checks its required tools.
  Readback rejects unsafe ownership/paths, symlinks, non-0600 files and malformed base64url.
  Kubernetes generates missing keys through exec in a uniquely named, digest-pinned,
  restricted temporary pod without service-account credentials. Remove it by UID before
  publishing; preserve existing keys (including invalid/empty ones, which inspection refuses).
  `docs/guided-setup.md` owns its image provenance, resource bounds and required permissions.
- The installer's only authority inside a Ky product is its container-local `apply-setup`
  command. Installer-managed workloads carry `ky.managed-by=kyquickstart`; only the shared upgrade
  module changes them, from the CLI or KyYard, under a target-side lock.
- `stack.yaml`'s `revision` rises on every plan write. Every locked run writes a plan marker on
  each target it locked (`<root>/.plan`; annotations on the `kyquickstart` namespace) with the
  plan's `installation_id`. It refuses a marker naming another installation
  (`owned_by_other_plan`; `apply --take-over-target T` claims T unless the runner owns it), then
  one of its own installation that is newer (`plan_stale`); it keeps the marker's owner. A
  marker without an id, from an older build, is claimed by the first plan that locks it. Plans are
  published only after every step passed, so each command reruns after interruption.
- Only `handoff` changes a marker's owner. Under `owner: runner` the CLI refuses `plan`,
  `install`, `uninstall` and `unmanage`, and `apply` or `upgrade` that would redeploy the app the
  runner runs in (`owned_by_runner`); the runner acts only on targets marked with its own ID
  (`owned_by_cli` otherwise) and never changes its own pod (`kubernetes.suite_runner`, the
  catalog KyYard): its workload (both Docker containers when on Docker) becomes a note step and requests naming it are refused
  (`runner_host`). The workstation deploys that app only with a digest-pinned runner image
  (`cli.Options.RunnerImage`, stamped into release binaries by `scripts/release-binaries.sh`; empty
  source builds refuse it; `version --runner-image` prints it). The runner's SSH key, kubeconfigs and
  identity stay in its state volume beside its secret-free `plan/`; the workstation never
  receives them. Runner access on targets is exactly the installer's: one `restrict`ed
  authorized_keys line, or a ServiceAccount bound to the preflight permission list.
- Deployments record their catalog version (`kyquickstart/version` in `compose.kyq.yaml` or on
  the app namespace), which outlives uninstall. Catalog versions are `MAJOR.MINOR.PATCH`, or
  `MAJOR.MINOR.PATCH.N` for a Ky product's dependency release (`X.Y.Z` orders as `X.Y.Z.0`). `apply`, `install` and `upgrade` gate a version
  change on the manifest's upgrade rules: `migrates_data` is always `backup_required` (a
  `kyquickstart backup` does not lift it yet); any other upgrade needs an explicit, recorded
  acknowledgement. The manifest's `upgrade.note` is printed and recorded with it.
  Uninstall keeps data and secrets; unmanage drops the managed labels and the installer then
  refuses to install over the app. Error codes are `internal/suite` codes.
- `stop`, `start` and `restart APP` act on one installed app under the target lock (Docker:
  Compose stop/up -d/restart; Kubernetes: replicas 0/1 and a pod-template restart annotation),
  waiting for health. `stop` records `stopped: true` on the app in stack.yaml; while it is set,
  `apply`, `upgrade`, `restart` and `backup` refuse `app_stopped`; `start` and a finished
  `restore` clear it. A workload stopped by hand without it is still repaired by apply. `logs
  APP` is read-only, redacts the secrets the app's catalog entry generates and needs `get pods/log` on
  Kubernetes, which preflight does not require. CLI `status` adds the live workload state; the
  runner's status does not (KyYard decodes it strictly). `handoff` refuses while any app is
  stopped: the runner cannot start one, and older runner builds refuse the field.
- Remote commands are POSIX `sh`, quoted with `remote.Quote`. The SSH runner sends each one
  base64-encoded to `sh` (`remote.wrap`), so a non-POSIX login shell such as fish cannot alter
  it. Unit tests run commands for real through `remote.Local` and under sh, bash and fish.
- Host keys are pinned in `<state>/known_hosts`; a changed key is never accepted, by flag or prompt.
- On a cluster each app lives in namespace `kyq-<app>` (Pod Security `restricted`, default-deny
  ingress) and the run lock is Lease `kyquickstart/kyquickstart-lock`. A manifest's
  `pod_security: baseline` is the only exception (NPM, whose root init it needs, and
  `euro-office`, whose entrypoint starts its bundled services as root; `docs/edge.md` and
  `docs/guided-setup.md` record their measured capabilities). `kubernetes.egress: edge` limits an
  app's egress to cluster DNS and the edge's pods; `euro-office` relies on it for
  `ALLOW_PRIVATE_IP_ADDRESS`. Only edge components publish NodePorts,
  never NPM's port 81, and preflight refuses one held by any Service but that app's own; with an edge on the cluster, apps admit ingress from its pods alone. The installer never adopts
  a namespace or object without the managed label. A kubeconfig is referenced by path and may not
  live inside the state directory, symlinks resolved. One target per cluster; `apply` repairs
  drift in an app namespace's labels and its default-deny policy. A target's optional
  `storage_class` sets every claim's class and must exist at preflight; without it claims need a
  default class. A claim's class never changes: `apply` refuses an existing claim of another class.
  A non-root app mounts each claim through a 0700 `private` subdirectory that an init container
  (the pinned BusyBox helper, the app's uid) creates. `kubernetes.termination_grace_seconds`
  (0-3600, 0 keeps the default) sets the pod's termination grace. A `kubernetes.env` value may contain
  `{{host}}`, replaced by the URL host of the app's one edge route (hostname, plus `:client_port` when an internal route's is not 443); no other template syntax.
- A Compose template may write `{{host}}`, `{{forward}}` (the route's
  `forward_host:forward_port`, an IP of the Docker target, where the app publishes its port),
  `{{forward_addr}}` (that IP alone), `{{hostname}}` (the route's hostname without a client
  port) and `{{edge_address}}` (the route's edge's `egress_address`; a plan whose edge has none
  is refused before connecting). An app whose manifest declares `routes` (KyMessages, Docker only)
  takes one plan route per role, the primary without `role`, all on one `forward_host` (one LAN
  IPv4 of the target: no wildcard, loopback, link-local, multicast, IPv6 or name) and one
  `forward_port` per service port, and writes `{{host "<role>"}}`,
  `{{forward "<role>"}}` and `{{hostname "<role>"}}`; `media` ports publish on the app's
  `media_address` (`{{media_address}}`, one LAN IPv4 of the target, checked at preflight). Every
  `ports:` entry starts with `{{forward…}}`, `{{forward_addr}}`, `{{media_address}}` or
  `127.0.0.1`: no bare or wildcard publication. A service in a Compose profile is a one-shot run
  (`compose run --rm`), never part of `up` or health; a bind source inside a bind directory
  must exist before deploy starts the app.
  Compose runs with `KYQ_UID`/`KYQ_GID` set to the target user's; `./<name>` bind directories
  are created private by the driver before Compose, and no other relative bind source is
  accepted.

## Work Guidance

Expansion is frozen (owner, 2026-10-22; supersedes the all-product adapter and roadmap plans):
no new catalog apps, Ky or third-party, and no new standalone orchestration features. Work is
compatibility, safety fixes, verification and the KyQuickStart-AI integration. Record each
still-wanted capability under Unfinished in `docs/redirect.md` instead of building it here.

Deployment-specific choices (existing or new NPM, domains, Cloudflare or VPS, and target
connections) are installer runtime inputs. Build against disposable local fixtures; those
choices do not block development. Live testing requires authorization for that deployment.

On this workstation, run builds and test suites serially. Use disk-backed scratch under
`/home/yoshi/.cache/kyquickstart-work`; `/tmp` is RAM-backed. Go commands use
`GOMAXPROCS=2`, `GOMEMLIMIT=1GiB`, `GOFLAGS=-p=1` and that scratch directory as `TMPDIR`.
Bound Node heaps and test workers, and give owned fixture containers explicit memory/CPU
limits. Host builds/tests use a transient `systemd-run --user` unit with `MemoryMax=2G`,
`MemorySwapMax=0`, `TasksMax=128` and `CPUQuota=200%`; the child fixture process shares the cap.
The Go heap target is soft; it does not replace the hard cgroup limit or serial execution.
The e2e Docker helper supplies missing owned `run` limits (512 MiB memory, swap equal to
selected memory, one CPU, PID128), preserving explicit fixture overrides. Direct Docker
calls and kind nodes must carry their own explicit bounds.

## Verification

- `make build`: builds the workstation `kyquickstart` binary.
- `make ci`: tidy check, gofmt, vet (including the `e2e,goolm` tags), race tests. Runner tests decode
  every answer with KyYard's client types (`internal/runner/kyyard_test.go`).
  `cmd/kyquickstart/release_test.go` runs `scripts/release-binaries.sh` (the stamp in all four
  binaries, its refusals) and checks `release-image.yml`'s and `dependency-release.yml`'s job
  graph, permissions and pins.
- `make workflow-lint`: needs Docker (CI runs it in `vulncheck`); actionlint v1.7.12 on every
  workflow and offline, digest-pinned zizmor (medium and up) on `release-image.yml` and
  `dependency-release.yml`.
- `go test -tags e2e ./test/e2e -run '^TestDockerKy'`: pinned-image Docker startup/readiness/native capabilities/restart for every new template, plus KyYard's private runner socket, credential isolation and container recreation.
- With `KYQ_TEST_EURO_OFFICE=1`, `TestKubernetesEuroOffice` (own kind cluster, 5 GiB node) installs NPM,
  KyIdentity and `euro-office` with KyDrive on an sshd target: baseline pod and its capabilities,
  seeded claims, the copied secret, `/healthcheck` through the edge, a signed KyDrive callback
  (200/401), egress through the edge only, a skipped rerun and a restart. Not part of `make e2e`.
- `make e2e`: needs Docker (in CI split by `E2E_RUN`/`E2E_SKIP` over four `e2e (<shard>)` jobs, summarised by the `e2e` job that `release-image.yml` requires; a new slow kind test should get a shard or join one); runs `apply` against an sshd container on the local Docker socket and
  against a kind cluster (`go run sigs.k8s.io/kind@v0.33.0`), including drift and stale-lock cases.
  Also runs disposable NPM, frpc/frps and cloudflared container tests in `internal/edge`, and
  `TestKubernetesNPMEdge`: catalog NPM on kind with claims, bootstrap, policy probes, a routed
  request through the NodePort as a cloudflare and then a lan edge, skipped reruns and token
  refresh. It then installs the catalog KyIdentity from its attested image behind the lan edge
  (`client_port` 30443): restricted uid-100 pod on a private data directory, staged
  `apply-setup`, OIDC discovery through the edge naming the `:30443` issuer, changed-intent
  refusal, and a later `client_port` change refused (`issuer_changed`) before
  `kyidentity.deploy`, Deployment and pod unchanged.
  The catalog KyDrive joins it from an sshd Docker target, routed through the same lan edge to
  the kind gateway: hardened container as the target user, published on that address only, its
  client in the same first setup, secrets crossed to its container over SSH, live `--check`,
  health through the edge, KyIdentity's SCIM connector (token copied to KyIdentity's pod, setup
  pass checked) and, later, both owner accounts provisioned by SCIM and signed in through
  KyIdentity, the admin as KyDrive's administrator and the everyday account as a user.
  The catalog KyRecovery joins it: restricted uid-10001 pod, its client in KyIdentity's first
  setup, transferred private setup files, live `--check`, health through the edge, the
  ceremony handover and a skipped rerun. The catalog kyPulse joins too: restricted uid-10001 pod
  with its audit key on its own claim, its client in the same first setup, its targets for
  every other routed app in its live `--check`, and health through the edge. Then, with
  test-only cluster DNS and CA trust, the owner's admin signs in to KyRecovery and kyPulse
  through KyIdentity, the everyday account is refused by both, and kyPulse reports every
  target ok. Last, the catalog KyNotes joins after KyIdentity's first setup through a setup
  amendment: restricted uid-65532 pod, staged/imported private setup files (checked from an
  ephemeral BusyBox container, the image has no shell), live present-only `--check`, no route
  until its setup passed, health through the edge, admin and everyday sign-in with KyNotes' admin
  separation (backup status 200/403, notes 403 `admin_account` for the admin), and a skipped rerun.
  The catalog KyCalendar joins the same way by amendment: restricted uid-10001 pod, private setup
  files and its 0600 break-glass password file, live `--check` naming both owner identities,
  readiness through the edge, the handover, admin and everyday sign-in with its admin separation
  (`/api/admin/users` 200/403, `/api/calendars` 403/200), and a skipped rerun.
  `TestKubernetesKyYard`, in a kind cluster of its own (the runner knows only its build's
  catalog and reaches only what its pod does), installs NPM, KyIdentity and then the catalog
  KyYard by amendment with a runner built from this checkout and pinned by the digest kind's
  containerd records (test plumbing): both containers ready, private setup files and live
  `--check`, the socket shared 0600/0700 as uid 10001, admin sign-in and the everyday account
  refused at KyIdentity's gate, KyYard's `/api/suite/status` read through the socket, the printed break-glass and handoff lines run as printed, an
  upgrade of every app through the socket that leaves KyYard's pod alone, `runner_host` for
  KyYard, the workstation apply refused until revoke, and no credential in output or state.
  The catalog KyDNS joins the Docker target by amendment: hardened container as the target
  user, setup files placed from the host, live `--check`, authoritative NXDOMAIN over UDP and
  TCP on the forward address, its transport gate refusing direct requests with forged headers
  (`kydns.gate`), and a skipped rerun. Where the Docker host keeps client addresses it also
  proves `kydns.edge`, trusted headers through the edge, the closed `/setup` and admin-only
  sign-in; where it hides them (this workstation) it proves KyDNS refuses the edge, that
  `kydns.edge` fails apply and that `kydns.gate` refuses a gateway `egress_address`.
  The catalog KyPost joins the Docker target beside KyDrive by amendment: container as `kypost`,
  read-only, no capabilities, published on the forward address only and listening on no mail
  port, private setup files, live `--check`, health and an untrusted-proxy network status
  through the edge, admin and everyday sign-in (`/api/users` 200/403), the handover's
  break-glass read and session-ending restart run verbatim on the target, and a skipped rerun.
  `TestKubernetesKyMessage` (`message_stack_test.go`, `message_chat_test.go`) installs the whole
  KyMessages stack on a non-root sshd target in the host's network namespace by amendment, with
  KyIdentity's first setup relaying mail through a test-only Mailpit (STARTTLS, AUTH, test CA)
  at `smtp.mail.example.test`: Docker picks its kind address (CI's kind network refuses a fixed
  `--ip`) and the cluster DNS's hosts block names it.
  A test-only Matrix client (`matrix_client_test.go`: mautrix with pure-Go goolm, build tag
  `goolm`, MAS OAuth with PKCE and dynamic registration) signs in by both verified-email paths,
  exchanges encrypted messages, gets a LiveKit grant, measures offboarding (the owner's everyday
  account and a person added later, each bounded at 60 s), proves a person assigned to the chat
  client alone is locked by the sweep, that removing "KyMessages chat" alone leaves an open
  session and removing "KyMessages" too ends it, that a person disabled while KyIdentity's
  create to the console fails cleanly (the setup's directory secret unreadable: a 503, which
  KyIdentity does not fence as uncertain) reaches the console's directory as inactive and
  `resync-system` delivers (this fails on KyIdentity 0.4.5, which dropped that removal), and
  backs the stack up and restores it through
  `backup`/`restore`, the original device decrypting the history and the replay (before Synapse
  starts) ending a session the snapshot still held before it ever reaches Synapse.
  Backups (`test/e2e/backup_test.go`) go to a disposable append-only TLS rest-server on its own
  network, published on the default bridge's gateway. `TestApply` backs up and restores the
  test-only `dumpdemo` (Postgres dump, a file volume with a `skip` path, a capsule secret) from
  its own root; `TestKubernetesNPMEdge` gives the Docker target a destination, asserts KyDrive's
  handover lines, backs KyDrive up cold and proves a restore of the running app is refused.
  `TestKubernetesRestore` runs the KyDrive drill on its own kind cluster: with KyIdentity at the
  attested 0.4.3 release (a catalog copy), restore is refused before any restic call; after
  `upgrade kyidentity` to the catalog's 0.4.7 it proves a disaster restore with the handover's
  password, the refusals, `--replace`, a refused `forget`, and no readmission of a person
  unassigned or deleted after the snapshot.
  Set `KYQ_TEST_IDENTITY_BINARY` to a local static KyIdentity build to additionally test real
  container-local identity setup over SSH and kind, including target-held preparation,
  inspection/resume and changed-intent refusal; SSH additionally refuses lost credentials.
  Its fixture also covers receipt-owned SCIM/group and signed
  suite connectors with create/check/resume; this fixture never uses a live deployment.
  `KYQ_TEST_NOTES_BINARY` additionally qualifies native secret-free bundle preparation,
  inspection/resume and changed-intent preservation over actual SSH and kind transports.
  Set both native binary variables to qualify private Identity-to-Notes transfer in `TestApply`:
  named local TLS issuer, canonical owner, exact credential bytes, no workstation persistence,
  apply/check/resume, changed-file refusal and unrelated-edge exclusion. It also signs in
  installer-created owner/everyday accounts through native password/CSRF and Notes PKCE,
  observes discovery/token/JWKS traffic, verifies reciprocal admin/ordinary access and
  reinspects/resumes after login. Client secrets and bootstrap passwords stay out of reports
  and final workstation state. This is native HTTP protocol acceptance, not browser UI or
  MFA qualification. `TestKubernetesApply` additionally qualifies restricted
  kind transfer with named, CA-verified TLS services, canonical owner/credential binding,
  apply/check/resume, drift preservation and no workstation persistence. Its test-only TLS
  helper uses the static test executable (`CGO_ENABLED=0`, set by `make e2e`); it proves no
  production edge topology or NetworkPolicy enforcement.

## Child DOX Index

- `internal/cli/AGENTS.md`: guided questions, local plan persistence and command orchestration,
  including `backup` and `restore`.
- `internal/runner/AGENTS.md`: `kyquickstart serve` and the serve-* commands inside the runner
  container; the root owns the ownership rule and the CLI owns handoff.
- `internal/edge/AGENTS.md`: NPM reconciliation, connector deployment and public forwarded-header verification.
  The root owns stack schema, CLI orchestration and target drivers.
