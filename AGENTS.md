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

- Deployment goal: an end-user stack installer guides requirements and connections to every target server, installs the selected suite software, and builds its initial configuration. The installer is `KyQuickStart`, a Go binary run from the operator workstation: bring-your-own Docker hosts over SSH with Kubernetes optional; it never installs Docker or Kubernetes (see `KyQuickStart/docs/superpowers/specs/2026-10-06-installer-architecture-design.md`). When designing suite installation, read `SINGLE_PROMPT_STACK_RESEARCH.md` for the assessed gaps and proposed sequence; recommendations there are not selected implementation contracts.

- Installer product selection: list available products with a short purpose and an explicit install/skip choice for each. Explain required dependencies and validate the selected combination before deployment; show every required component in the installation plan. Save selections for resume and configure and verify the selected products. Skipping an already installed product does not authorize uninstalling it or deleting its data.

- Installer ownership and handover: let the installation owner choose a dedicated KyIdentity administrator login and give that identity verified administrator access across every installed product. Provision a separate everyday identity for ordinary suite use. Fix inconsistent acceptance of KyIdentity administrators across products; successful SSO alone is insufficient. Provide a printable record of installed product URLs and administrator logins/credentials for the owner to save securely. Administrator access does not change product-specific content confidentiality or recovery-custody contracts. The grant mechanism remains unselected.

- Administrator separation: application administrator identities are dedicated to administration and recovery; everyday identities have no application administrator grants. Enforce this separation in identity assignments and product authorization, including API access, rather than relying on labels or operator discipline. Dedicated administrator identities cannot use ordinary mail, calendar, chat, personal drive, notes or vault workflows. Administrative management of those services remains available within the product's confidentiality contract. Existing mixed-use accounts require a migration that preserves their content and verified owner access.

- Suite components must be open source, including required editor and collaboration features; proprietary editions and required proprietary extensions are excluded.

- Document management must integrate with centralized suite user management through a single administrative interface. Evaluate account lifecycle, groups, document access and offboarding; SSO alone does not satisfy this requirement. SCIM is a possible integration mechanism, not a selected implementation contract.

- Document architecture placement: Euro-Office runs in Kubernetes; the NAS hosts the document-management/storage container and persistent document data. Connect them through authenticated HTTPS file-transfer and save endpoints.

- The installation owner must administer KyDrive through their KyIdentity account. Explicit application administrator grants are separate from directory provisioning; an undisclosed local recovery login is not sufficient owner access.

- KyDrive provides both a private personal workspace per account and group-owned shared workspaces. Put workspace destinations directly in the main sidebar. Users can create blank documents, spreadsheets and presentations in the selected workspace and open them in Euro-Office.

- The NAS service is a general drive backend for suite applications, with Euro-Office as its first integration. Shared files belong to organization workspaces and survive employee offboarding. Applications use authenticated service APIs for file access. Initial scope: upload/download, folders, group-owned workspaces, permissions, versions, trash and quotas. Support the same API/data model on NAS storage or a Kubernetes persistent volume; desktop sync, WebDAV, SMB access and advanced search are deferred.

- Internal infrastructure, including NAS and Kubernetes hosts, is x86-64. Target `linux/amd64` for the initial document-stack containers.

- For drive implementation or deployment work, read `DRIVE_IMPLEMENTATION_PLAN.md` for the agreed scope, proposed phases and acceptance gates; unresolved implementation choices remain proposals.

- KyDrive people and groups are managed in KyIdentity; group permissions belong to KyDrive. Initial development is build-and-test local: NAS target `unraid.urlxl.us` / KyYard `hluswcdata01`, recovery target `https://kyrecovery.urlxl.us/`. The user authorized the live deployment pilot on 2026-10-04. Internal HTTPS origins are `https://kydrive.urlxl.us` and `https://office.urlxl.us`; the latter replaces the proposed euro-office hostname. Independent bulk backups were deferred for the KyDrive pilot; the suite installer's bulk path is restic to an append-only rest-server beside KyRecovery, off the NAS; restores run on the operator workstation and nothing decrypts inside KyRecovery (`KyQuickStart` catalog spec). NAS-local backup copies must not be described as independent. Preserve existing suite services and credentials; verify each pilot gate before declaring readiness.

- Nextcloud is excluded from document management and file storage. ONLYOFFICE products are excluded for geopolitical reasons. Evaluate Euro-Office's independent governance, build and release chain rather than rejecting it solely for inherited code; it is selected for the KyDrive local integration; production release requires source/build provenance verification. CryptPad's ONLYOFFICE-derived browser editors must remain explicit in comparisons.

