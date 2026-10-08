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

- KyPasswords is replaced by Vaultwarden (decided 2026-10-06). KyAuth does not become a Bitwarden client and becomes authenticator-only (TOTP, Push MFA, KyIdentity sign-on and passkey); Android users use the official Bitwarden app against Vaultwarden for passwords and passkeys. See `kyauth-android/docs/superpowers/specs/2026-10-06-vaultwarden-replaces-kypasswords.md`.

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

- Third-party catalog, admission rules, edge and reverse-proxy choices, and the app-aware bulk
  backup path: `docs/superpowers/specs/2026-10-06-third-party-catalog-design.md`.
- Installer architecture (CLI, catalog format, targets, state, setup, offboarding bridge,
  upgrades, testing): `docs/superpowers/specs/2026-10-06-installer-architecture-design.md`.
- Build sequence and product readiness gates: `docs/superpowers/plans/2026-10-06-kyquickstart-roadmap.md`.
  Implementation waits until the suite products are further along; write each phase's detailed
  plan when its gate opens.
- Phase 1 installer core: `docs/superpowers/plans/2026-10-08-phase-1-installer-core.md`.
- Phase 2 Kubernetes driver: `docs/superpowers/plans/2026-10-08-phase-2-kubernetes-driver.md`.

## Local Contracts

- Command-line only: no GUI, web UI or full-screen terminal UI. `plan` asks its questions as
  plain terminal prompts, and every command also runs unattended from `stack.yaml`.
- A third-party app enters the catalog only if it passes every admission rule in the catalog
  spec. Record each rejection and its reason there.
- Third-party apps run on Docker hosts only. Kubernetes hosts Ky products and the edge
  components (cloudflared, Nginx Proxy Manager, frp client), deployed with `client-go` typed
  objects (no Helm); the edge components also deploy to Docker hosts. An app leaving the catalog stops taking new installs after
  notice, and existing installs are handed to their owner with `unmanage`.
- Every installed app signs in through KyIdentity with native OIDC, uses *Assigned users only*,
  and has an offboarding adapter. The installer builds no auth-proxy gate.
- Reverse proxy is Nginx Proxy Manager. On an existing NPM, touch only installer-created hosts,
  through its API, never its database. NPM admin is LAN-only; its credentials go in the handover.
- One edge (NPM, and a tunnel or frp client) per target and one owner per hostname. The installer
  sets each product's trusted-proxy value to exactly where its edge connects from (pinned /32 on
  Docker, NAT-free LAN /32 across hosts, pod network plus an edge-only NetworkPolicy in the
  cluster), never `0.0.0.0/0`, and `verify` checks the client address the product reports.
- Bulk data goes to restic (K8up on Kubernetes) on an append-only rest-server container on the
  KyRecovery host, off the NAS, one repository and password per app. The password lives on the
  app's target and a copy is sealed in KyRecovery; the rest-server host never holds one.
- Restores start from KyRecovery's UI or the CLI and always run in `kyquickstart restore`.
  Nothing decrypts inside KyRecovery, server or browser.
- Secrets are generated once on their targets and read back on re-run; the workstation state
  directory never holds a secret.
- The installer's only authority inside a Ky product is its container-local `apply-setup`
  command. Installer-managed workloads carry `ky.managed-by=kyquickstart`; only the shared upgrade
  module changes them, from the CLI or KyYard, under a target-side lock.
- Remote commands are POSIX `sh`, quoted with `remote.Quote`. The SSH runner sends each one
  base64-encoded to `sh` (`remote.wrap`), so a non-POSIX login shell such as fish cannot alter
  it. Unit tests run commands for real through `remote.Local` and under sh, bash and fish.
- Host keys are pinned in `<state>/known_hosts`; a changed key is never accepted, by flag or prompt.
- On a cluster each app lives in namespace `kyq-<app>` (Pod Security `restricted`, default-deny
  ingress) and the run lock is Lease `kyquickstart/kyquickstart-lock`. The installer never adopts
  a namespace or object without the managed label. A kubeconfig is referenced by path and may not
  live inside the state directory, symlinks resolved. One target per cluster; `apply` repairs
  drift in an app namespace's labels and its default-deny policy.

## Work Guidance

## Verification

- `make ci`: tidy check, gofmt, vet (including the `e2e` tag), race tests.
- `make e2e`: needs Docker; runs `apply` against an sshd container on the local Docker socket and
  against a kind cluster (`go run sigs.k8s.io/kind@v0.33.0`), including drift and stale-lock cases.

## Child DOX Index