- KyPasswords is replaced by Vaultwarden (decided 2026-10-06). Installer password selection is Vaultwarden only (confirmed 2026-10-09); exclude the older KeePass-based KyVault from new installs and adapters. Existing KyVault installations and data stay untouched. KyAuth does not become a Bitwarden client and becomes authenticator-only (TOTP, Push MFA, KyIdentity sign-on and passkey); Android users use the official Bitwarden app against Vaultwarden for passwords and passkeys. See `kyauth-android/docs/superpowers/specs/2026-10-06-vaultwarden-replaces-kypasswords.md`.

- Third-party apps installed with the suite must pass the admission rules in `KyQuickStart/docs/superpowers/specs/2026-10-06-third-party-catalog-design.md` (approved 2026-10-06): open source with SSO in the free edition, native OIDC against KyIdentity, admin from a claim or an unattended grant, and an offboarding path. No auth-proxy gate is built. Monitoring stays with kyPulse; forms, video calls and video streaming are out of scope.

- KyNotes is retained: no off-the-shelf OneNote replacement passes the admission rules. It gains OneNote-style notebooks (notebooks, sections and pages, freeform canvas pages, ink) as its own sub-project.

- Product branding uses the Busnes.app-site Systems stamp icon masters and platform-sized local exports. Busnes Light/Dark reached web and `kypost-android`, whose default is now Busnes Light; the remaining native clients keep their existing defaults until their own pass.

- Web themes default to the Busnes.app cream/light and charcoal/dark palettes with orange accents, following the OS until a browser-local choice is saved. Preserve existing named themes and saved choices.

When the user requests a durable behavior change, record it here or in the relevant child AGENTS.md

# KyQuickStart

Everything above this heading is the shared suite contract, copied from the busnes.app workspace
root so this repository stands alone. Keep it in step with the workspace copy.

## Purpose

One command-line installer that stands up the Ky suite and selected third-party apps across the
operator's servers: choices, prerequisite checks, installation, preconfiguration, and a printed
handover of admin and everyday logins.

## Ownership

- Public overview, build and command summary: `README.md`.
- Third-party catalog, admission rules, edge and reverse-proxy choices, and the app-aware bulk
  backup path: `docs/superpowers/specs/2026-10-06-third-party-catalog-design.md`.
- Installer architecture (CLI, catalog format, targets, state, setup, offboarding bridge,
  upgrades, testing): `docs/superpowers/specs/2026-10-06-installer-architecture-design.md`.
- Build sequence and product readiness gates: `docs/superpowers/plans/2026-10-06-kyquickstart-roadmap.md`.
  Implementation waits until the suite products are further along; write each phase's detailed
  plan when its gate opens.
- Phase 1 installer core: `docs/superpowers/plans/2026-10-08-phase-1-installer-core.md`.
- Phase 2 Kubernetes driver: `docs/superpowers/plans/2026-10-08-phase-2-kubernetes-driver.md`.
- All-product adapter implementation: `docs/superpowers/plans/2026-10-09-all-product-adapters.md`.
- Phase 4 edge implementation plan: `docs/superpowers/plans/2026-10-08-phase-4-edge.md`.
- Current edge configuration and supported deployment paths: `docs/edge.md`.
- Guided planning, demo deployment, existing installation references and explicit KyIdentity setup: `docs/guided-setup.md`.
- Resident offboarding runtime configuration, TLS, command behavior and remaining gates:
  `docs/offboarding.md`.
- Suite runner (KyYard manages the suite after install): ownership, hand-off, runner protocol v1,
  HTTP status table and image: `docs/superpowers/specs/2026-10-16-suite-runner-design.md`;
  operator steps in `docs/guided-setup.md` ("Hand-off to KyYard"); compose example
  `docs/kyyard-runner.compose.yaml`, pod manifest `docs/kyyard-runner.k8s.yaml`; image `Dockerfile`, published by
  `.github/workflows/release-image.yml`. That workflow runs `publish` (image, attested and
  verified), `binaries` (`scripts/release-binaries.sh`, the only home of the release `-ldflags`,
  stamps `main.runnerImage` with the verified digest; the four binaries are attested, uploaded
  and re-verified after download) and `promote` (`:stable`), each with only the permissions it uses.

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
  KyNotes, KyCalendar and KyYard on Kubernetes, KyDrive, KyPost and KyDNS on Docker (its one running managed container, through `docker
  exec` over SSH; KyDNS's shell-less image gets its setup files placed from the host). An installed KyNotes' entry is exactly `bundle: /data/installer/setup.json`
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
  privately into KyIdentity's pod, never replaced, and a pass of the setup that registered its
  client with `scim` added (outside the immutable preparation directories, so no reset).
  The catalog KyIdentity sets `KYIDENTITY_ALLOW_PRIVATE_CALLBACKS`, since routes resolve to
  LAN or pod addresses; loopback, link-local and metadata stay refused. Before any step, apply refuses
  (`issuer_changed`) a plan that moves the issuer KyIdentity's Deployment runs with, or the URLs
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
- A third-party app enters the catalog only if it passes every admission rule in the catalog
  spec. Record each rejection and its reason there.
- Third-party apps run on Docker hosts only. Kubernetes hosts Ky products and the edge
  components (cloudflared, Nginx Proxy Manager, frp client), deployed with `client-go` typed
  objects (no Helm); the edge components also deploy to Docker hosts. An app leaving the catalog stops taking new installs after
  notice, and existing installs are handed to their owner with `unmanage`.
- Every installed app signs in through KyIdentity with native OIDC, uses *Assigned users only*,
  and has an offboarding adapter. The installer builds no auth-proxy gate.
- Bridge SCIM connectors are app-scoped: assignment loss revokes only that app; global
  disablement reaches every owning connector. Register verified native bindings first and
  qualify the complete connector lifecycle before deployment. New-account creation requires
  an explicit protected per-app opt-in and a verified durable native receipt; never adopt a
  foreign account. A revocation-only receiver
  does not satisfy active profile provisioning or resident-service acceptance.
- Forgejo group delivery is a separate protected runtime opt-in: pin organization and unit
  permissions, use verified account/team receipts, and retain native repositories on logical
  group retirement. Qualify actual KyIdentity group event delivery before deployment.
- Reverse proxy is Nginx Proxy Manager. On an existing NPM, touch only installer-created hosts,
  certificates and certificate users, through its API, never its database. LAN DNS records in
  KyDNS are touched only while listed in the installer's ledger (`docs/edge.md`). NPM admin is LAN-only; its credentials go in the handover.
  A host is this installation's only with `stack.yaml`'s `installation_id` in its marks; hosts
  with the older target-only marks change only under `apply --adopt-edge-hosts`, once. On
  Kubernetes the catalog's `npm` app keeps its admin API on a ClusterIP Service reached through
  the driver's port-forward (`kube://`); bootstrap replaces NPM's first or default administrator
  with a cluster-generated one (`docs/edge.md`).
- One edge (NPM, and a tunnel or frp client) per target and one owner per hostname. A route's
  app is on the edge's target or on a Docker target, never in another cluster; a cluster edge's
  egress admits such an upstream's `forward_host` (an IP) and port alone. The installer
  sets each product's trusted-proxy value to exactly where its edge connects from (pinned /32 on
  Docker, NAT-free LAN /32 across hosts, pod network plus an edge-only NetworkPolicy in the
  cluster), never `0.0.0.0/0`, and `verify` checks the client address the product reports.
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
  healthy and keeps the app stopped until that is delivered (`restore_incomplete` otherwise), so
  a removed person is readmitted only for the measured seconds between the app's start and the
  replay; until that replay is delivered (`.restore-pending`), `apply` refuses to start the app.
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
  catalog KyYard): its workload becomes a note step and requests naming it are refused
  (`runner_host`). The workstation deploys that app only with a digest-pinned runner image
  (`cli.Options.RunnerImage`, stamped into release binaries by `scripts/release-binaries.sh`; empty
  source builds refuse it; `version --runner-image` prints it). The runner's SSH key, kubeconfigs and
  identity stay in its state volume beside its secret-free `plan/`; the workstation never
  receives them. Runner access on targets is exactly the installer's: one `restrict`ed
  authorized_keys line, or a ServiceAccount bound to the preflight permission list.
- Deployments record their catalog version (`kyquickstart/version` in `compose.kyq.yaml` or on
  the app namespace), which outlives uninstall. `apply`, `install` and `upgrade` gate a version
  change on the manifest's upgrade rules: `migrates_data` is always `backup_required` (a
  `kyquickstart backup` does not lift it yet); any other upgrade needs an explicit, recorded
  acknowledgement.
  Uninstall keeps data and secrets; unmanage drops the managed labels and the installer then
  refuses to install over the app. Error codes are `internal/suite` codes.
- Remote commands are POSIX `sh`, quoted with `remote.Quote`. The SSH runner sends each one
  base64-encoded to `sh` (`remote.wrap`), so a non-POSIX login shell such as fish cannot alter
  it. Unit tests run commands for real through `remote.Local` and under sh, bash and fish.
- Host keys are pinned in `<state>/known_hosts`; a changed key is never accepted, by flag or prompt.
- On a cluster each app lives in namespace `kyq-<app>` (Pod Security `restricted`, default-deny
  ingress) and the run lock is Lease `kyquickstart/kyquickstart-lock`. A manifest's
  `pod_security: baseline` is the only exception (today only NPM, whose root init it needs;
  `docs/edge.md` records its measured capabilities). Only edge components publish NodePorts,
  never NPM's port 81, and preflight refuses one held by any Service but that app's own; with an edge on the cluster, apps admit ingress from its pods alone. The installer never adopts
  a namespace or object without the managed label. A kubeconfig is referenced by path and may not
  live inside the state directory, symlinks resolved. One target per cluster; `apply` repairs
  drift in an app namespace's labels and its default-deny policy. A target's optional
  `storage_class` sets every claim's class and must exist at preflight; without it claims need a
  default class. A claim's class never changes: `apply` refuses an existing claim of another class.
  A non-root app mounts each claim through a 0700 `private` subdirectory that an init container
  (the pinned BusyBox helper, the app's uid) creates. `kubernetes.termination_grace_seconds`
  (0-3600, 0 keeps the default) sets the pod's termination grace. A `kubernetes.env` value may contain
  `{{host}}`, replaced by the URL host of the app's one edge route (hostname, plus `:client_port` when it is not 443); no other template syntax.
- A Compose template may write `{{host}}`, `{{forward}}` (the route's
  `forward_host:forward_port`, an IP of the Docker target, where the app publishes its port),
  `{{forward_addr}}` (that IP alone) and `{{edge_address}}` (the route's edge's
  `egress_address`; a plan whose edge has none is refused before connecting).
  Compose runs with `KYQ_UID`/`KYQ_GID` set to the target user's; `./<name>` bind directories
  are created private by the driver before Compose, and no other relative bind source is
  accepted.

## Work Guidance

Continue adapter implementation across every supported server product; a first working adapter
is a milestone, not completion of the all-product request. Product interfaces and local tests
may be built before their production release gates open.

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

- `make build`: builds workstation `kyquickstart` and resident `kyoffboard` binaries.
- `make ci`: tidy check, gofmt, vet (including the `e2e` tag), race tests. Runner tests decode
  every answer with KyYard's client types (`internal/runner/kyyard_test.go`).
  `cmd/kyquickstart/release_test.go` runs `scripts/release-binaries.sh` (the stamp in all four
  binaries, its refusals) and checks `release-image.yml`'s job graph, permissions and pins.
- `make workflow-lint`: needs Docker (CI runs it in `vulncheck`); actionlint v1.7.12 on every
  workflow and offline, digest-pinned zizmor (medium and up) on `release-image.yml` and
  `kyoffboard-image.yml`.
- `make e2e`: needs Docker; runs `apply` against an sshd container on the local Docker socket and
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
  Backups (`test/e2e/backup_test.go`) go to a disposable append-only TLS rest-server on its own
  network, published on the default bridge's gateway. `TestApply` backs up and restores the
  test-only `dumpdemo` (Postgres dump, a file volume with a `skip` path, a capsule secret) from
  its own root; `TestKubernetesNPMEdge` gives the Docker target a destination, asserts KyDrive's
  handover lines, backs KyDrive up cold and proves a restore of the running app is refused.
  `TestKubernetesRestore` runs the KyDrive drill on its own kind cluster: with KyIdentity at the
  attested 0.4.3 release (a catalog copy), restore is refused before any restic call; after
  `upgrade kyidentity` to the catalog's 0.4.4 it proves a disaster restore with the handover's
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

- `cmd/kyoffboard/AGENTS.md`: resident bridge command flags, safe output and signal lifecycle;
  the root retains deployment and credential provisioning.
- `internal/cli/AGENTS.md`: guided questions, local plan persistence and command orchestration,
  including `backup` and `restore`.
- `internal/runner/AGENTS.md`: `kyquickstart serve` and the serve-* commands inside the runner
  container; the root owns the ownership rule and the CLI owns handoff.
- `internal/edge/AGENTS.md`: NPM reconciliation, connector deployment and public forwarded-header verification.
  The root owns stack schema, CLI orchestration and target drivers.
- `internal/adapters/AGENTS.md`: native third-party setup, administrator verification,
  offboarding and disposable qualification; the root retains catalog admission and deployment.
- `internal/offboard/AGENTS.md`: resident bridge account/group bindings, creation receipts, directory/membership intents, durable revocation jobs and audit
  outbox; the root retains deployment, target secrets, network policy and admission.
