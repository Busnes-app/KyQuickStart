**Repo:** KyQuickStart and Ky suite server repositories
**Worktree:** /home/yoshi/git/busnes.app/KyQuickStart (branch feat/phase-4-edge); product worktrees /home/yoshi/git/busnes.app/<Product>-installer-setup (branch feat/installer-setup or feat/installer-adapter)

# All product setup adapters

User instruction, 2026-10-09: continue until adapters are built for all products.
This authorizes local product-interface and installer development; it does not authorize
changing live installations. Existing product checkouts and other agents' work are preserved.

## Scope

Server products: KyIdentity, KyRecovery, kyPulse, KyYard, KyPost, KyMessage,
KyNotes, KyMark, KyDNS, KyDrive, KyCalendar and KyForge. ky-server-base is a template,
not an installation choice. Android/native clients are paired clients of these servers,
not separate server deployments. Vaultwarden is the only installer password service (user confirmation, 2026-10-09).
The older KeePass-based KyVault is excluded from new installer choices and adapter work;
existing installations and data are preserved. Euro-Office retains its selected integration contract. Admitted third-party catalog entries also receive
adapters; a rejected or still gated entry does not become admitted by adding a registry row.

## Build order

1. Reuse current KyIdentity, KyPost and KyNotes setup implementations. Reconcile differences
   in capability discovery, reports and inspection without replacing product security logic.
2. Put bounded private-file reads and the common setup envelope in ky-primitives. Product
   packages own their mutations, runtime activation, transactions and per-change audits.
   Test development consumers with an explicit isolated Go workspace; record any dependency
   release requirement instead of misrepresenting an unpublished library as a shipped release.
3. Complete each missing product-owned `apply-setup --file` interface. Products with live
   in-memory state use a private container-local socket or supported reload; a database
   edit that the running server cannot observe is not successful configuration.
4. Implement installer adapters and explicit target/container selection for each product,
   using SSH/Docker or client-go container exec. No database access from the installer.
5. Complete KyIdentity identities/groups/roles/assignments/SCIM wiring, exact owner grants,
   everyday denial and deprovision hooks needed to compose the product adapters.
6. Build admitted external-app adapters around their supported native APIs/CLIs, resolving
   upstream admission questions against primary sources before enabling catalog entries.
7. Run adapter acceptance against real disposable containers and test identity. Add guided
   inputs and unattended saved-plan wiring to the same adapter execution path.

## Product-specific ownership

| Product | Setup responsibilities |
|---|---|
| KyIdentity | Dedicated admin/everyday identities, groups, OIDC clients, app roles, assigned-only access, assignments and SCIM connector |
| KyRecovery | SSO/admin bindings, initialized blind-store configuration, named-service pairing-code generation; human ceremony remains a gate |
| kyPulse | SSO/admin bindings, targets, log sources, KyYard connection and recovery |
| KyYard | SSO/admin bindings, endpoint enrollment and management refusal for installer-labelled workloads |
| KyPost | Existing SSO/domain setup, issuer-bound owner access, DNS requirements, recovery and offboarding |
| KyMessage | Identity/control-plane settings, explicit owner authorization, Matrix prerequisites and recovery |
| KyNotes | Existing socket-based SSO/admin/backup setup, read-only inspection and deprovision proof |
| KyMark | SSO, issuer-bound administrator grants, recovery and deprovision proof |
| KyDNS | SSO/admin control and recovery, trusted-proxy/TLS behavior |
| KyDrive | SSO/SCIM settings and explicit exact-subject admin grant; preserve content grants and editor/service contracts |
| KyCalendar | SSO/SCIM settings and issuer-bound administrator grant; preserve calendar confidentiality and DAV authorization |
| KyForge | SSO/admin bindings, incident/service-management initial configuration and recovery |

## Acceptance

Each adapter must discover the supported contract, inspect real state, create only missing
configuration, verify runtime activation, and rerun without duplicate resources or resets.
Conflicts preserve existing settings and data. Every successful mutation has its required
product audit; native file audits require inactive staging until completion, while SQL
audit products commit authority and audit together. Secrets stay in protected target files; no recovery shares enter setup.

Prove owner administration and everyday denial through the product's actual authorization,
then prove deprovisioning ends sessions/tokens. Test cancellation/resume and conflicts.
Run relevant product checks, installer CI and disposable transport tests. Update every
owning DOX chain. Mirror local plans/status/handoffs to myslop. Keep production release,
image provenance and live deployment gates explicit; source adapters are not a deployed suite.

## Current implementation evidence

- Shared `ky-primitives/applysetup`: strict protected-file boundaries, common envelopes,
  bounded reports, private socket transport and graceful activation; focused race tests pass.
  Shared recovery-key loading now also refuses invalid retained k-of-n topology without repair.
  The setup package is unpublished; development consumers require an explicit workspace
  containing the product and shared-library worktrees. The old RAM-backed
  `/tmp/kyq-adapters.work` was lost during interruption; current identity qualification uses
  `/home/yoshi/.cache/kyquickstart-work/identity-fixture.work`.
- KyIdentity: identity/client adapter and explicit native per-app owner/everyday role
  assignments are implemented; real SSH/container creation, read-only check and resume
  pass. Native PKCE exchange, signed ID tokens and UserInfo prove exact role claims and
  management-only/ordinary-only/unassigned denial. Owned initial groups, app-group assignments
  and optional native SCIM group delivery are also implemented and verified. Full downstream
  lifecycle composition plus suite-wide separation remain open.
- KyDrive: product-owned setup, atomic owned identities/configuration/audits, protected
  SCIM credential references, startup activation, dedicated new-admin denial, public SCIM
  session revocation and G11 fields are implemented. Whole-product race tests pass; actual
  SSH/Docker apply, one graceful restart, live check and resume pass. Recovery pairing in
  this envelope, mixed-use migration and complete suite identity/editor/recovery acceptance
  remain open; the adapter refuses unsupported pairing before writes.
- KyNotes: existing setup interface now advertises capabilities and supports read-only
  inspection of running SSO, exact admin bindings, schedule and sealed pairing. Setup CLI
  no longer initializes keys or probes directory writes. Public G11 network diagnostics
  share exact address resolution with rate limits while IPv6 grouping stays only in limit keys.
  Focused race tests, rebuilt container script and actual SSH create/check/resume fixture pass.
  Dedicated administrator separation, existing mixed-use migration and full suite role
  composition remain acceptance gates.
- KyPost: native version-1 setup now supports capabilities, bounded target-held credential
  references and genuinely nonmutating configuration inspection (SQLite WAL coordination
  companions may be maintained). Read-only state opening verifies bootstrap without migrations;
  retained pairing inspection verifies its pin/topology and token decryption without provider
  calls or application lock creation. New browser/directory identities and authorized links
  retain verified issuer bindings independently of native mailbox ownership. Canonical issuer
  validation prevents discovery/directory mismatches. Narrow public G11 diagnostics reuse
  existing trust/security logic. Mandatory reviewer-agent passes found and fixed route-auth,
  retained-token, schema and topology issues. Focused race tests and vet pass, and actual SSH
  create/check/resume as `kypost` passes while preserving external mail/recovery blockers.
  App-role/directory composition, external-IMAP administrator separation and full mail/recovery
  qualification remain open. Standalone app/backup/state/SSO/users race tests and vet also pass; full API qualification remains open.
- kyPulse: common-envelope setup, atomic new administrator/viewer ownership and keyed audit,
  private live IPC, activation restart, protected OIDC/directory-secret references and G11
  diagnostics are implemented. Product store/config/API/auth/server race tests and vet pass.
  Actual private IPC verifies activation; real role authorization and signed directory
  deactivation revoke both owner/everyday sessions. Actual SSH apply/restart/check/resume
  fixture passes with exactly one restart. Native owned monitoring target/source enrollment
  is qualified in the milestone below. Installer recovery pairing and KyYard enrollment
  remain explicit follow-on gates; unsupported pairing
  is refused before writes.
- KyDNS: common-envelope setup, private live IPC, exact verified administrator subjects,
  atomic configuration/audit, read-only checks and ownership conflicts are implemented.
  DNS is administrator-only and refuses ordinary identity assignments. Signed revision-fenced
  directory offboarding revokes sessions and deletes bound API tokens and descendants,
  preserving independent local recovery credentials. Protected managed credentials and
  their retained destination map are included in sealed backups. G11 diagnostics share
  trusted-peer resolution with rate keys, callback scheme and cookies. Whole-product tests,
  targeted race tests, vet and actual SSH create/live-check/resume pass without a restart.
  Installer backup enrollment and full central composition remain explicit follow-on gates.
- KyForge: common-envelope setup, private live IPC, startup/graceful activation, atomic
  owned accounts/configuration/audit and exact verified issuer binding are implemented.
  Signed directory desired state fences durable revisions/digests, preserves native roles
  and tickets and revokes sessions and pending device pairings on offboarding. Independent
  SCIM and old flat sync are refused in managed mode. Managed credentials are required sealed
  backup members; narrow G11 diagnostics share trusted-peer session/rate resolution.
  Whole-product race tests and vet pass. Installer store/API tests also pass against real
  disposable PostgreSQL 17. Actual SSH create/one restart/live check/resume passes.
  Installer recovery pairing, initial ITSM configuration and central composition remain open.
- KyMark: common-envelope setup, private live IPC, exact owned issuer/subject accounts,
  dedicated administrator API/browser separation and nonmutating checks are implemented.
  Keyed file-audit intent/completion surround inactive SQLite staging before activation;
  this is recoverable staging, not a transaction across audit files and SQLite. Pending
  accounts cannot authenticate; drift and offboarding are never repaired by setup.
  Signed directory revisions/digests fence authority updates; offboarding retains encrypted
  content and revokes sessions, pairings and device envelopes. Native admin mutations cannot
  change managed lifecycle or grants. Protected credentials and their destination map are
  required sealed backup members and restored-database drill requirements. G11 reports the
  same peer trust used for session/rate/cookie decisions. A fresh empty audit-anchor bug was
  fixed while retaining truncation checks; the ablation now disables both relevant defenses
  when proving detection. Whole-product race tests, vet, frontend build, managed backup drill,
  real signed OIDC owner/everyday/unassigned tests, audit-failure tests and all 42 security
  ablations pass. Actual browser owner administration works without a vault key; personal
  API requests return 403 and SSO responses contain no secret. Browser observed an existing
  font-path warning; no font assets changed. Actual SSH create/check/resume passes with no
  restart. Recovery enrollment, mixed-use migration and full identity composition remain open.
- KyRecovery: common-envelope setup, live private IPC and exact verified administrator
  subjects are implemented. Configuration, grants, native keyed audit and anchor commit in
  one SQLite transaction. Signed revision-fenced directory changes revoke sessions atomically;
  managed sign-in requires a verified ID token and refuses userinfo-only login and unassigned
  global administrators. Local recovery administration remains independent. G11 diagnostics
  reuse the trusted-peer logic for cookies, rate limits and audit addresses. Whole-product
  race tests, vet, real TLS OIDC and actual SSH create/live-check/resume as `kyrecovery` pass,
  with no restart. Setup leaves the suite recovery key absent: human custodian ceremony,
  named-service pairing orchestration and complete central composition remain explicit gates.
- KyMessages: common-envelope console setup, private IPC, graceful activation and exact
  owned issuer/subject grants are implemented. Accounts/configuration and native SQL audit
  commit together. Directory changes retain revision/digest fences, preserve deleted Matrix
  status, refuse reactivation and revoke sessions/MFA challenges/device pairings atomically.
  Native writers cannot change owned authority. Dedicated console administrators are inactive
  in the Matrix access projection and omitted from the ordinary chat link; native MAS sweep
  and call access use that projection. Central MAS assignment exclusion remains a gate, since
  sweeps are not instantaneous revocation. Managed credentials and their destination map are
  required sealed members and restored-database drill checks. Whole-product race tests, vet,
  Compose checks, managed backup drill and real TLS OIDC owner/everyday/unassigned tests pass.
  Installer store/API tests pass against disposable PostgreSQL 17; actual SSH create/one
  graceful restart/live-check/resume passes. Matrix provisioning/acceptance, recovery enrollment,
  mixed-use migration and full identity composition remain open. Frontend, Matrix configs and
  upstream images are unchanged; their complete browser/Matrix release gates were not rerun.
- KyCalendar: common private-IPC setup creates immutable issuer-bound accounts, sign-in
  binding and native SQL audit in one transaction using the native access/name locks.
  Standalone sign-in and reattach writers cannot change owned authority. Protected credentials
  are reread for OIDC flows; exact app roles preserve administrator/everyday separation.
  Signed revision-fenced offboarding revokes sessions/MFA/pairings/app passwords and retains
  calendars. G11 shares native proxy resolution. Managed credential backups and seal/open
  drills pass, retaining calendar count/object checks. Whole-product race tests, vet,
  frontend 110 tests/build/vendor check, vendored CalDAV tests and disposable PostgreSQL
  store/API checks pass. Actual SSH creates three objects, activates with one graceful restart
  and passes check/resume. The built embedded UI was verified in the T3 shared browser:
  native recovery sign-in/password replacement, managed read-only fields/Test/Save,
  callback Copy, credential redaction and ordinary calendar/app-password 403s pass.
  Installer recovery enrollment, central client/group/role composition, legacy mixed-use
  migration, real-device interop and image/library release provenance remain gates.
- Installer registry contains KyIdentity, KyDrive, KyNotes, KyPost, kyPulse, KyDNS, KyForge,
  KyMark, KyRecovery, KyMessage, KyCalendar and KyYard. Native reports are sanitized, checks refuse mutations,
  and KyPost external pending qualifications stay in the report. Docker KyPost and KyRecovery
  setup execute explicitly as their runtime users. Installer CI passes with all twelve entries
  and optional real SSH fixtures.
- KyYard's common source/transport adapter is implemented in its isolated worktree. It
  now has the common container-local `apply-setup` CLI/private socket, immutable owned
  account/configuration records and native SQL audit in one transaction, zero-restart
  live provider activation, protected native settings/account mutations, exact verified
  issuer/subject/app-role sign-in, signed canonical revision/digest-fenced offboarding,
  atomic session/MFA/pairing revocation and native G11 network reporting. Platform
  administrator accounts receive no implicit tenant memberships. Whole-product race
  tests and vet pass; focused store/API checks pass against SQLite and disposable
  PostgreSQL 17. The actual binary passes private-IPC create/check/resume without restart
  and certificate-verified TLS OIDC/PKCE owner/everyday/unassigned admission, global-role
  rejection and durable app-role loss/session purge. The runnable OIDC fixture is
  `KyYard-installer-setup/scripts/installer-oidc-test.py`; DOX and `docs/INSTALLER_SETUP.md`
  describe its current contracts. Managed SQLite backups now carry both setup credentials
  and their retained absolute-path mapping; real seal/open drills reject omitted credentials
  independently of recipe changes and reject conflicting mappings. Docker lifecycle,
  replacement, removal and exec refuse installer-owned containers before mutation.
  Kubernetes workload actions/configuration/exec refuse installer ownership on object or
  pod-template labels; deployment/removal ownership excludes such objects. Full runtime
  and backup package race tests, renewed whole-product race tests and vet pass after these
  changes (`/tmp/kyq-yard-runtime-backup.log`, `/tmp/kyq-yard-all-race-current.log`,
  `/tmp/kyq-yard-vet-current.log`). The rebuilt binary passes the TLS OIDC regression again.
  All 523 frontend tests, vendor check and production build pass. T3 browser proof covers
  native bootstrap/password replacement, read-only managed provider controls, public callback
  Copy, credential redaction and exclusion from account linking; the loopback proxy fixture
  maps Origin for layout/authenticated-projection checks and does not establish real TLS/edge
  acceptance. `KyYard-installer-setup/UI-VERIFICATION.md` records that limit and screenshot.
  Actual SSH creates three objects and passes check/resume with zero restarts
  (`/tmp/kyq-yard-ssh.log`). Installer `make ci` passes with all twelve source interfaces
  (`/tmp/kyq-installer-ci-yard.log`). Owning backup/runtime/web/CLI DOX, setup docs and restore
  runbook are updated; API/root contracts and child indexes remain unchanged because ownership
  and route shape are unchanged. Remaining qualification: restored-process verification of
  the credential mapping, tenant/endpoint composition, shared upgrade orchestration, recovery
  enrollment and release provenance. Existing KyYard installations were untouched.
  User confirmed **Vaultwarden only** for password installation on 2026-10-09. The older
  KeePass-based KyVault is excluded from installer choices. Partial native KyVault work is
  preserved solely in `KyVault-installer-setup` and is not registered or qualified: account
  staging, protected-reference SSO, directory staging and CLI/socket sources exist; initial
  native users/SSO/API race and command tests passed, but added setup tests, frontend,
  backup, transport and DOX were not completed. Do not deploy or resume that excluded
  adapter unless the user changes scope. No existing KyVault installation/data changed.
- Forgejo: native source and account adapters are implemented in
  `internal/adapters/forgejo`. Source setup uses release-qualified native administrator
  forms, because the public REST API omits authentication sources. It creates only missing
  sources, reads every authority/claim/scope field back, compares the credential in memory,
  pins the native ID, refuses unpinned collisions and refuses configuration drift without
  mutation. Native source forms require Forgejo 16.0.5; secrets never appear in returned
  source settings, server bodies or errors. Account `Apply` uses the native REST API and
  pins native account ID, authentication-source ID and OIDC subject. Reruns verify retained
  bindings without changing authority/passwords or reactivating an offboarded account.
  `GrantAdmin` verifies the role granted by native OIDC claim mapping; it does not grant
  an unverified account administration. Offboarding disables before deleting tokens, checks
  final authority and preserves accounts/content. API redirects are refused; native forms
  never follow redirects and reject foreign locations. Responses/timeouts are bounded.
  Boundary race tests reject incomplete identity, ambiguous forms and malformed token lists
  before mutation. A real disposable Forgejo 16.0.5 rootless image,
  `codeberg.org/forgejo/forgejo@sha256:5effb7305584aca479b29fde6f9631a6dbe86ae798ae02eeea33a3666f0c0bf8`,
  passes source create/inspect/resume, wrong-secret/role conflicts without overwrite,
  source-bound account provisioning/resume, wrong-subject refusal and a certificate-verified
  test IdP issuing RSA-signed OIDC tokens with real PKCE. Actual owner/everyday sign-in and
  owner administration/everyday administrator denial pass; a pre-created unassigned identity
  is denied access. The owner role is produced by the native OIDC claim mapping, without a
  direct API administrator seed. All 101 personal tokens are revoked; old token requests are
  denied; old and new sign-in sessions reach the native suspended-account boundary; a retained
  private repository keeps its ID and description. Native suspended-account pages deliberately
  return HTTP 200, so the test checks the rendered denial rather than assuming status alone.
  Native source-form creation activates immediately with zero restarts. Earlier CLI source
  creation required one explicit restart to initialize the provider cache before first PKCE
  login; `/tmp/kyq-forgejo-oidc-test.log` records that separate passing path. The current proof is
  `KYQ_TEST_FORGEJO_IMAGE=<pinned-image> go test -race -v ./internal/adapters/forgejo`, recorded
  in `/tmp/kyq-forgejo-source-test.log`. Fixtures bind only ephemeral loopback endpoints on
  Docker host networking, use disposable SQLite data and remove their own containers/volumes.
  The existing `golang.org/x/net` dependency is reused as a direct dependency for native HTML
  parsing; no new version or library was introduced. Installer `make ci` passes after the final
  source/client changes (`/tmp/kyq-installer-ci-forgejo-source.log`); `git diff --check` passes.
  Owning adapter/Forgejo child DOX and installer architecture explain the native forms and release
  qualification; affected child indexes are current. Existing
  CLI/edge and workspace parent DOX are unchanged because their contracts are unchanged.
  Remaining qualification: actual KyIdentity assignment/client composition, role-loss handling,
  dedicated-admin ordinary-workflow policy, native SSH/other application tokens, protected
  target-owned receipt durability and unknown-outcome resume, PostgreSQL/bulk backup/restore,
  CLI/guided deployment orchestration, browser UI acceptance and release provenance. No production
  catalog entry is enabled by this source/API milestone. No live service changed.
- BookStack native offboarding qualification reproduced a missing interface in v26.09.1.
  `internal/adapters/bookstack/qualification.py` starts only disposable BookStack and MariaDB
  containers, creates a source-bound non-administrator and proves access using an existing
  session and API token. Updating the user with `active:false` and `disabled:true` returns
  success but leaves both credentials authorized. The retained account ID, subject and roles
  are unchanged. This is a passing counterexample, not a successful offboarding adapter.
  `/tmp/kyq-bookstack-qualification.log` records the result. The packaged UserApiController
  matches upstream commit `31d94dd8b98e3334dc01b66bed6d514719a2f85d`, normalized SHA-256
  `2241f23894c2f346069387221146983f0bc4bc9958fcb1245dcd779b28cd96b5`.
  Fixture pins: BookStack
  `lscr.io/linuxserver/bookstack@sha256:9b5cd22653b503a53a764671ed96e2e87ea3f62bc3fba6250de86a73d642c426`
  and MariaDB
  `mariadb@sha256:93fc3fe333b6cdfb061425869c2c3a5bb2851c0c74dbebc2c940f024e7482d76`.
  Run with `KYQ_TEST_BOOKSTACK_IMAGE` and `KYQ_TEST_BOOKSTACK_DB_IMAGE` set to these pins,
  then `python3 -u internal/adapters/bookstack/qualification.py`. All owned containers,
  networks and volumes were removed. Standard authentication is tested here; OIDC remains
  unverified. The catalog's earlier disable-via-API claim is corrected. Next: implement and
  test a product-native offboarding extension that retains accounts/content and denies old
  sessions, tokens and new sign-in, then compose setup, backups and release qualification.
  The MIT community extension in `internal/adapters/bookstack/module` now provides
  `php artisan kyquickstart:offboard --request=<canonical-private-file>`, using native
  logical-theme hooks and framework services, with no authentication proxy. Its
  `ky.bookstack-offboard/1` check/deprovision contract pins native ID, configured HTTPS issuer
  and exact external subject, and requires protected application-owned input. Immutable
  per-ID and per-subject fences are atomically published and synced before native API-token
  deletion. Accounts, roles and content remain. `KYQ_BOOKSTACK_REVOCATION_DIR` is an explicitly
  provisioned persistent 0700 directory outside the web root; the HTTP workers and CLI share
  its owner. Canonical paths require root/application-owned ancestors without group/other
  write permission; native public/upload roots are refused. Linux effective UID is read
  from `/proc/self/status` because the pinned PHP image lacks the POSIX extension. Missing
  or unsafe storage fails closed. Revocation has no reactivation command.
  Runtime qualification now passes with `KYQ_TEST_BOOKSTACK_MODULE=1` added to the command
  above (`/tmp/kyq-bookstack-module.log`): wrong subject/issuer, oversized and non-private
  requests are refused; old web/API sessions, old tokens and new password sign-in are denied;
  native account/roles/owned book identity and content remain. Two simultaneous retries use
  separate private request files and return the same receipt. Restart preserves the fences.
  A replacement token, changed subject on the retained native ID, and the same revoked
  subject on a newly created native ID all remain denied. Native PHP lint and the full
  fixture pass; all owned containers/networks/volumes are removed. Python syntax and
  `git diff --check` pass. This is functional native HTTP/CLI evidence, not browser UI
  acceptance, actual OIDC qualification or a complete setup adapter. The self-registration
  hook is implemented but its signed-OIDC path still requires a test. Native requests
  already executing at publication are not aborted. Next: signed native OIDC/registration,
  central assignment/admin policy, setup/installer orchestration, bulk restore including
  the module and fences, and pinned release provenance. No production catalog entry is
  enabled and no live service changed. Module/parent ownership, verification and child DOX
  indexes are updated. Root/CLI/edge and workspace parent docs are intentionally unchanged
  because their contracts and indexes still cover this work. BookStack remains in scope
  and gated; the all-products goal remains active.
- BookStack's Go native-command offboarding client is implemented in
  `internal/adapters/bookstack/client.go`. Execution is supplied already bound to an owned
  container and its application user; this does not adopt targets or add a production
  catalog entry. `Inspect` and `Deprovision` stage bounded JSON through stdin into a separate
  protected target directory for every call, remove it on exit, pin module `0.2.0`, check
  exact native ID and issuer/subject SHA-256, reject missing/malformed authority and
  sanitize native error/output bodies. Calls have a 30-second context deadline. The module
  now reports `revocation_complete` separately from `disabled`: a single fence denies the
  original account but is incomplete, even when token count is already zero. The client
  checks before mutation, repairs that partial state on an explicit retry, and verifies
  both fences and zero tokens afterward. The real Go-client test runs inside the Python
  fixture with `KYQ_TEST_BOOKSTACK_ADAPTER=1`; it accepts only the fixture's exact nonce,
  ownership labels and image digest. It passes initial inspect, wrong-subject refusal,
  deprovision, deliberate removal of the subject fence with zero tokens, repair, repeat
  inspection/idempotence and target staging cleanup. The Python fixture then passes all
  retained-content/access-denial, changed-ID/subject/token and restart checks.
  `/tmp/kyq-bookstack-adapter.log` records this passing native adapter run. All owned
  containers/networks/volumes are removed. Installer `make ci` passes after the final Go
  changes (`/tmp/kyq-installer-ci-bookstack-adapter.log`); `git diff --check` passes. Owning
  client/module/parent DOX and catalog qualification are updated; child indexes remain
  current. Root/CLI/edge and workspace parent docs are unchanged because their contracts
  are unchanged. Remaining: signed native OIDC/self-registration, owner/everyday grants
  and assigned-only composition, full setup and CLI/guided dispatch, protected target
  ownership/receipts, bulk restore including fences/module and release provenance. Browser
  UI acceptance remains separate. No live deployment changed; the complete all-products
  goal remains active.

## Status checkpoint: native KyIdentity group setup

Native KyIdentity setup now provisions up to 32 owned initial directory groups, ordinary-user
membership and app-group assignments, with optional native SCIM group delivery. Administrator
and foreign-account membership are rejected at the public boundary. Read-only checks and
resume verify existing definitions and grants; operator changes are refused rather than
overwritten. Group, membership and app-grant changes share the native setup transaction and
audit rollback. Existing group MFA policy remains product-owned.

Verification passed: installer `make ci` and `make build`, native KyIdentity full Go race
suite and vet, focused drift/audit/validation boundaries, native TLS SCIM group delivery and
disablement, and an actual disposable SSH/container create/check/resume fixture. Evidence:
`/tmp/kyq-identity-groups-all.log`, `/tmp/kyq-identity-groups-vet.log`,
`/tmp/kyq-identity-groups-final-boundaries.log`, `/tmp/kyq-identity-groups-worker.log`,
`/tmp/kyq-identity-groups-installer-ci.log`, `/tmp/kyq-identity-groups-build.log`, and
`/tmp/kyq-identity-groups-ssh.log`. The full race process completed successfully.

Setup interfaces exist locally for all 12 registered Ky server products. This does not
complete all-product deployment acceptance: automatic bundle and credential wiring, full
provider/profile/group lifecycle composition, dedicated-administrator separation across
products, Vaultwarden administration, KyYard and recovery enrollment, sealed-secret and
bulk restore, release dependencies and image provenance remain open. Production catalog
entries remain gated. No live services, commit, PR or release changed.

DOX contracts and native setup documentation were updated for group setup; the installer
CLI contract and guided runbook were updated. Root ownership and child indices are unchanged
because this checkpoint introduces no new documentation boundary. Work remains active.
- **kyPulse ordered audit-ingest prerequisite:** isolated native worktree
  `/home/yoshi/git/busnes.app/KyPulse-installer-setup` (branch `feat/installer-setup`) now
  supports optional `Ky-Log-Sequence` on the existing paired-source NDJSON route. Migration 10
  stores only the latest sequence/body SHA-256 per source. Its receipt, log/activity rows,
  usage and retention commit in the same existing transaction. An exact latest replay returns
  verified receipt headers without new rows, including after retention removes the payload.
  Changed replay bytes, gaps, stale sequences and legacy fallback after activation return 409;
  malformed headers/batches and revoked sources remain refused without acknowledgment.
  Unsequenced senders and internal collection retain their current behavior. One dedicated
  source needs one ordered writer: persist its current body/sequence before sending and its
  verified acknowledgment before advancing. A lost reply alone is safe to retry; rewinding
  an older sender state needs explicit reconciliation. This is acknowledgment state, not the
  native keyed audit chain or permanent log archival. SQLite capsules preserve receipts and
  source identities while excluding collected payloads; a sender ahead of the restored
  receipt fails closed rather than skipping a gap.
  Native store/API tests prove concurrent duplicate suppression, transaction rollback,
  byte-sensitive replay, canonical header validation, stale/gap/fallback refusal, attribution,
  revocation and retention; the real SQLite snapshot/open test proves receipt preservation.
  Final Go race suite passes (`/tmp/kyq-pulse-receipt-all.log`), store/API race suites against
  an owned disposable PostgreSQL 17 fixture pass (`/tmp/kyq-pulse-receipt-postgres.log`), and
  `make lint test-web smoke` passes (`/tmp/kyq-pulse-receipt-checks.log`, 88 frontend tests).
  `go vet ./...`, formatting and `git diff --check` pass. All test processes are terminal and
  the owned PostgreSQL container is removed. `make ci` stops at its tidy prerequisite because
  published ky-primitives v0.9.0 lacks the already-required `applysetup` package
  (`/tmp/kyq-pulse-receipt-ci.log`); local checks use `/tmp/kyq-adapters.work`. No module files
  were changed to hide that release gate. Native root/API/store/backup DOX and README now
  describe wire and recovery semantics. Installer/workspace/CLI/edge/other product DOX stay
  unchanged because their contracts and child indices are unaffected; the Vaultwarden-only
  decision is already recorded in both installer and shared workspace roots.
  Next: build the bridge's durable ordered audit relay and prove lost-response/restart
  delivery against this native receiver, then failure-alert delivery and full KyIdentity
  connector/profile/lifecycle composition. Neither `kyoffboard` nor `kypulse-send` uses the
  receipt protocol yet. Remaining product adapters, Vaultwarden admission/administrator
  separation, guided deployment, recovery/bulk restore and provenance remain unfinished.
  No live service, production catalog entry, commit, PR or release changed; goal stays active.
- BookStack native account provisioning is implemented in `module/accounts.php` and
  `internal/adapters/bookstack/accounts.go`, pinned with module `0.3.0`. The new protected-file
  `ky.bookstack-account/1` interface supports source probe, create and check. It verifies
  exact configured HTTPS issuer/client ID, external subject from `sub`, groups from
  `roles`, group removal enabled, avatar fetching off (`AVATAR_URL=false` included) and
  token debugging off before creation. The Go client probes source/module compatibility
  before invoking create. Accounts receive a target-generated random password, no initial
  role grant and no invitation. One immutable native setting per account's setup UUID
  records intent/native ID in the same database transaction as the native `UserRepo`
  creation. It never adopts an existing email/subject or changes retained credentials,
  profile, roles or revocation. A lost create response resumes the same receipt-owned
  native identity. Dedicated owner and everyday intent are distinct; everyday inspection
  refuses user/role/settings management authority. Source activation and owner grants are
  explicitly pending. A native post-login hook records source/subject/native-ID and actual
  administrator authority only after verified OIDC login and native group synchronization;
  `VerifyAdmin` requires that proof as well as current authority and active OIDC mode.
  A role alone cannot satisfy readiness. This fixture retains standard auth for its native
  account/CLI tests; signed OIDC is verified separately below.
  The expanded real Go-client fixture passes creation, Unicode/HTML-character intent
  hashing, target-receipt resume, changed-intent/source conflicts, no adoption, pending
  owner grant refusal, deliberate loss of the real committed create reply, exact resume
  and retained offboarded-account refusal to reactivate. The existing native offboarding,
  partial-fence repair, old-session/token denial, content retention and restart cases pass
  afterward. `/tmp/kyq-bookstack-account.log` records the run. PHP lint, Go boundary/race
  tests and installer `make ci` pass (`/tmp/kyq-installer-ci-bookstack-account.log`); a final
  focused race test also checks refusal of an active admin role without OIDC proof.
  `git diff --check` passes and owned containers/networks/volumes are removed. Account
  receipts and OIDC proof records are required native database backup artifacts; fence
  files and module remain required filesystem artifacts. Owning client/module/parent DOX,
  operational README and catalog qualification are current; root/CLI/edge/workspace parent
  docs remain unchanged because their contracts/indexes still cover this boundary.
  Remaining: actual KyIdentity composition, central assignment/admin and
  everyday policy composition, CLI/guided deployment dispatch, protected installation
  ownership, bulk restore of database/module/fences and release provenance. Browser UI
  acceptance remains separate. No production catalog entry or live service changed;
  every supported-product adapter and full acceptance gate remain in the active goal.
- **BookStack signed-OIDC qualification:** module/Go client 0.4.0 now commits a separate
  native setting per issuer/subject assignment in the same transaction as each account and
  ownership receipt. It binds exact source digest, setup UUID and native user ID. Native
  `RegistrationService` resolves only these assignments after token validation and before
  group synchronization. Unknown, rebound or revoked subjects cannot register or receive
  roles; retained-account resume checks both immutable receipts. Existing installations
  and older receipts are not adopted or migrated implicitly.
  `internal/adapters/bookstack/oidc_qualification.py` owns a fresh Docker bridge, MariaDB,
  digest-pinned BookStack container and loopback-published TLS/test-IdP gateway. No app or
  database port is published. TLS and RSA private keys stay in memory; only the public
  certificate and PHP trust configuration are shared. HTTPS certificate/name validation
  remains enabled and BookStack trusts exactly the gateway IP for proxy headers. The
  gateway supplies transport and a disposable signed IdP; native BookStack validates OIDC.
  Real discovery, exact redirect/client, one-use authorization codes and five PKCE S256
  exchanges pass. Accounts begin without seeded administrator roles. Native owner login
  establishes the administrator grant and bound login proof; the Go client's actual
  `VerifyAdmin` accepts it. Everyday login has no management authority; native JSON
  authorization returns 403. A bad RSA signature changes no owner authority. An unassigned
  signed subject neither creates an account nor authorizes an API request. Offboarding
  denies old OIDC web sessions/API tokens and a fresh signed login, while retaining native
  ID, subject, email, roles and owned book content. The Go client verifies the retained,
  disabled everyday receipt and refuses it as an owner administrator.
  `/tmp/kyq-bookstack-signed-oidc.log` records the complete signed fixture; owned resources
  are removed. The expanded standard-auth/native Go regression also passes
  (`/tmp/kyq-bookstack-assignment.log`), including lost create response and restart fences.
  PHP lint, installer `make ci` and `git diff --check` pass
  (`/tmp/kyq-installer-ci-bookstack-assignment.log`). Parent/module/gateway DOX, README and
  catalog qualification are current. Root/CLI/edge/workspace DOX remain unchanged because
  their ownership and contracts still cover this work. Actual KyIdentity deployment,
  dedicated-admin ordinary-workflow separation, central assignment composition, guided
  dispatch, protected installation ownership, restored database/module/fence proof,
  source/build provenance and browser acceptance remain open. No live service, production
  catalog entry, commit, PR or release was changed; the all-products goal remains active.
- **KyIdentity native app-role composition:** isolated `KyIdentity-installer-setup` now
  accepts optional `administratorRole`/`everydayRole` per confidential client. Both omitted
  retains the original ordinary-client contract; everyday only assigns that role;
  administrator only creates management-only access; both assign distinct exact roles to
  the two identities. Names follow the native role grammar and equality ignores case.
  Whole-bundle validation rejects bad/shared role intent before credentials are read.
  The product commits app access, owned roles, principal mappings, receipts and per-change
  audits in the same transaction as account/client creation, using native role-shape,
  token-revision and provisioning follow-ups. Owner role names do not derive from global
  directory authority. Receipt keys are unambiguous `client/role` names. Read-only resume
  verifies native role ownership, sole intended holder, no group mapping, exact declared
  role set, assigned-only/nonlegacy claim policy and required permanent assignments.
  Changed intent/policy/holders and foreign roles fail without repair or adoption; audit
  failure rolls back all setup authority. Retained passwords and old ordinary-only bundle
  receipts are preserved. The interface advertises `app-role-assignments`; installer
  dispatch requires it, validates bounded safe role receipts, strips arbitrary fields and
  rejects creation reports during inspection.
  Full store and command race suites pass (`/tmp/kyq-identity-app-roles.log`, store 249s).
  `internal/oauth/installer_setup_test.go` passes real native PKCE code exchange, signed
  ID-token verification and UserInfo for setup-created owner/everyday accounts, proving
  app-specific roles, no leaked global `role`, and management-only/ordinary-only/unassigned
  issuance denial (`/tmp/kyq-identity-setup-oauth.log`). The rebuilt static product binary
  `/tmp/kyq-identity-app-roles` passes the installer SSH/Docker fixture: five owned account,
  client and role objects created once, read-only verification, resume and credential-free
  reports (`/tmp/kyq-identity-app-roles-ssh.log`). Installer `make ci` passes
  (`/tmp/kyq-installer-ci-identity-app-roles.log`); product `go vet ./...` and both worktrees'
  `git diff --check` pass (`/tmp/kyq-identity-app-roles-vet.log`).
  Product root DOX/setup runbook and installer CLI DOX/guided documentation are current;
  installer root child index now includes the existing third-party adapter DOX boundary.
  Workspace/edge/third-party DOX contracts remain unchanged because their ownership and
  verification scope are unaffected. These native claims are not downstream administrator
  or ordinary-workflow separation proof. Next: compose actual setup-created KyIdentity
  accounts/clients with third-party native OIDC, product-bound role mappings and offboarding;
  groups/SCIM, guided bundle generation, administrator separation, backup/restore and
  release provenance remain open. No live service, commit, PR or release changed.
- **BookStack actual KyIdentity composition:** `oidc_qualification.py` now accepts an
  explicit `KYQ_TEST_BOOKSTACK_IDENTITY_BINARY` for a nonce-owned, unexposed native provider.
  `native_identity.py` creates it in the disposable bridge using the pinned fixture image
  as a runtime base and the static source-built `/tmp/kyq-identity-app-roles` binary.
  Private product data, target credentials and native signing keys occupy a separate 0700
  temporary target. The existing gateway supplies verified TLS only; its issuer-prefix
  native mode forwards to the real discovery, authorization, token, login and CSRF routes.
  No authentication proxy is introduced. KyIdentity trusts only the gateway /32.
  Product `apply-setup` creates/checks/resumes owner/everyday accounts, an assigned-only
  confidential client and distinct `admin`/`editor` role mappings. A second owned account
  pair has no assignment to that client. Native subject IDs from setup receipts, not email
  matching, bind BookStack's protected native account setup. Actual source login,
  discovery/HTTPS/PKCE, signed token validation, exact owner administration, post-login
  proof and Go `VerifyAdmin` pass; everyday management and unassigned issuer access are
  denied. The real unassigned callback exposed an upstream missing-code HTTP 500.
  Community module/Go client 0.5.0 now refuses a missing/empty code with a generic
  `OidcException` and clears PKCE only after the native controller consumes and validates
  state. No token exchange or provider error-body reflection occurs in that branch.
  KyIdentity's real operation-bound password step-up and audited assignment API remove
  everyday access. Fresh OIDC is denied, while the old product session/API token demonstrably
  remain usable before product deprovisioning. Explicit adapter revocation then denies both
  and fresh login, retains native ID/subject/email/roles/content, and passes actual Go-client
  inspection. This verifies component composition, not an automatic resident bridge.
  `/tmp/kyq-bookstack-kyidentity-oidc.log` passes; signed fake-provider invalid-signature and
  five one-use PKCE regressions also pass (`/tmp/kyq-bookstack-signed-oidc.log`). The full
  native standard-auth/Go account/offboarding regression passes, including partial-fence
  repair, lost-create-response resume, binding/privacy refusal and restart persistence
  (`/tmp/kyq-bookstack-native-identity-regression.log`). Installer `make ci` and PHP lint
  pass (`/tmp/kyq-installer-ci-bookstack-native-identity.log`); Python syntax and
  `git diff --check` are verified. All owned containers, networks and temporary target data
  are removed. Owning parent/module/gateway DOX, README and catalog qualification are
  current. Root/workspace/CLI/edge/product DOX are unchanged because their ownership and
  contracts are unaffected. Next: the resident native-API SCIM offboarding bridge and
  compatible product endpoints, remaining third-party adapters, guided bundle/deployment
  wiring, admin ordinary-workflow separation, recovery/bulk restore and release provenance.
  No live service, production catalog entry, commit, PR or release changed; goal remains active.
- **BookStack scoped HTTPS offboarding:** community module/Go receipt version 0.6.0 adds
  `POST /api/kyquickstart/offboard` and `HTTPClient`, providing a native product endpoint
  compatible with a network-only resident bridge. Account provisioning remains on the
  protected container-local command. `KYQ_BOOKSTACK_BRIDGE_TOKEN_FILE` holds exactly one
  target-generated 32-byte lowercase-hex bearer credential in canonical application-owned
  0600 JSON, under protected 0700 ancestors outside public/upload roots. Missing/insecure
  credentials fail closed. HTTPS and the separate token are required; browser cookies and
  native API tokens confer no endpoint authority. Inspection/revocation resolves both the
  immutable account and assignment receipts, then matches exact native ID/issuer/subject.
  The token cannot read content, create/reactivate accounts or grant roles. HTTP input is
  bounded before JSON decoding; route-specific trimming is skipped to preserve bindings.
  Native fences/token removal and generic local auditing are reused. Target credentials,
  database receipts and fence files remain required backup artifacts.
  The Go client verifies TLS, disables environment proxies, refuses redirects, bounds
  requests/responses/timeouts and shares inspect/revoke/verify with the native-command
  adapter. It verifies version, identity/digest, both fences and zero native tokens;
  partial publication is repaired even after tokens are gone. Unknown outcomes require
  inspection before retry. Actual KyIdentity and signed-provider disposable fixtures pass
  the real HTTPS SDK, completed resume and partial-fence repair. Wrong/cookie-only credentials,
  unowned accounts, wrong bindings, oversized input and insecure credential permissions
  are refused without changing inspected state. Scoped-token access to content APIs is
  denied. Old OIDC sessions/tokens/new login are denied after revocation; native identity,
  roles and content remain. Logs: `/tmp/kyq-bookstack-native-http.log`,
  `/tmp/kyq-bookstack-http-signed-regression.log`. Native CLI/account/offboarding regressions
  pass (`/tmp/kyq-bookstack-http-cli-regression.log`), including lost-create-response resume,
  retained state and restart persistence. Installer `make ci` passes
  (`/tmp/kyq-installer-ci-bookstack-http.log`); PHP lint, Python syntax and
  `git diff --check` pass. Disposable resources are removed.
  Adapter/module/parent DOX, module README and catalog qualification are current. Root child
  index duplication is removed; workspace/CLI/edge/product DOX remain unchanged because
  their contracts and ownership are unaffected. Next: resident SCIM dispatch with durable
  bindings/retry/audit, native KyIdentity connector setup, remaining third-party adapters,
  guided deployment, administrator separation, recovery/bulk restore and release provenance.
  No live service, production catalog entry, commit, PR or release changed; goal remains active.
- **KyIdentity native SCIM connector setup:** isolated `KyIdentity-installer-setup` now
  accepts optional `scim: {url, tokenFile}` per client and advertises `scim-connectors`.
  Whole-bundle validation rejects insecure/credential-bearing/query/fragment base URLs
  before credential reads. Tokens are bounded protected target files, 32–8192 visible ASCII
  bytes. The command reuses the running product's existing encryption-key environment or
  private data file; setup and read-only inspection never generate a missing key.
  Native store setup creates a generic connector and attaches it to the same owned OAuth
  app identity, retaining its stable ID, roles and assignments. Only the newly created empty
  connector app is consolidated; general native administrator linking rules remain intact.
  Separate connector ownership binds the app, endpoint and credential digest; tokens are
  encrypted with the product key. A connector can be added after initial account/product
  provisioning without changing client intent. Omission leaves it intact. Changed credentials,
  endpoints, foreign ownership, group-delivery drift, disabled connectors and restored
  provisioning holds fail without repair or adoption; operational `failing` status is retained.
  Creation/link audits, ownership and initial desired-state work commit together. Native
  `admin.system_created` also raises the credential-change alert. Group delivery and scheduled
  reconciliation remain off in this initial setup path; no receiver or bridge is deployed.
  Existing worker retry/fencing protects delivery. The new TLS receiver fixture runs native
  setup and dispatch: exact owner/everyday app roles, unassigned global-admin exclusion,
  assignment loss/expiry and account disable/deletion all pass. Remote users remain present
  but inactive; everyday access loss leaves owner access active. This proves native delivery
  composition, not downstream product revocation (`/tmp/kyq-identity-scim-delivery.log`).
  Native store tests cover later connector addition, stable app/role preservation, encrypted
  token and credential-free receipts, create/check/resume, drift refusal, audit rollback and
  credential alerts. Full store race suite passes in 302s
  (`/tmp/kyq-identity-scim-store-regression.log`). Full sync/OAuth/command race suites pass
  (`/tmp/kyq-identity-scim-runtime-regression.log`); focused protected-file/key/schema command
  tests also pass (`/tmp/kyq-identity-scim-command.log`). Static product binary
  `/tmp/kyq-identity-scim-connectors` passes actual SSH/Docker creation, read-only check and
  resume of six owned resources (`/tmp/kyq-identity-scim-ssh.log`). That fixture deliberately
  uses an unreachable receiver URL; it proves configuration/transport, not delivery.
  Installer dispatch requires the new capability and permits only safe connector receipt
  fields, within the full two-account/32-client bound of 130 objects. Installer `make ci`
  passes (`/tmp/kyq-installer-ci-scim-connectors.log`), product `go vet ./...` passes
  (`/tmp/kyq-identity-scim-vet.log`) and both worktrees pass `git diff --check`.
  Product root DOX/setup runbook and installer CLI DOX/guided docs are current. Workspace,
  installer root, edge and third-party DOX remain unchanged because their ownership and
  contracts are unaffected; child indices need no changes. Owned disposable fixtures are
  removed. Next: resident per-app SCIM endpoints, durable verified native account bindings,
  adapter dispatch/backoff and kyPulse audit/alerts, then protected deployment/connector
  composition. Groups, remaining external adapters, guided bundles, full administrator
  separation, recovery/bulk restore and release provenance remain required. No live service,
  production catalog entry, commit, PR or release changed; the all-products goal stays active.
- **Resident bridge durable state foundation:** new `internal/offboard` owns target-local
  binding/job/audit persistence and revocation scheduling. No SCIM HTTP receiver or runnable
  resident service exists yet; this is a required bridge component, not operational dispatch.
  Caller-verified provisioning supplies exact setup UUID, app/kind, HTTPS product origin,
  HTTPS issuer/subject and native account/source/name values. Initial recognized interface
  kinds are BookStack and Forgejo. One immutable binding per app/issuer/subject refuses
  changed native identity, target or installation ownership. The bridge never selects a
  product account by display name/email and records no credential in this state.
  Storage requires a canonical service-owned 0700 directory with protected ancestors.
  An exclusive directory-inode flock prevents a second writer without a removable lock-file
  pathname. Versioned private 0600 files reject symlinks, unsafe ownership/permissions,
  unsupported fields and oversized input. Immutable bindings/audits publish with no-overwrite
  hard links; mutable jobs publish atomically. File and directory fsync precede success.
  Permission drift fails closed at I/O. Separate files own each account's binding/job and
  each attempt's started/result audit; no shared suite state file or credentials are added.
  Requests persist permanent intent before native calls and preserve original time, attempts
  and completion on retry. Attempts persist their state and immutable started audit before
  calling the supplied inspect/revoke/verify operation. Result audit precedes job completion;
  a completion marker without both started/success proofs is refused. Native error text,
  bodies and credentials never enter diagnostics. Failed/interrupted work survives restart,
  with backoff from attempt completion capped at one hour. Each account's delivery is exclusive;
  state inspection/new duplicate requests stay available during its network call and other
  apps/accounts progress independently. Neither re-binding nor a repeated request reactivates.
  Race tests prove private-path/permission/lease refusal, immutable identity conflicts,
  exact callback binding and audit-before-mutation, failure redaction, restart/resume,
  concurrent duplicate suppression, interrupted-attempt backoff, staging cleanup, completion
  proof and concurrent inspection/other-app independence. Installer `make ci` passes
  (`/tmp/kyq-offboard-state-ci.log`); Darwin cross-compilation passes
  (`/tmp/kyq-offboard-darwin-build.log`), which is not a macOS runtime/durability test.
  `git diff --check` passes. New owning DOX and root child index are current. Product,
  workspace, CLI, edge and third-party DOX remain unchanged because this component changes
  none of their existing contracts. Only temporary local state is used; no live resources,
  production catalog entry, commit, PR or release changed.
  Native source inspection confirms `internal/sync/delivery.go` fences lost/asynchronous,
  408 and server-error mutation outcomes; successful resource observation alone does not
  clear that fence. The next transport step must preserve truthful downstream completion
  and these protections, rather than acknowledge a queued job as completed offboarding.
  Still required: complete app-scoped SCIM lifecycle composition, bounded resident scheduling,
  kyPulse outbox delivery/alerts, protected runtime
  configuration/credentials, recovery/restore proof and deployment/connector wiring. Groups,
  remaining products/external adapters and full installer acceptance retain the original scope;
  the all-products goal remains active.
- **Native adapter queue composition:** `internal/offboard/target.go` connects the durable
  store to the actual BookStack scoped HTTPS and Forgejo native HTTPS SDKs. Protected target
  configuration pins app, kind, bare HTTPS origin, issuer and Forgejo source ID. Source setup
  must already have verified that Forgejo source against its issuer; a directory event cannot
  choose a source, target or native account. Registration performs native exact-account
  inspection before publishing an immutable binding. Delivery reads the retained binding,
  refuses foreign scope before any network call, and uses the SDK's inspect/revoke/verify
  path before recording completion. Credentials remain in the native client's memory.
  Supported transport configuration is unchanged: verified TLS, no environment proxy or
  redirect, bounded replies and timeouts. No account creation, grant or reactivation is added.
  Race-tested TLS wire fixtures cover both actual SDK paths: wrong native identity publishes
  no state, foreign app/origin/issuer/source scope performs no request, a native disable with
  failed response stays pending, and restart resumes inspection/token removal or partial
  fence repair. Completed retries perform no native mutation. Private errors and credentials
  remain absent from all binding/job/audit files. Invalid target configuration is refused.
  Actual disposable BookStack qualification passes with real KyIdentity and the queue flag
  (`/tmp/kyq-bookstack-native-queue.log`): signed native login, owner grant/everyday denial,
  assignment-removal refusal, native registration, interrupted partial publication, store
  restart, fence repair, completed retry and old web/API/fresh OIDC denial with retained
  identity/roles/content. Actual pinned Forgejo qualification passes with its signed test IdP
  and queue flag (`/tmp/kyq-forgejo-native-queue.log`): native registration, interrupted result,
  store restart, inspected completion, 101 token removals, old/new session denial, retained
  repository and unchanged owner administration. Its separate queue-test process accepts
  only its labeled pinned fixture and loopback TLS endpoint; fixture credentials/public trust
  stay in process memory. This Forgejo fixture does not prove actual KyIdentity composition.
  These interrupted-result tests deliberately return failure after native success; they prove
  recovery from that local outcome, not a real dropped network response. TLS wire tests also
  exercise failed HTTP responses after mutation. No test clears KyIdentity's uncertainty fence.
  Final installer `make ci` passes (`/tmp/kyq-offboard-target-ci.log`); both native fixture
  processes are terminal successful and remove their owned resources. `git diff --check`
  passes. Owning offboard/BookStack/Forgejo DOX are updated. Parent/root/workspace, CLI, edge
  and other product DOX remain unchanged because ownership, indices and their contracts did
  not change. No live service, production catalog entry, commit, PR or release changed.
  Still required: complete app-scoped SCIM lifecycle composition with truthful native completion,
  bounded resident scheduler/service, protected runtime credentials, kyPulse audit/alerts,
  deployment/connector composition, restore and full source-lifecycle qualification. Other
  product/external adapters, guided bundles, groups, administrator workflow separation,
  recovery/bulk restore and release provenance remain in the all-products goal, which stays
  active. The preceding choice-confirmation turn added no state; this turn resumes concrete
  implementation and native verification rather than treating that acknowledgment as progress.
- **Authenticated SCIM revocation receiver:** `internal/offboard/scim.go` exposes an
  app-scoped revocation projection at `/scim/<app>/Users`. Each receiver uses a separate
  32-byte bearer credential, accepts direct TLS 1.2+ and exactly one Authorization header,
  and refuses cookie-only/forwarded-TLS/browser-origin authority. Protected target scope and
  retained bindings select native identities; directory inputs never choose targets or
  native account IDs. Lookup requires exact externalId filtering and bounded sender pagination.
  Unknown registrations/missing bindings return conflict rather than an empty/404 result that
  KyIdentity could misinterpret as completed disable. Responses contain no native error text,
  credential or request reflection, and use no-store/nosniff.
  Only one exact `replace active=false` PATCH is supported. Input is bounded to 4096 bytes,
  duplicate keys (case aliases included) are refused, and schemas/operations are narrowed
  before publishing intent. Activation, profile/role writes, creation and content deletion
  are explicitly unsupported. These endpoints are not a complete generic SCIM lifecycle
  connector and do not qualify any app for installation. Directory names are opaque subjects;
  active stays conservatively true while access revocation lacks completed durable proof,
  including native partial state. Only complete started/result audit proof supplies false.
  Four synchronous native calls per app and a five-second callback deadline bound request
  work inside KyIdentity's 20-second delivery budget. Pending intent survives slot exhaustion,
  failed native responses and deadlines, returning 429 with Retry-After. Completed native
  inspection/revocation/verification alone returns 204; unsafe/unreadable state returns 503.
  No 202 or queued-as-success shortcut is added. A received pending response is an explicit
  retry outcome; a lost/408/server-error response retains the sender's existing uncertainty
  fence. No code clears that fence from a successful resource observation.
  Race tests prove TLS/bearer/scope refusal, malformed/ambiguous/oversized input rejection,
  no intent/native mutation from refused requests, pending status and retry/backoff,
  restart/completion, immutable proof loss, no completed replay, safe diagnostics and bounded
  concurrency that preserves an unattempted fifth intent. A real deadline test leaves a
  canceled native attempt pending within seven seconds.
  Actual BookStack native qualification passes with real KyIdentity provider and both queue/
  SCIM flags (`/tmp/kyq-bookstack-native-scim.log`): pending 429/active, actual 30-second backoff,
  restart, native partial-fence repair via authenticated TLS PATCH, verified 204/inactive,
  completed retries and retained identity/roles/content with old web/API/fresh OIDC denial.
  Actual pinned Forgejo fixture passes with its signed test provider and both flags
  (`/tmp/kyq-forgejo-native-scim.log`): the same real-backoff TLS receiver path after interrupted
  native completion, 101 token removals, old/new session denial, retained repository and
  unchanged owner authority. These are HTTP-client receiver tests: the KyIdentity SCIM engine
  did not deliver these events, and Forgejo still lacks actual KyIdentity composition.
  Final installer `make ci` passes (`/tmp/kyq-offboard-scim-ci.log`) and `git diff --check`
  passes. Both native fixture processes are terminal successful and clean their owned
  temporary containers/state; no native test remains running.
  Owning offboard/BookStack/Forgejo DOX, installer root contract, architecture and catalog
  qualification docs are current. Parent adapter/workspace/CLI/edge/other product DOX remain
  unchanged because their ownership, indices and contracts are unaffected. No live services,
  production catalog entry, commit, PR or release changed. The all-products goal stays active.
  Next: private runtime/service configuration, kyPulse audit
  delivery, full native lifecycle/profile handling and actual KyIdentity connector composition,
  then protected deployment/restore qualification. All other product/external, guided/group,
  administrator-separation, recovery/bulk restore and provenance gates remain in scope.
- **Independent resident retry worker:** `internal/offboard/worker.go` now resumes due native
  revocation jobs without a further SCIM mutation. One worker per opened store owns its
  streaming directory cursor; a second local worker is refused, in addition to the existing
  cross-process store lease. Up to 32 distinct configured apps and four background native
  deliveries total are supported. Each delivery still uses exact protected target scope,
  retained native binding, per-account exclusion and the SDK's inspect/revoke/verify path.
  Read at most 1024 directory names per 250ms scan without loading the full ledger into RAM.
  Pending/interrupted jobs retain due times across restart; unattempted backlog is untouched
  when slots are full. Completed jobs require durable audit proof and never replay.
  Missing/conflicting target configuration, unsafe storage or corrupt proof stops the worker
  with generic diagnostics, preserving the underlying intents. Parent cancellation cancels
  every native call and joins them before worker return; callers must join before store close.
  HTTP receiver calls retain their separate per-app limit, rather than sharing this background
  semaphore. Flat-directory scan latency grows with retained audit files; separating a jobs
  directory is the documented upgrade if measured deployment growth exceeds its budget.
  Worker race tests prove future-backoff persistence across store/worker restart, eventual
  completion without another request, completed suppression, single-worker refusal, four-call
  bound, joined cancellation and retained unattempted backlog. Configuration/permission/proof
  damage causes no native mutation. A streaming fixture with over 1024 existing entries finds
  an intent published after worker start without losing or replaying it.
  Real pinned BookStack fixture passes with actual KyIdentity provider, queue and worker flags
  (`/tmp/kyq-bookstack-native-worker.log`): after interrupted native publication and store
  restart, the worker preserves actual 30-second backoff, repairs the native partial fence,
  records completion and exposes inactive through authenticated TLS GET. No subsequent SCIM
  mutation triggers the repair. Original owner/everyday/assignment checks and retained
  identity/roles/content with old web/API/fresh OIDC denial still pass. Real pinned Forgejo
  fixture passes with its signed test IdP, queue and worker flags
  (`/tmp/kyq-forgejo-native-worker.log`): the independent worker resumes after real backoff,
  verifies retained native completion, and the original 101-token/session/repository and
  unrelated-owner checks pass. Neither fixture proves KyIdentity SCIM event production;
  Forgejo still lacks actual KyIdentity provider qualification. No production service executable
  or deployment is added by this library worker.
  Final installer `make ci` passes (`/tmp/kyq-offboard-worker-ci.log`), including all new race
  tests; both native fixture processes are terminal successful and clean owned resources.
  `git diff --check` passes. Offboard/BookStack/Forgejo owning DOX and architecture/catalog
  readiness wording are updated. Root/parent adapter/workspace/CLI/edge/other product DOX
  remain unchanged because their ownership, indices and contracts are unaffected. No live
  services, production catalog entry, commit, PR or release changed. The all-products goal
  remains active. Next: kyPulse audit delivery/alerts and full connector/lifecycle composition,
  kyPulse audit delivery/alerts, full native profile/lifecycle and actual connector composition,
  followed by protected deployment/restore. Remaining product/external adapters, groups,
  guided bundles, administrator separation, recovery/bulk restore and provenance stay in scope.
- **Protected runtime and standalone command:** `internal/offboard/runtime.go` now loads
  `ky.offboarding-runtime/1` JSON with one installation UUID, an explicit local IP/nonzero
  listener port, separate private configuration/state directories, PEM server material and
  1–32 configured app entries. Shared private-root/file readers preserve the existing state
  lease and permission checks while allowing read-only configuration loading without a lease.
  The entire public intent is narrowed before credentials. App names, canonical origin
  authorities and referenced files are unique; case/default-port/trailing-dot origin aliases
  cannot create two owners. Only relative private basenames are accepted, never inline tokens.
  Canonical service-owned 0700 directories and protected ancestors contain service-owned 0600
  regular files; symlinks, permission drift, oversized data, duplicate JSON fields, unknown kinds
  and overlapping configuration/state are refused. Configuration is capped at 128 KiB and
  credential/TLS files at 16 KiB. Native product and SCIM credentials are distinct across all
  app authorities; raw files allow at most one final newline. BookStack/SCIM tokens use exact
  lowercase 32-byte hex; Forgejo uses its native token. No credential is generated, replaced
  or saved in installer workstation state. Runtime target bindings also require the exact
  installation UUID, without weakening existing app/issuer/native-source checks.
  Server certificate/key must match an unexpired server-auth leaf; CA private keys are
  refused. Native outbound clients retain verified standard TLS trust and their existing
  redirect/proxy refusal. Public trust distribution remains a deployment acceptance gate.
  `Runtime.Serve` owns the exclusive state lease, direct TLS 1.2+ listener, bounded HTTP
  receiver and independent retry worker. Cancellation closes the handler admission gate,
  cancels/joins native calls and HTTP handlers, and releases listener/state only after work
  stops. The explicit handler join also covers forced socket closure after shutdown timeout.
  Runtime/configuration errors are generic; JSON and normal pointer/value formatted reports
  reveal no token/key/body. `cmd/kyoffboard` provides `--config <private file>` and read-only
  `--check`, with safe schema/status/app-count output, 0/1/2 exit semantics and signal handling.
  `make build` now builds both `kyquickstart` and `kyoffboard`; generated binaries are ignored.
  Runtime tests prove file/permission/symlink/intent/credential/certificate refusal, duplicate
  authority aliases, installation separation, key mismatch/expiry/CA/client-only refusal,
  no native requests or state writes during load/actual command check, report redaction,
  verified TLS native SDK wire dispatch, state exclusion, listener release and joined active
  HTTP/native cancellation. A Linux fixture builds/runs the actual binary with public fixture
  certificate trust, verifies wire completion and SIGTERM exit 0 with retained proof/lease
  release. This process fixture uses a native-protocol HTTP stand-in, not a full product.
  Actual pinned BookStack runtime qualification passes with its real KyIdentity provider
  (`/tmp/kyq-bookstack-native-runtime.log`): protected fixture configuration/material, actual
  30-second backoff, native partial repair through the service worker, observer-only TLS GET,
  joined shutdown and reopened completion proof. Original owner/everyday/assignment checks,
  retained identity/roles/content and old web/API/fresh OIDC denial pass. Actual pinned Forgejo
  qualification passes with its signed test IdP (`/tmp/kyq-forgejo-native-runtime.log`), including
  runtime restart/backoff/verification, 101 tokens, retained repository and owner authority.
  These real-product runtime tests run the serving library; actual standalone-binary lifecycle
  is separately proved by the Linux wire fixture. They do not prove KyIdentity SCIM engine
  delivery, actual KyIdentity/Forgejo provider composition, deployed network policy or restore.
  Final `make ci` passes (`/tmp/kyq-offboard-runtime-ci.log`), `make build` passes
  (`/tmp/kyq-offboard-runtime-build.log`) and `git diff --check` passes. All native fixture
  processes are terminal successful and remove owned temporary resources. New command owning
  DOX/root child index, offboard and native fixture DOX, root build/ownership guidance,
  architecture/catalog readiness wording and `docs/offboarding.md` runbook are current.
  Parent adapter/workspace/CLI/edge/other product DOX remain unchanged because their ownership
  and contracts are unaffected. No live services, production catalog entry, commit, PR or
  release changed. Next: kyPulse outbox delivery/alerts, full native lifecycle/profile handling
  and actual connector composition, then protected installer deployment and sealed restore.
  Other products/external adapters, groups, guided bundles, administrator separation,
  recovery/bulk restore and provenance remain in the active all-products goal.
- **Resident kyPulse audit relay:** `internal/offboard/audit.go` now owns one ordered relay
  per private store, independent of native revocation workers. It authenticates a capability
  OPTIONS request before any POST and pins kyPulse HTTPS origin, native source UUID and the
  installation UUID. Native kyPulse gained that authenticated, quota-bound capability route
  without exposing it through browser CORS preflight or expanding collected-data access.
  Shared source authentication now refuses duplicate Authorization headers and invalid token
  characters. Capability checks create no collected rows and revoked credentials are refused.
  The relay projects immutable attempt audits into recognized imported Activity: fixed
  actions/outcomes, local audit hashes and app/binding digests, with no subject, native error
  or token. It validates target scope, persists exact NDJSON/body digest and assigned sequence
  before sending, verifies the native receipt, then publishes an immutable per-event `.sent`
  file before advancing `pulse-state.json`. A lost response retains the same body/sequence and
  durable backoff; a receipt published before an interrupted head advance completes locally
  without I/O. Started phases precede results. One owner writes relay state; product workers
  retain separate audit/job ownership. Bounded streaming scans verify/count retained receipts
  before assigning a new sequence, refusing missing historical proofs or a rewound head
  before replaying old events. State JSON now rejects duplicate as well as unknown fields.
  Verified HTTPS, no proxy/redirects, 15-second calls and 8-KiB response headers bound the
  transport. Transient failures retain pending work; bounded Retry-After is honored and
  backoff caps at one hour. Unsupported protocol, changed source/destination/installation,
  invalid acknowledgment, authentication/sequence conflict or unsafe state fail closed.
  Protected runtime configuration accepts optional `audit:{origin,token_file}`; its private
  credential is distinct from all product/SCIM authority. Loading remains read-only, with all
  public intent validated before credentials and no native readiness requests. `Runtime.Serve`
  starts and cancels/joins the relay alongside HTTP/native workers before releasing state.
  Optional omission remains a local-development path, not an operational admission claim.
  Tests cover lost reply/restart, durable pacing/Retry-After, receipt-before-head crash repair,
  phase ordering, single-writer refusal, missing historical proof, old server, redirects,
  changed source, bad/double receipt headers, safe diagnostics, protected credentials and
  joined cancellation with retained pending intent. Native kyPulse fixtures exercise its
  actual API/store on both SQLite and an owned disposable PostgreSQL 17: commit then lose
  the response socket, restart via the actual `kyoffboard` executable with protected target
  configuration and standard public fixture trust, preserve real 30-second backoff, verify
  exactly two native log/activity rows from three POSTs, then SIGTERM exit 0 and lease release.
  These prove native ingestion and executable restart, not actual KyIdentity event production,
  product mutation, deployed network policy, archival or restore. Native failure-alert
  qualification is recorded in the following milestone.
  Final installer `make ci` passes (`/tmp/kyq-audit-relay-ci.log`) and `make build` passes
  (`/tmp/kyq-audit-relay-build.log`). Native fixtures pass
  (`/tmp/kyq-audit-relay-native.log`, `/tmp/kyq-audit-relay-postgres.log`); the complete native
  Go race suite and vet pass (`/tmp/kyq-pulse-capability-all.log`,
  `/tmp/kyq-pulse-capability-vet.log`). Formatting and `git diff --check` pass; all test
  processes are terminal and owned PostgreSQL resources are removed. Native full `make ci`
  still has the recorded unpublished shared `applysetup` tidy dependency; product checks
  use `/tmp/kyq-adapters.work`. No module changes conceal that gate.
  Offboard/native API and root DOX, README/runbook, architecture/catalog readiness wording
  are current. Installer/workspace/CLI/edge/adapter/other product DOX and child indices remain
  unchanged because their contracts/ownership are unaffected. No live services, production
  catalog entries, commit, PR or release changed. Remaining product/external adapters,
  full profile/lifecycle/group composition, alert deployment, guided deployment, administrator
  separation, recovery/bulk restore and provenance remain in the active all-products goal.
- **Native offboarding failure alerts:** kyPulse migration 11 adds a default-off,
  administrator-owned alert grant to pairing codes and claimed log sources. Admin pairing
  accepts `offboarding_alerts:true` only with an existing watched target. Claim JSON cannot
  alter the grant or target. Authenticated non-browser OPTIONS advertises
  `Ky-Log-Alerts: ky.offboarding-alerts/1` only for granted sources with retained target scope;
  ordinary source tokens still cannot read logs, administer sources or create failure alerts.
  Granted sources require one sequenced log/activity kyoffboard projection with fixed
  actor/actions/outcomes and app/binding digest labels. Bad shape, unsequenced writes and
  lost target scope refuse without acknowledgment. A failed result inserts one native
  `offboarding_failed` Alerts event with pending notification in the same transaction as
  receipt, payload, usage and pruning. Concurrent exact replay creates one alert; a failed
  alert insert rolls back the receipt and payload. Started/success outcomes create no alert.
  The target's health track is untouched. Target deletion removes its alerts and the source
  cannot silently continue accepting alert-bearing writes without a target.
  The existing serial kyPulse sender checks one indexed durable pending failure each second,
  independent of the health queue, including after restart. It honors timed/until-fixed
  silence and disabled targets; explicit terminal statuses are `silenced`, `paused` and
  `unconfigured` when no webhook exists. Delivery reuses native retries, safe reasons,
  webhook status and keyed alert audit. Cancellation retains pending notification for restart.
  Recorded success and receipt replay do not resend; a crash after external webhook success
  but before recording its outcome may repeat the webhook. Run one monitor service per
  database; this is not an exactly-once external notification promise. SQLite recovery
  snapshots retain source grants and alert events/pending delivery while excluding collected
  payloads; original receipt replay cannot duplicate or repopulate them.
  Protected resident configuration now supports `audit.require_alerts:true`, requiring one
  exact capability header before any audit POST/state initialization. Plain sources and
  missing/duplicate headers are refused before mutation. False/omission remains a disposable
  ingestion-only development path, not operational qualification. Native fixture handoff uses
  the actual private immutable failed-attempt outbox and verified TLS source capability,
  ingests started/failed Activity through the real API/store, starts a fresh monitor against
  the retained event, verifies webhook delivery and replays the final receipt without another
  event, payload row or recorded successful webhook. It runs on SQLite and disposable
  PostgreSQL 17; webhook transport and product revocation failure are fixture stand-ins.
  This proves native failure integration, not deployed product failures, production webhook
  connectivity, KyIdentity lifecycle delivery, network policy or complete recovery.
  Installer `make ci` passes (`/tmp/kyq-offboard-alert-installer-ci.log`) and `make build`
  passes (`/tmp/kyq-offboard-alert-build.log`). Native complete Go race suite and vet pass
  (`/tmp/kyq-offboard-alert-all.log`, `/tmp/kyq-offboard-alert-vet.log`). PostgreSQL component
  checks pass (`/tmp/kyq-offboard-alert-postgres.log`); native combined fixtures pass
  (`/tmp/kyq-offboard-alert-native.log`, `/tmp/kyq-offboard-alert-native-postgres.log`).
  All processes are terminal successful and owned PostgreSQL containers are removed.
  Native full `make ci` retains the unpublished shared applysetup tidy gate; the explicit
  product workspace remains `/tmp/kyq-adapters.work`, with no module changes to conceal it.
  Native root/API/store/monitor/backup DOX, README and installer offboard DOX/runbook/catalog
  readiness are current. Child indices and installer/workspace/CLI/edge/adapter/alerts/other
  product DOX remain unchanged because their ownership/contracts are unaffected. No live
  services, production catalog entry, commit, PR or release changed. Next: native kyPulse
  protected installer composition, complete
  KyIdentity active-profile/group/connector lifecycle, Vaultwarden administrator separation,
  other product/external acceptance, guided deployment, recovery/bulk restore and provenance.
- **kyPulse native monitoring enrollment:** the product-specific `monitoring` extension
  now accepts at most 32 keyed health targets and 32 keyed log sources referencing those
  targets. Public intent validates before credential reads; target URL policy uses native
  egress rules, names/keys are bounded and distinct, and the configured kyPulse self origin
  is refused. Each source reads a private canonical target file containing a distinct
  lowercase 32-byte hex token; active SSO authority cannot be reused. Prepared hashes are
  private Go fields omitted from bundle serialization. Only the native source token hash
  and protected file references enter durable data; no token is returned in setup reports.
  Product API/private IPC, CLI capability discovery and product-owned SQL transaction now
  support enrollment. The common envelope's SSO/accounts/configuration and keyed setup audit
  remain atomic with target/source creation. Stable setup/key UUIDs and retained ownership
  definitions refuse foreign names/token hashes, different setup identity, changed intent,
  revoked sources and missing/drifted owned objects. Resume preserves health, silence,
  ingestion receipt state, collected data and credentials; omitted objects are not deleted.
  Inspection succeeds with writes actually disabled on both SQLite and PostgreSQL, rather
  than relying only on unchanged counters. Monitoring activates live without adding a restart;
  SSO/backup configuration retains its existing graceful activation contract. Configuration
  receipts carry `monitoring.target.<key>` / `monitoring.source.<key>` names and native IDs.
  Source setup can grant target-bound offboarding alerts, with the same ingestion-only
  native authority and capability as administrator pairing; no public setup route is added.
  Installer kyPulse discovery now requires `monitoring-targets`, `log-sources` and
  `offboarding-alerts` before calling setup. Unit checks prove missing capabilities never
  reach mutation and private report extras cannot escape. Native IPC qualification enrolls
  the graph, checks/resumes without duplicates, authenticates the resulting source, creates
  an actual failure alert through native ingestion and verifies enrollment still inspects.
  Ownership tests prove a foreign source conflict rolls back a candidate administrator,
  target, ownership and audit, preserves foreign credentials, and never reactivates revoked
  sources or silently repairs target/definition/token drift. Protected credential tests cover
  unknown references, duplicate names, self origin, invalid targets/intervals, public files,
  symlinks, malformed tokens, duplicate credentials and active-SSO credential reuse.
  The actual SSH/container fixture passes (`/tmp/kyq-pulse-enrollment-ssh.log`): five created
  objects (runtime configuration, two identities, target and source), exactly one graceful
  restart, present-only check/resume and native source alert capability. It uses a rebuilt
  static local product binary and owned disposable containers. Health URLs and identity
  provider are fixture references, so this does not prove production polling, real KyIdentity
  provider composition, protected cross-target credential distribution or deployed alerts.
  Product source token sealing is covered by the credential-backup milestone below;
  bridge copies and target placement still require suite restore qualification.
  Native hashes alone cannot reconstruct caller credentials. KyYard/recovery enrollment,
  full guided composition, webhook deployment and complete suite acceptance remain open.
  Installer final `make ci` and `make build` pass
  (`/tmp/kyq-pulse-enrollment-final-ci.log`, `/tmp/kyq-pulse-enrollment-build.log`). Native
  complete Go race suite passes (`/tmp/kyq-pulse-enrollment-all.log`); final context-dependent
  boundaries and read-only checks pass (`/tmp/kyq-pulse-enrollment-final-boundaries.log`,
  `/tmp/kyq-pulse-enrollment-readonly.log`). Native vet passes
  (`/tmp/kyq-pulse-enrollment-vet.log`); disposable PostgreSQL checks pass
  (`/tmp/kyq-pulse-enrollment-postgres.log`, `/tmp/kyq-pulse-enrollment-final-postgres.log`).
  Native full `make ci` retains the unpublished applysetup dependency gate; no module edits
  hide it. DOX pass updates native root/API/config/store/backup and installer CLI contracts, native
  setup runbook, guided documentation and this ledger. No child index changes; workspace,
  installer root/edge/adapter/offboard, native monitor/alerts and other product DOX
  remain unchanged because their ownership/contracts are unaffected. No live deployments,
  production catalog entries, commit, PR or release changed. All-product scope remains
  active: protected graph/credential composition, KyYard and recovery enrollment, complete
  Identity lifecycle/groups and app acceptance, Vaultwarden administrator separation, guided
  deployment, secret/bulk restore and source/build provenance.
- **kyPulse installer source credential backup:** active installer-owned source tokens now
  enter only the private sealed capsule member `config/installer-source-credentials.json`
  (0600, maximum 1 MiB). Ownership, native IDs/target grants and expected token hashes come
  from the immutable sanitized SQLite snapshot. Protected file reads and exact credential
  matching refuse missing, changed, public or symlinked files. Revoked sources retain their
  revocation and require no token. Public backup metadata and setup reports carry no token.
  Drills check every active owned binding, even when a recipe omits the credential member;
  older capsules without active owned sources remain drillable. The actual shared-library
  sealed drill restores a collected token after its original file is deleted, authenticates
  the restored native source, and proves no write to the recorded absolute source path.
  Inconsistent and omitted credential-member drills fail. Target credential placement,
  bridge-side copies/receipts and complete suite recovery remain unqualified; extraction
  is confined to the private restore directory. Native backup race checks pass
  (`/tmp/kyq-pulse-source-backup.log`), complete Go race suite passes
  (`/tmp/kyq-pulse-source-backup-all.log`), vet passes
  (`/tmp/kyq-pulse-source-backup-vet.log`) and the rebuilt static binary is
  `/tmp/kyq-pulse-source-backup-binary`. Native full `make ci` retains the unpublished
  shared applysetup dependency gate; no module change conceals it. No installer Go code
  changed, so its previously passing CI/build were not repeated. DOX updates the native
  backup contract and product restore/setup runbooks; installer guided documentation and
  this ledger are current. Parent ownership and child indices are unchanged, so workspace,
  installer root/CLI/edge/adapters/offboard and other native/product contracts are left
  unchanged. No live services, production catalog entry, commit, PR or release changed.
  Next: complete protected graph/credential composition, Identity profile/group lifecycle,
  Vaultwarden administration separation, KyYard/recovery enrollment, guided deployment,
  cross-target secret/bulk restore and source/build provenance. All-product scope stays active.
- **Guided and unattended native integration selections:** `stack.yaml.integrations` now
  separately selects each declared product/target, container, protected target-local bundle
  and optional Kubernetes namespace/pod. Boundary validation rejects undeclared/wrong-target
  products, duplicate products/shared containers, inline credential fields and invalid paths
  or workload names. Non-demo planning offers a separate setup choice (default no), persists
  explicit selections, reconstructs them from the published plan on resume and removes only
  intent when declined. Demo planning does not offer real-product setup by default. Review
  discloses configuration intent and separate commands; planning/deploy actions and `apply`
  retain their existing scope. `integrate-stack [--check]` validates every selected native
  adapter before connecting, runs KyIdentity first then saved order, and reuses the existing
  native transport/target lock/capability/activation/live-check path. Sanitized native receipts
  are emitted with product/target context; interactive host-key prompts stay visible instead
  of being captured as report bytes. Failure/cancellation stops the remaining plan without
  reversing successful setup; native ownership supports resume. Focused final race boundaries
  pass (`/tmp/kyq-saved-integrations-final-boundaries.log`), including unsupported later
  adapters and cancellation before connections. Installer `make ci` and `make build` pass
  (`/tmp/kyq-saved-integrations-ci.log`, `/tmp/kyq-saved-integrations-build.log`); final
  e2e-tag vet passes (`/tmp/kyq-saved-integrations-final-vet.log`). Actual disposable SSH
  fixture passes (`/tmp/kyq-saved-integrations-final-ssh.log`): saved kyPulse setup creates
  five objects, activates with one restart, emits its verified receipt before a deliberately
  absent later DNS container fails, then inspects/resumes without duplicate setup or restart
  and verifies native source alert capability. Only owned fixture containers are used and
  removed. This tests saved-plan routing and partial failure, not complete KyIdentity/provider
  composition or all-product deployment. Automatic target bundle construction, protected
  graph/credential distribution, external-app saved dispatch, full lifecycle/admin acceptance,
  KyYard/recovery enrollment, secret/bulk restore and image provenance remain open.
  DOX updates root schema and child CLI contracts plus the architecture command table and
  guided runbook. Child indices and workspace/product/edge/adapter/offboard docs are unchanged
  because their ownership/contracts are unaffected. No live deployments, production catalog
  entry, commit, PR or release changed. The all-products goal remains active.
- Vaultwarden, other admitted external apps, guided composition and complete per-product
  acceptance remain unfinished. Vaultwarden upstream commit
  `df2cd3c8693782eeedfd1f70318f6f14033213a8` was checked: native OIDC/PKCE and disable/deauth
  APIs exist, but instance administration checks its own admin token/cookie. The catalog spec
  records its selected-password status and native KyIdentity administration/dedicated-admin
  separation gate, with pinned primary-source links. Do not replace that gate with an auth
  proxy or count SSO alone as admin access. No live services have changed. The all-products
  goal remains active.

## Current checkpoint: Forgejo profiles and actual KyIdentity

The native Forgejo adapter now updates directory-owned display name and primary email on an
exact pinned account/source/subject. Native profile responses must include both fields before
mutation. Writes contain only those two fields; verification requires the desired values and
unchanged administrator/active/prohibited state. Disabled accounts remain disabled. A lost
reply is an error; resume inspects the desired profile before writing again. Boundary race
tests cover invalid inputs, missing fields, conflicting source, native profile/authority drift
and a committed write with a lost socket reply followed by a no-write retry.

The pinned Forgejo 16.0.5 fixture now accepts `KYQ_TEST_FORGEJO_IDENTITY_BINARY` for the actual
KyIdentity build. Its own labeled container uses a private temporary target, loopback-only
published port and exact Docker gateway /32 proxy trust. Product-owned setup creates, checks
and resumes two independently owned admin/everyday pairs and an assigned-only client with
app roles. Native subject receipts bind the Forgejo accounts. Real discovery, signed tokens,
PKCE and subject-bound owner/everyday/unassigned authorization pass. Source configuration
uses native required claim `aud` equal to the client ID plus app-scoped `roles`; the synthetic
test provider's `kyq_assigned` claim is not required from KyIdentity.

Actual operation-bound native step-up removes the everyday app assignment: fresh OIDC
issuance is denied while retained product tokens remain usable until explicit offboarding.
Native owner app-role removal removes Forgejo administration after the next OIDC sign-in,
including access through the old product session. This does not prove immediate role
revocation before that sign-in. Owner/everyday profile changes, repeats, disabled-account
updates, 101 personal-token revocations, old session denial and repository retention pass.

Installer `make ci` and `make build` pass. The actual KyIdentity fixture also passes combined
with `KYQ_TEST_FORGEJO_QUEUE=1 KYQ_TEST_FORGEJO_RUNTIME=1`: protected runtime loading, exact
native binding, interrupted delivery, restart, actual retry backoff and resident revocation
complete before native access-denial/content checks. Fixture cleanup leaves no owned identity
container. Evidence: `/tmp/kyq-forgejo-profile-boundaries.log`,
`/tmp/kyq-forgejo-profile-native.log`, `/tmp/kyq-forgejo-native-identity-profile-final.log`,
`/tmp/kyq-forgejo-native-identity-runtime.log`, `/tmp/kyq-forgejo-profile-installer-ci.log`,
and `/tmp/kyq-forgejo-profile-build.log`.

The SCIM receiver still exposes revocation only: it does not deliver these profile updates
or create accounts, and these fixtures do not prove KyIdentity's SCIM worker emitted the
revocation. Full active-account/profile/group delivery, immediate lifecycle authority changes,
dedicated-admin ordinary-workflow separation, remaining external adapters, automatic guided
bundle/credential composition, enrollment, restore and release/image gates remain unfinished.
Production catalog entries remain gated; no live deployment, commit, PR or release changed.

DOX updates the Forgejo owner's contract and fixture limits, the catalog's native qualification
and offboarding runbook. Root ownership, sibling contracts and child indices are intentionally
unchanged because this adds no new boundary or deployment authority. The all-product goal
remains active; the next integration work is the bridge's missing active lifecycle.

## Current checkpoint: registered Forgejo directory delivery

Explicit protected Forgejo runtime configuration now enables active SCIM User replacement
on previously registered native bindings. Its `directory` object pins distinct administrator
and everyday role names alongside source/issuer/installation scope. Default configuration
retains revocation-only behavior; other adapters reject the object before credentials.
Source setup must verify that mapping against the owned native authentication source.

The full active PUT is bounded to 8 KiB, rejects ambiguous/invalid UTF-8 JSON, unknown fields,
duplicate keys/roles, foreign subjects, renamed usernames and unsupported role/name shapes.
It narrows one display name, primary email and administrator projection; empty roles remove
administrator authority. Native Forgejo writes only profile fields and the administrator flag,
never activity/prohibited/source/subject/password fields. Disabled accounts refuse this lane.
GET reports the inspected native profile/role; the configured ordinary-role alias represents
Forgejo's non-administrator state. No native inactive flag alone proves completed offboarding.

Each account owns a private `.directory` intent record and distinct immutable local audit
files, with started proof before I/O and result proof before completion. The profile and
revocation lanes serialize the native account's mutation, while their state/audit files remain
separate. Known native refusals permit a new audited attempt. A started/interrupted or
uncertain native write holds later directory changes across restart and returns 503 rather
than releasing KyIdentity's uncertainty fence through pending acceptance. Read-back does not
clear it. Existing irreversible revocation remains available and refuses subsequent active
updates. Native outcome reconciliation and directory-audit relay are still admission gates.

Boundary race tests pass for narrowed role/profile input, intent and audit ordering, missing
proof/partial publication, known-refusal retry, unknown-write persistence/no replay or
supersession, and shared native-account serialization with revocation. Protected runtime
mapping checks remain read-only. Native SDK tests qualify administrator projection, disabled
refusal and known/unknown outcome classification. Installer `make ci` and `make build` pass;
the full offboard/Forgejo Go race suites pass.

The real pinned Forgejo fixture with actual KyIdentity, queue/runtime and
`KYQ_TEST_FORGEJO_DIRECTORY=1` passes. Its actual native app-role removal is followed by
authenticated SCIM PUT through protected runtime/TLS. Native profile/role replacement,
repeat, live read-back, joined shutdown and durable audit proofs pass; the parent proves
old-session administrator denial before another OIDC login. Directory-mode fixture accounts
use the actual directory usernames, while immutable subject/source/native IDs retain ownership.
The initial runtime fixture failed its private-directory boundary and was corrected to provision
0700 directories; production permission checks were preserved. Owned containers are removed.
Evidence: `/tmp/kyq-directory-final-boundaries.log`, `/tmp/kyq-directory-all.log`,
`/tmp/kyq-directory-native-isolated-final.log`, `/tmp/kyq-directory-native-runtime-final.log`,
`/tmp/kyq-directory-installer-ci.log`, and `/tmp/kyq-directory-build.log`.

This proves authenticated receiver/native delivery, not the KyIdentity worker as producer.
New-account creation, group/rename delivery, held-write reconciliation, directory-audit relay,
BookStack active updates and full central lifecycle remain open. Dedicated-admin workflow
separation, other external adapters/Vaultwarden administration, automatic guided credential
composition, enrollment, restore and release/image qualification retain the original scope.
Production catalog entries remain gated. No live deployments, commit, PR or release changed.

DOX updates the nearest offboard/Forgejo contracts, adapter parent and root child index;
runtime documentation and catalog qualification were reconciled. Other child indices,
workspace preferences and sibling-product/CLI/edge contracts remain unchanged because their
scope and authority are unaffected. The full all-product goal remains active. Next: actual
KyIdentity event production and missing creation/group lifecycle without weakening ownership
or uncertainty fences.

## Current checkpoint: actual KyIdentity worker to Forgejo

`KYQ_TEST_FORGEJO_PRODUCER=1` now qualifies the real native producer against the protected
resident bridge and pinned Forgejo 16.0.5. Native container-local setup atomically creates the
assigned-only client, app roles and owned SCIM connector; target-owned token files and public
fixture trust are provisioned before startup. Actual native receipt subjects bind the two
pre-created Forgejo accounts. The test observes producer acknowledgment/status and increasing
revisions rather than inferring delivery from a product profile alone; unrelated/unassigned
identities are refused in the observed resource set.

Actual initial profile/role delivery passes. An operation-bound native administrator profile
edit reaches Forgejo without another sign-in. Native app-assignment loss automatically removes
all 101 retained personal tokens and denies the existing everyday session while retaining its
repository. Native administrator-role removal automatically denies the old Forgejo admin
session before a fresh OIDC login. The separate race-enabled bridge process loads private
runtime files, verifies both native bindings and serves only the actual worker's mutations.
Bounded stdin shutdown cancels/joins the runtime; reopened private state verifies directory
and revocation completion with immutable audit proofs. Existing interrupted queue/runtime
repair and real retry backoff pass in the same fixture.

The receiver previously rejected KyIdentity's valid role `primary:true` metadata. It now
accepts that boolean while deriving administrator authority only from exact protected role
values; strict field/type/duplicate/unknown-role checks remain. Its boundary regression uses
primary-marked administrator and ordinary roles. No new protocol or dependencies were added.

The initial Docker-gateway topology passed host-side verified TLS reads, but container-to-host
requests timed out under this host's network policy. No firewall rules were changed. Native
KyIdentity now accepts optional literal-IP `KYIDENTITY_LISTEN_HOST`, joined with its existing
port; empty preserves existing all-interface binding. Malformed hostnames/ports/scoped IPs
fail startup. Compose and its environment example expose the option. Producer fixtures first
run an isolated no-network malformed-bind probe to refuse older binaries before host-network
access, then bind identity and bridge only to ephemeral loopback endpoints with exact loopback
/32 proxy trust. Normal non-producer identity fixtures keep their private bridge namespace and
loopback-published port. HTTPS certificate verification remains enabled throughout.

Installer `make ci` and `make build` pass. Native identity config/API/command race tests pass
(the full API package takes about 299 seconds), relevant vet passes, and a fresh static native
binary is `/tmp/kyq-identity-producer-binary`. Native Compose configuration renders, both repos
pass `git diff --check`, and owned fixture containers are removed. The final combined producer,
queue and runtime test passes in 51.754 seconds and includes the native profile-edit proof.
An intermediate fixture asserted an empty initial display name; native setup deliberately uses
the username, so that assertion was corrected against the product-owned setup contract.
Evidence: `/tmp/kyq-producer-boundaries-final.log`, `/tmp/kyq-producer-native-qualified.log`,
`/tmp/kyq-producer-installer-final-ci.log`, `/tmp/kyq-producer-build.log`,
`/tmp/kyq-producer-identity-bind.log`, and `/tmp/kyq-producer-identity-vet.log`.

This closes registered-account native producer composition only. Creation, groups/rename,
uncertain-write reconciliation, directory-audit relay, BookStack active delivery, dedicated
administrator ordinary-workflow separation, remaining external adapters/Vaultwarden owner
administration, guided credential/bundle construction, enrollment, restores and image/release
qualification remain open. Production catalog entries stay gated. No live services/data,
commit, PR or release changed. The full all-product objective remains active.

DOX updates the Forgejo/offboard owners, runtime runbook and catalog qualification. Native
KyIdentity's owning root documents the bind option and its environment/Compose wiring. Installer
root/adapter-parent ownership, child indices, CLI/edge contracts, shared workspace preferences
and sibling products are intentionally unchanged because no ownership boundary or deployment
authority changed. Next: safe new-account lifecycle and scoped groups over this verified worker
path, including protected native receipts and unresolved-write recovery.

## Current checkpoint: owned Forgejo creation through the native worker

Protected Forgejo directory configuration now optionally accepts `create_accounts:true`;
role configuration alone does not enable creation. The source/issuer and installation UUID
stay pinned by runtime configuration. Exact native source/login-name lookup requires a
successful empty array and one zero total-count header; username or subject collisions
refuse adoption. Corrupt/missing owned metadata, unknown native reads and held initialization
never imply an absent account. Full active User POST shares the bounded strict profile/role
parser with PUT. Default targets still refuse new accounts.

Each account owns private `.creation` intent and distinct immutable creation audit files.
Intent/start precede native POST. Native creation verifies the returned account's complete
profile, source/subject, active state and ordinary authority before recording its native ID.
That verified receipt precedes an immutable binding with `creation:true`; the marker makes
its creation proofs mandatory for binding reads and repeat registration. Initial app-role
projection follows publication of the owned receipt, with its own directory audits. A final
creation marker references the immutable initial directory attempt, so later profile updates
do not erase initialization proof. Only then does SCIM return 201. Identical creation repeats
inspect the retained account and never create again or reset its profile, roles or password.

Known native rejection permits another audited attempt. A failed verified binding publication
can be repaired from its receipt without another native POST. Lost/unknown POST, malformed
success or failed post-write verification persists a hold across restart; later valid profile
changes remain held rather than releasing the producer through a known rejection. Missing
proofs fail closed. GET/PUT refuse unfinished initialization; irreversible revocation remains
available for retained bindings. Held creation/write reconciliation and creation audit relay
remain admission gates, not automatic adoption or replay paths.

Boundary race checks cover exact subject absence, missing count proof, no creation authority
from untrusted native response, unknown-write classification, durable intent/audit ordering,
known rejection retry, native receipt publication repair, no uncertain replay/supersession,
missing creation proof refusal, required administrator-grant ordering, explicit read-only
runtime opt-in and default/foreign-subject refusal. Native errors and credentials never enter
the durable proofs.

The actual KyIdentity/pinned Forgejo producer fixture now accepts `KYQ_TEST_FORGEJO_CREATE=1`.
A third independently owned identity pair is initially unassigned; audited operation-bound
app assignment of its everyday user causes the real worker to create the previously
uninstalled Forgejo account. Exact subject/source/profile, native SSO and ordinary administrator
denial pass. A repository and personal token are created; actual app-assignment removal
revokes the token and existing session while retaining the repository. Joined runtime shutdown
verifies reopened creation, initialization and revocation proofs. Existing initial/native
profile edits, original 101-token offboarding, role removal before fresh SSO and interrupted
queue/runtime repair after real backoff still pass in the same test.

Installer `make ci` and `make build` pass. The final combined producer/creation/queue/runtime
qualification passes in 89.995 seconds. `git diff --check` passes and owned fixture containers
are removed. Evidence: `/tmp/kyq-creation-boundaries.log`, `/tmp/kyq-creation-proofs-final.log`,
`/tmp/kyq-creation-native-qualified.log`, `/tmp/kyq-creation-closeout-ci.log`, and
`/tmp/kyq-creation-closeout-build.log`. An intermediate fixture compile omitted its new subject
field; that was corrected before qualification. Native KyIdentity code did not change in
this checkpoint; the fresh prior bind-capable binary remains the fixture producer.

DOX updates the closest Forgejo/offboard owners, adapter parent/root ownership notes, runtime
runbook and catalog qualification. Child indices retain their existing boundaries; CLI/edge,
native product docs, shared workspace preferences and sibling products are intentionally
unchanged because this adds no deployment authority or product-owned configuration contract.
Production catalog entries stay gated. No live services/data, commit, PR or release changed.

Group/rename delivery, creation/directory outcome resolution and audit relay, BookStack active
lifecycle, administrator ordinary-workflow separation, remaining external adapters/Vaultwarden
owner administration, guided credential/bundle construction, enrollment, restores and release
qualification retain the full original scope. The all-product objective remains active. Next:
scoped native group lifecycle over the verified identity worker path, without implicit adoption
of existing organizations/teams or loss of content.

## Current checkpoint: native Forgejo teams and owned membership

`internal/adapters/forgejo/team.go` now creates missing non-administrative teams in an exact
retained organization, inspects protected team authority and replaces owned memberships using
native HTTPS APIs. Creation verifies organization ID/name and disabled repository-admin team
control, requires a complete bounded team listing and refuses existing names, including case
collisions. The returned receipt pins organization/team/source IDs, names and exact unit
permissions. All-repository access, repository creation and owner/admin team permissions are
refused. Native malformed/unknown creation or failed verification returns `ErrTeamUncertain`.

Membership checks the receipt before writes and requires a complete native member listing.
Every current member must match a supplied owned native ID/login and verified source/subject;
foreign members stop replacement before any mutation. Desired members must be active ordinary
accounts. Withdrawals precede additions regardless of input order. Post-write verification
checks team authority, exact final membership and desired account authority. A lost write,
failed post-write verification or known rejection after an earlier successful mutation returns
`ErrTeamUncertain`, rather than releasing a partially changed operation. Repeats do not rewrite
membership. The native method changes neither repository assignments/content nor team
permissions, passwords, account roles or activity.

Boundary tests cover case-collision refusal, incomplete absence/member counts, organization
and team authority drift, foreign members, inactive/administrator accounts, post-write authority
changes, known create rejection, malformed/lost creation, membership repeat/withdrawal and
remove-before-add ordering with a held partial failure. `KYQ_TEST_FORGEJO_TEAMS=1` adds native
missing-team creation and collision refusal to the pinned Forgejo 16.0.5 fixture. A private
organization repository is readable by the ordinary account only while it belongs to the
scoped read-only team. Administrator/foreign-member cases refuse; membership withdrawal
retains the native repository ID. The fixture creates its organization through the native
administration API in its own disposable container; it does not adopt a live organization.

The combined actual KyIdentity producer, creation, teams, interrupted queue and runtime test
passes in 90.451 seconds. Actual account creation/profile edits, role removal before fresh
SSO, assignment-loss revocation, token/session denial, reopened durable account proofs and
repository retention still pass alongside native team qualification. Final `make ci`, both
binary builds and `git diff --check` pass. Evidence: `/tmp/kyq-team-native.log`,
`/tmp/kyq-team-producer-qualified.log`, `/tmp/kyq-team-boundaries-final.log`,
`/tmp/kyq-team-closeout-ci.log`, and `/tmp/kyq-team-build.log`.

This is the native group foundation, not completed central group delivery. Callers must own
serialized operations, durable intent and native team receipts before calling these APIs and
retain unknown outcomes across restart. No SCIM group receiver is enabled yet. Production
organization setup, team receipt storage/group mapping, actual KyIdentity group delivery,
group/account-role concurrency and unknown-outcome reconciliation remain next requirements.
The real fixture qualifies scoped read-only repository access, not every supported unit or
permission combination. Native-administrator refusal alone does not prove suite-wide dedicated
administrator ordinary-workflow separation.

DOX updates the closest Forgejo owner and adapter parent ownership. Root/CLI/edge/offboard
contracts, sibling product docs, shared preferences and child indices are intentionally
unchanged: no directory route, deployment authority or ownership boundary changed. No live
services/data, commit, PR or release changed. Production catalog entries remain gated, and
all other product/external-adapter, guided configuration, enrollment, recovery/restore,
administrator separation and release requirements retain the full original scope. The
all-product objective remains active. Next: durable native organization/team ownership and
SCIM group lifecycle over the verified identity worker path.

## Current checkpoint: durable Forgejo group creation ownership

`internal/offboard/group_creation.go` adds protected native group setup over the qualified
team SDK. `GroupBinding` pins installation/app/issuer/source, directory external ID, exact
native organization/team IDs and names, and non-administrative unit permissions. Native
framework/transport types stay behind the target adapter. Group identity hashing is separate
from account hashing, and each group owns its own state/audit files and creation delivery lock.
No group operation shares an account state file or a global creation lock.

`Store.CreateGroup` persists narrowed `.group-creation` intent and an immutable started audit
before the native POST. The target's native adapter verifies the created team and its authority;
the immutable result retains that exact receipt before publishing `.group-binding`. Every
binding read requires the matching intent/start/result proofs. Successful repeats inspect the
retained native team and never POST again. Known rejection can retry the identical scope.
Unknown/interrupted creation holds all later valid scope changes across restart. Foreign
native receipts become held uncertainty; names never authorize adoption. Unsafe/failed binding
publication retains its verified native receipt, allowing read-only inspection and publication
repair without another creation. Missing or conflicting receipt proofs refuse recreation.

Boundary race tests prove intent/start ordering, known-refusal retry, completed proof/repeat,
defensive permission-map copies, missing-result refusal, unknown/interrupted/foreign-receipt
holds across restart, changed-scope refusal, publication repair, installation isolation,
same-group serialization and independent-group progress. The native teams fixture invokes a
separate race-enabled bridge test executable with bounded stdin input, labeled/digest-pinned
container ownership checks and verified public TLS trust. Real native creation, private receipt
publication, reopen/repeat, a second group refused from adopting the existing team name and
missing-proof refusal pass. No live organization or team is selected by this fixture.

The combined actual KyIdentity producer/creation/teams/group-ownership/queue/runtime test
passes in 91.032 seconds; initial account profiles/roles, new-account creation, actual native
profile edits, assignment-loss offboarding, role removal before fresh SSO and content retention
remain verified. The final native test additionally requires a native 403/404 for repository
denial, so a transport failure cannot masquerade as withdrawn access; it passes in 9.379
seconds. Final `make ci`, both binary builds and `git diff --check` pass. Owned fixture
containers are removed. Evidence: `/tmp/kyq-group-creation-boundaries.log`,
`/tmp/kyq-group-owned-boundaries-final.log`, `/tmp/kyq-group-owned-producer.log`,
`/tmp/kyq-group-owned-native-final.log`, `/tmp/kyq-group-owned-closeout-ci.log`, and
`/tmp/kyq-group-owned-build.log`.

This is a protected setup API, not an enabled SCIM group receiver. Production organization
creation/ownership, durable membership intent/audits, account/group mutation concurrency,
actual KyIdentity group lifecycle, group rename/deletion semantics, held-outcome reconciliation,
group audit relay and restore remain requirements. No new runtime JSON option or SCIM route
is admitted by these methods. Dedicated-administrator ordinary-workflow separation and all
other native/external-adapter, guided configuration, enrollment, recovery, image/release and
deployment requirements retain their original scope. The complete all-product objective
remains active; production catalog entries stay gated.

DOX updates the closest offboard owner, Forgejo native fixture contract, root child ownership
index and runtime runbook. Adapter-parent ownership/child indices, CLI/edge/command/runtime
contracts, shared global preferences and sibling product docs are intentionally unchanged:
no deployment authority, runtime credential input or ownership boundary changed. No live
services/data, commit, PR or release changed. Next: durable membership operations using only
verified account/group receipts, then authenticated SCIM group delivery through the actual
KyIdentity worker with retained content and unknown-write fences.

## Current checkpoint: durable native group membership

`internal/offboard/group_membership.go` adds protected membership application over verified
account/group receipts. Input contains only bridge account IDs: a bounded unique owned scope
and a desired subset, canonicalized without mutating caller arrays. Every supplied account
must match the protected installation/app/issuer/source. Native team/member authority is still
verified by the Forgejo SDK, including refusal of foreign native members and administrator
grants. No event can supply native IDs, team permissions or credentials.

Each group owns `.group-membership` intent and separate immutable
`.group-membership-audit-*` proofs. The retained team binding digest pins the authority used
by the attempt. Intent/start precede native I/O; result proof precedes completion. Known
native refusal permits another audited attempt. Unknown/interrupted outcomes hold all later
membership changes across restart; missing result proofs refuse further native work. Native
errors/bodies and credentials stay out of these durable records.

The group delivery lock serializes creation/membership for that group. Membership additionally
reserves the delivery locks of its sorted supplied owned-account scope (at most 100), sharing
only the native authority used by roles, account creation and revocation. Other accounts
remain independent. Desired revoked, unfinished-created, unresolved-directory and completed
administrator accounts are refused before native I/O; the SDK checks live ordinary/active
authority as well. Revocation intent remains immediately publishable while membership runs.
Desired durable authority is rechecked afterward: revocation arriving during native work
leaves an unknown membership hold, not an acknowledgment. Native account revocation can still
run once the operation releases its locks, including while the persisted group hold remains.

Boundary race tests prove intent/start ordering, verified native binding dispatch, completion
proofs, missing-result refusal, unknown/interrupted holds across restart without replay or
supersession, independent account revocation during persisted holds, invalid/unowned scopes,
revoked/unsettled/administrator refusal and lock coordination with role updates/native disable.
An unrelated account progresses during the blocked group callback. The first concurrency
fixture omitted its directory callback; that fixture was corrected before final verification.

The separate real Forgejo bridge fixture now registers exact owner/everyday/foreign native
bindings, assigns its owned team to a disposable private repository and runs the same durable
membership method. Native administrator refusal, ordinary grant/repeat, restart completion
proofs, foreign-member refusal, withdrawal with explicit 403/404 denial, repository-ID retention
and revocation-intent refusal pass. Its private pending revocation fixture does not disable the
parent's ordinary account. Native fixture APIs use bounded verified HTTPS with redirects and
environment proxies refused; credentials are bounded stdin data, never argv or durable state.

The combined actual KyIdentity producer/creation/teams/group-ownership/membership/queue/runtime
test passes in 91.544 seconds. Existing actual worker profile edits, new-account SSO and
revocation, old-session administrator removal, token/session denial and content retention still
pass. Final `make ci`, boundary race checks, both builds and `git diff --check` pass; owned fixture containers
are removed. Evidence: `/tmp/kyq-membership-boundaries-final.log`,
`/tmp/kyq-membership-native.log`, `/tmp/kyq-membership-producer.log`,
`/tmp/kyq-membership-closeout-ci.log` and `/tmp/kyq-membership-build.log`.

DOX updates the closest offboard owner, Forgejo fixture contract, root ownership index and
runtime runbook. Adapter-parent ownership/child indices, CLI/edge/command/runtime configuration,
shared global preferences and sibling product docs are intentionally unchanged because no
deployment authority or transport route changed. Production catalog entries remain gated.
No live services/data, commit, PR or release changed.

This remains a protected library path, not enabled SCIM Groups delivery. The next requirement
is the authenticated full-group receiver and its explicit protected runtime configuration,
then actual KyIdentity group creation/update/deletion/rename qualification. Production
organization ownership, group audit relay, unknown-outcome reconciliation, restore and suite
administrator ordinary-workflow separation remain gates; serialization does not prevent a
later administrator promotion from retaining ordinary membership. All other native/external
adapters, guided configuration, enrollment, recovery, release and deployment requirements
retain the full original scope. The all-product objective remains active.

## Current checkpoint: authenticated Forgejo group receiver

Forgejo now has an explicitly enabled SCIM Groups receiver. Protected configuration pins
the organization ID/name and permitted repository units; request bodies cannot choose
organization or privileges. Group membership accepts only verified bridge account receipts,
derives withdrawals from the latest proof under the group lock, and refuses unresolved
native writes. Human group renames change the native description while preserving the
owned team identity. Logical deletion withdraws membership, retains repositories and
durable ownership, and refuses reactivation.

Disposable Forgejo 16.0.5 tests passed for authenticated TLS group creation, read, rename,
retirement, repeat/restart, repository retention and ordinary token access withdrawal.
The combined actual KyIdentity account-worker regression also passed (91.550 seconds),
including account creation, profiles, administrator role removal and revocation. Group
requests in that test were driven separately: actual KyIdentity group event production
has not yet been qualified. That is the next concrete integration step.

The final CI log records the tidy/format/vet/race checks with all tested packages passing;
the build log records both workstation and resident binaries built. The process handles
were already closed at this status check, so no new exit-code observation is claimed.
Focused native and boundary tests had recorded successful terminal results. Diff whitespace
checks pass. Root, offboarding and Forgejo DOX contracts and operational docs were updated;
this checkpoint changes no additional stable contract or child index.

All 12 Ky server setup interfaces and installer adapters have local implementation/test
coverage. This does not make the full installation deployable: remaining external-app
adapters and admission proofs, automatic protected configuration construction, dedicated
administrator/everyday authorization, recovery/restore, enrollment, released dependencies
and deployment acceptance retain the original scope. Vaultwarden remains the only new
password-service choice. No live service was changed and no release was published. The
all-product objective remains active; there is no new user-input blocker.

## Current checkpoint: actual KyIdentity group worker to Forgejo

The next integration step is now qualified. `KYQ_TEST_FORGEJO_GROUPS=1`, with producer
and teams modes, builds an owned KyIdentity group, assigns it to the fixture app and
enables the protected Forgejo group policy. Native setup create/check/resume produces
the group receipt. Audited, operation-bound native identity mutations drive the actual
worker through group creation, rename, membership removal/regrant and deletion. The
observer performs authenticated verified-TLS reads only; it never manufactures group
delivery requests. Native repository access follows membership changes, and retirement
retains the repository and native team.

After joined shutdown the bridge reopens verified ownership and membership artifacts:
at least five completed attempts, the renamed label, empty members and permanent
retirement are required. The same fixture retains actual worker account creation,
profile changes, administrator loss and revocation checks. The final pinned Forgejo
16.0.5 native run passed with the race detector in 106.533 seconds; an earlier combined
run passed in 109.829 seconds. Final `make ci`, `make build` and diff whitespace checks
passed with observed successful terminal results. No live deployment was changed.

The Forgejo and offboarding DOX owners and operational/catalog docs now describe the
actual worker fixture rather than listing group production as unqualified. Root and
parent adapter contracts/index remain unchanged because this adds qualification within
existing ownership, not a new domain or operating contract. Production organization
provisioning, group audit delivery, held-outcome reconciliation, restore and dedicated
administrator ordinary-workflow separation remain gates. The source's existing public
provisioning view reports users; this test does not claim a public group acknowledgment
view. Complete remaining native/external adapters, automatic protected configuration,
enrollment, recovery, release and deployment requirements retain the original scope.
The all-product objective remains active and is not blocked on user input.

## Current checkpoint: catalog admission correction and Immich native boundary

Continued the remaining external-app work after actual Forgejo group-worker qualification.
Vikunja v2.7.0's tag resolves to `a16be96aa454671fdf213b0fbe411dd38a098418`; its native
`user set-admin` command checks the Pro admin-panel entitlement before granting authority.
The official Pro documentation confirms the administrator API is gated as well. This
contradicts the old catalog's free-administration assumption. The catalog now rejects stock
Vikunja for new installs under the existing free-edition requirement, preserves existing
installations, and leaves the task-management category unresolved rather than dropping it
from the objective. No substitute or license bypass was selected.

Added `internal/adapters/immich` against v3.2.0 source commit
`1b6098c9dbfffe978bec2d414606ed7a4c8e019a`. Native inspection requires the exact release,
enabled OAuth provider with insecure requests disabled, pinned issuer/client, native UUID,
OIDC subject, explicit administrator field and active/nondeleted state. The supported
administrator PUT changes only `isAdmin`; repeat does not rewrite and readback must prove
the requested authority while preserving identity/profile. Unknown writes or failed
verification return `ErrUncertain`; callers still need target-local intent/serialization.
HTTPS uses verified TLS 1.2+, no proxy or redirects, bounded headers/body/time and generic
errors without native response bodies or API keys. No email-based adoption is implemented.

Verified TLS boundary fixtures pass grant/repeat/demotion, foreign identity/provider refusal,
insecure-provider refusal, missing-field/deleted-account/version refusal, redirects,
malformed responses and uncertain writes. Focused race tests and final `make ci` passed;
`make build` and diff whitespace checks passed. These are API boundary fixtures, not a real
Immich deployment or native sign-in/session/key-denial proof. No live service changed.

Source inspection also proves Immich DELETE schedules account/content removal; it must not
stand in for offboarding. Complete owned provisioning and product setup, back-channel logout,
API-key/session revocation, dedicated administrator ordinary-workflow policy, installer
dispatch, native qualification and release/deployment/restore remain unfinished. Other Ky
and external adapters, automatic configuration, enrollment, recovery and task-management
selection retain the original scope. The all-product objective remains active.

DOX: added the Immich child owner and refreshed its parent ownership/index; corrected the
catalog and Phase 7 roadmap. The root contract/index is unchanged because external adapters
already belong to the parent and the free-edition requirement is unchanged. Next: qualify
Immich against a pinned disposable native service and complete its content-preserving
offboarding/setup interfaces; retain the remaining external-app work in parallel scope.

## Current checkpoint: native Immich consumer and revocation gap verified

Added an opt-in real-service fixture for the partial Immich adapter. The official v3.2.0
amd64 image is pinned to
`sha256:949bbf9ff32543539f471bc275437e6d1a1a4fee43c55bb9e4c06b946909bf1c`; its OCI revision
matches source `1b6098c9dbfffe978bec2d414606ed7a4c8e019a`. The fixture uses the release's
pinned PostgreSQL/Valkey dependencies, new storage, an owned labeled Docker network and
one loopback-only host port. A synthetic RSA/PKCE provider runs in its own unexposed
container, compiled from the test package. Protected disposable files carry only fixture
configuration/TLS material. Initial discovery could not reach a workstation gateway;
moving the provider into the same private network fixed that without firewall changes.

Native signed owner/everyday login and exact provider/account inspection pass. The SDK
verifies native administrator grant/repeat/demotion and foreign-subject refusal while
retaining identity/profile. A fresh signed back-channel logout then denies the everyday
session and preserves the unrelated owner session, but the everyday personal API key
still succeeds on account access. This is verified incomplete offboarding, not a passed
deprovision adapter. Account DELETE was not called; it remains unsuitable because it
schedules content removal. The next missing native interface must revoke personal API
keys and preserve content, with owned identity/receipt checks and repeat verification.

The final native race test passed in 18.039 seconds, including asserted cleanup. Earlier
native account/grant qualification passed in 32.598 seconds. Full `make ci`, `make build`
and diff whitespace checks passed with observed terminal success. No labeled fixture
container or network remains. No live service, existing credential, source product
checkout, release or production catalog workload changed.

Immich child DOX and catalog evidence now distinguish actual native verification from
boundary fixtures and record the key-revocation gap. Parent/root ownership and indexes
remain unchanged: the new test files live within the existing Immich domain. Actual
KyIdentity integration, owned setup/provisioning, complete offboarding, dedicated-admin
ordinary-workflow denial, photo retention/restore, deployment and release gates remain.
All remaining Ky/external adapters, automatic configuration, enrollment, recovery and
task-management selection retain the original full scope. The goal remains active;
there is no user-input blocker.

## Current checkpoint: memory-interruption recovery and disk audit qualification

The user reported this agent was killed for memory usage. Repository edits survived;
`/tmp` was cleared, including the source clone and previous logs. Current inspection
confirmed `/tmp` is a 12 GiB tmpfs. Before interruption it held approximately 9.5 GiB;
this is a plausible contributor, not a proven OOM diagnosis. No surviving installer
build/test process was found. Do not use lost temporary logs as fresh verification.

Recovered the exact Immich v3.2.0 source commit
`1b6098c9dbfffe978bec2d414606ed7a4c8e019a` into the disk-backed sparse checkout
`/home/yoshi/.cache/kyquickstart-work/immich-v3.2.0` (server and packages only).
New compiler scratch and logs use that disk-backed workspace. Run checks with
`GOMAXPROCS=2 GOMEMLIMIT=1GiB GOFLAGS=-p=1` and its `TMPDIR`; the Go heap target is soft,
not a process memory ceiling. Native Immich fixtures now cap server/database/provider/
cache memory at 2 GiB/768 MiB/256 MiB/128 MiB with no additional container swap, and
compile the test provider with one build worker. The native race fixture passed in
17.293 seconds, including the unchanged verified API-key revocation gap and cleanup.
No owned Immich container/network remained afterward.

Disk-backed full CI exposed a real audit-relay defect, not merely a timeout under load.
On this Btrfs filesystem the retained directory reader missed newly published receipt
files after rewind, although a fresh pathname directory read saw them. The relay now
reopens at the next full scan, after intervening writes, while preserving bounded paging,
ordered receipt verification and missing-history refusal. Reopening before publication
alone did not fix the reproduced failure. Tests report early service termination directly
and exercise a second account's audit work arriving after the relay has already delivered
its initial work. Late delivery, historical-receipt refusal and joined runtime tests passed
with the race detector in 14.401 seconds. Temporary diagnostic instrumentation was removed.

Final constrained `make ci` and `make build` both completed successfully; whitespace
checks passed. Logs are on disk under `/home/yoshi/.cache/kyquickstart-work/`:
`immich-native-capped.log`, `recovery-audit-late.log`, `recovery-ci-final.log` and
`recovery-build-final.log`. Updated the owning Immich/offboarding DOX contracts. Parent
and root docs/indexes remain unchanged because ownership and suite contracts did not move.

No live deployment, existing credential or content was changed. The Immich revocation
extension has not yet been implemented: continue against the recovered source to add
scoped, permanent session/API-key denial without account/content deletion, then qualify
native retention and new-login denial. All other adapter, configuration, recovery,
administrator-separation, deployment and release requirements retain their original scope.
The full all-product goal remains active, with no user-input blocker.

## Current checkpoint: Immich retained-account module built and natively qualified

Implemented the missing product-side interface as an AGPL-3.0 community extension to
Immich v3.2.0 source `1b6098c9dbfffe978bec2d414606ed7a4c8e019a`, rather than deleting an
account or globally unlinking OAuth. Durable artifacts are in
`internal/adapters/immich/module`: source patch, license, native contracts/build runbook
and disposable overlay Dockerfile. Patch SHA-256:
`a6b67b4319f2dc0cc19373eea58e4de072a8e6b9a1745e0d5d9cd5d6564623d6`.
It matches the isolated checkout's complete diff and applies cleanly to a fresh detached
checkout of the pinned commit; that checking worktree was removed after verification.

Native administrator read/update permissions and an API key protect scoped access/revoke
endpoints. Exact native account/provider/subject pins and immutable installation ownership
are required; self-revocation and session-only authority are refused. A per-user transaction
locks the real native account invariant, publishes a write-once `ky.immich-access/1` fence,
and removes only that user's sessions and API keys. Account/content fields stay untouched.
Fence presence fails closed even if metadata is corrupt; a provider/client/subject query
also denies fresh OAuth sign-in before account resolution. Socket subscribers disconnect,
and authentication is rechecked after joining rooms to close the join/revocation race.
No clearing/reactivation route exists. Already authorized in-flight requests can finish;
this is not a cancellation guarantee. Restore must retain the fence with the account.

The installer-side HTTPS consumer now verifies explicit native module fields/counts and
installation ownership before revocation, independently reads back permanent denial and
zero credentials afterward, and verifies retained identity/profile. Lost writes, missing
post-write proof and profile/identity drift return `ErrUncertain`; callers still require
protected target-local intent and reconciliation. Boundary race fixtures prove refusal of
foreign installation ownership, missing/negative counts and held uncertain outcomes.

Built upstream SDK, plugin SDK and server serially from frozen dependencies; full server
type checking, changed-source ESLint/formatting and all 100 existing authentication/admin
unit tests passed. The local overlay is immutable image
`sha256:f57ba0873e2e77f9c62374e7c770f26aca914c909449cded4f502e504ac52327`
over the official pinned amd64 image. Source/patch labels identify fixture inputs, not
production provenance. No image was published and no production workload uses it.

Native qualification with the race detector now uploads a fresh PNG, verifies its native
record and original bytes, opens a subscribed TLS socket, then proves session/key/socket
denial, fresh signed login refusal, unchanged native account/profile/photo counts and
bytes, unrelated owner access, permanent repeat and native endpoint refusals for foreign
subjects/installations, self-revocation and session-only authority. Final expanded run
passed in 20.146 seconds; earlier expanded scope/socket qualification passed in 17.385
seconds. The provider remains synthetic. Two early service starts exited with code 1 and
`oom=false`; their underlying startup cause was not captured. The fixture now explicitly
waits for TCP PostgreSQL and Valkey readiness before starting Immich, and reports safe
startup categories/state with protected diagnostics if readiness fails. Do not interpret
those earlier failures as verified memory exhaustion or native API verdicts.

The updated stock-image fixture passed in 15.667 seconds and still proves stock logout
retains personal API keys. Full installer `make ci`, both binary builds, source and repo
whitespace checks passed with observed terminal success. No owned Immich container/network
remains. Disk-backed logs are `immich-module-native-complete.log`,
`immich-stock-after-module.log`, `immich-module-full-typecheck.log`,
`immich-module-lint-final.log`, `immich-module-unit.log`, `immich-access-boundary.log`,
`immich-module-ci.log` and `immich-module-installer-build.log` under
`/home/yoshi/.cache/kyquickstart-work/`.

DOX: added the module owner/index and updated Immich/parent ownership and catalog evidence.
Root contracts/index remain unchanged because this stays within the existing adapter domain.
No live deployment, existing credential, content, service checkout, release or PR changed.
This qualifies the module and consumer, not complete Immich installation: resident bridge
UUID binding/dispatch, actual KyIdentity delivery, owned setup/provisioning, protected
configuration, dedicated-administrator ordinary-workflow separation, restore and release
remain gates. Continue with bridge integration and product-owned setup; retain all other
Ky/external adapter, task-management selection, configuration/enrollment/recovery and
full deployment requirements. The all-product goal remains active and has no user-input
blocker; this milestone does not narrow its scope.


## Checkpoint: Immich resident revocation and interruption recovery — 2026-10-10

The resident bridge now supports Immich's UUID account identity with an independently
pinned OIDC client ID. Numeric BookStack/Forgejo receipts retain their existing shape;
mixed identity formats, foreign clients and receipt replacement are refused. Runtime
loading remains read-only. Immich dispatch supports retained-account revocation only;
account creation, active directory/profile delivery and groups remain unsupported.

The real native fixture now starts the bridge worker before creating a job, rejects the
first native revocation write with 503, then verifies its actual delayed retry succeeds
on attempt two. Restarted stores retain both the account binding and completion/audit
proofs. The worker reopens its directory for each full scan so late jobs are visible on
Btrfs. Socket heartbeats prevent idle timeout from masquerading as revocation. Native
checks prove old session/key/socket and fresh login denial, retained photo record/bytes
and unrelated owner access. Both parent and helper use the race detector. The sender
and OIDC provider are synthetic; actual KyIdentity lifecycle delivery remains a gate.

Final native run passed in 52.107 seconds; full `make ci` passed. After the memory
interruption, both binaries were rebuilt with observed exit zero and `git diff --check`
passed. No fixture-owned Immich containers or networks remain. Verification logs are
`immich-bridge-native-race-final.log`, `immich-bridge-ci.log` and
`immich-bridge-build.log` under `/home/yoshi/.cache/kyquickstart-work/`.

Recovery confirmed the work survives on disk. Builds run serially with GOMAXPROCS=2,
GOMEMLIMIT=1GiB and GOFLAGS=-p=1; large temporary files use the disk-backed cache directory,
not RAM-backed /tmp. These limits constrain build work; they do not diagnose the cause of
the agent termination. No unrelated processes were stopped.

DOX: updated offboard and Immich contracts, parent adapter gate wording, operational
offboarding documentation and catalog evidence. Root ownership/index stays unchanged
because these changes remain within existing adapter/offboard boundaries.

No live deployment, credentials, product data, release or PR changed. Work remains
uncommitted. Continue with Immich owned setup/provisioning and actual KyIdentity delivery,
then the remaining product adapters and full installer acceptance. The all-product goal
remains active and is not narrowed to this milestone; there is no user-input blocker.


## Checkpoint: native Immich OIDC configuration consumer — 2026-10-10

Added `internal/adapters/immich/provider.go`: inspect a version-qualified native
configuration and retain its fingerprint as protected target-local preflight intent.
Configure only a blank provider or repeat the exact desired provider. Refuse foreign
providers, disabled-but-configured providers, invalid HTTPS/claim/credential inputs and
configuration drift. Set native OIDC, signed ID tokens, client-secret-post and roles claim;
retain every unrelated native setting. Independent full readback proves the outcome, while
lost writes or changed readback remain `ErrUncertain`. Fingerprints contain no raw secrets
but belong in protected target state, not public reports.

The shared HTTP decoder now retains JSON numbers exactly. Boundary tests prove a setting
above the float64 integer limit survives unchanged, alongside unrelated OAuth labels and
other settings. The provider consumer reuses version validation from account inspection.
Native fixture setup now calls this adapter instead of its own direct configuration PUT.

Verification with the race detector: boundary package passed; stock native configuration,
repeat/stale/foreign refusal and signed owner/everyday login passed in 25.656 seconds.
The community module plus actual bridge worker fixture also passed in 54.796 seconds,
including unavailable-write retry, session/key/socket and new-login denial, retained photo
data and unrelated owner access. Full `make ci` passed (offboard 28.965 seconds). Both
workstation/resident binaries built successfully. No owned Immich containers/networks remain.
Disk-backed logs: `immich-provider-native.log`, `immich-provider-module-bridge.log`,
`immich-provider-ci.log` and `immich-provider-build.log`, under the existing cache directory.
All compilation remains serial and memory-limited.

The upstream configuration PUT is a whole-document write without conditional-update
support; production callers must control every configuration writer. The consumer is
not yet wired to a protected installation receipt, target lock or product-owned apply-setup
command. Native autoRegister is first-login creation, not complete directory provisioning,
and requires Assigned users only in KyIdentity. This does not prove actual KyIdentity
event delivery or dedicated-administrator ordinary-workflow separation.

DOX updated Immich/parent ownership and catalog evidence. Root, module and offboard
contracts/indexes stay unchanged because no deployment, native module or bridge contract
changed. No live workload, credentials, content, release or PR changed; work is uncommitted.
Continue with product-owned setup and protected ownership/configuration, then actual
KyIdentity lifecycle delivery and all remaining products/full installer acceptance. The
all-product goal remains active, with no user-input blocker and no reduction in scope.


## Checkpoint: product-owned Immich setup and initial accounts — 2026-10-10

The pinned community module now provides `immich-admin apply-setup --file <path>` with
`--check` and `--resume`. Its protected `immich.setup/1` bundle names installation UUID,
HTTPS provider/client/secret/roles claim and distinct administrator/everyday subject, email
and name. Input is bounded to 64 KiB with fatal UTF-8 decoding and a strict schema. The
absolute canonical file must be a 0600 regular file owned by the command user, beneath
protected nonsymlink root/user-owned parents. Config-file installations are refused.
Errors omit underlying bodies, bundle values and credentials.

Native initial setup requires a blank OIDC provider and absent subject/email accounts.
A serializable native transaction creates both passwordless accounts and cluster groups,
sets OIDC and pins `ky.immich-setup/1` system metadata with the installation/intent hash
and native UUIDs. Unrelated accounts/settings are retained. Check remains read-only;
resume requires the receipt. Repeated processes verify exact intent, account UUID/subject,
profile, active status, roles, password absence and access-fence absence. Foreign
accounts/provider/installation, drift and revoked accounts refuse setup. There is no
adoption, reactivation or migration behavior. The CLI independently rechecks ownership
after commit before returning its non-secret report.

Expanded native qualification creates the protected bundle inside the fixture container;
no secret appears in command arguments. It proves missing-resume refusal, initial check
without provider mutation, foreign-account conflict with unchanged configuration and
subsequent successful creation, independent native account inspection, repeated check/
resume with identical UUIDs, foreign installation/provider refusal, public-file/symlink/
parent-write refusal and intact owned state afterward. Signed native owner/everyday
login must use those precreated UUIDs. After bridge revocation, setup resume fails and
independent readback retains the fence/time and zero sessions/keys. Existing photo-byte,
profile, unrelated owner and signed fresh-login denial checks still pass.

The final combined setup/module/bridge run passed with the race detector in 83.103
seconds (82.07 seconds in the test); the earlier initial setup run passed in 79.709
seconds. Full server type checking, changed-source formatting/ESLint, source build and
114 existing authentication/admin/CLI tests passed. A clean detached pinned worktree
applied the patch and reproduced its exact bytes, then was removed. Full installer
`make ci` passed (offboard 29.330 seconds), both binaries built and whitespace checks
passed. No fixture-owned Immich container/network remains. Compilers and fixtures stayed
serial with the existing memory caps and disk-backed scratch.

Current source patch SHA-256 is
`9613f0a8cff7cdeea4d045bd6bac71a101b7249656c075812a00c5617cfd73a5`.
Current local fixture image is
`sha256:2c21fb3a13545f5bddb1b9e1cc427cf61b115f3c16c3df552c302bd4a6445517`;
`immich-setup-image.id` and the canonical `immich-module-image.id` both reference it.
This supersedes the prior overlay for current patch qualification; it is not published
or production provenance. Logs in `/home/yoshi/.cache/kyquickstart-work/`:
`immich-setup-typecheck.log`, `immich-setup-lint.log`, `immich-setup-build.log`,
`immich-setup-unit.log`, `immich-setup-native-final.log`, `immich-setup-ci.log` and
`immich-setup-installer-build.log`.

DOX updated module/parent ownership and contracts, module runbook and catalog evidence.
Root/offboard indexes and contracts remain unchanged: ownership stays within the existing
adapter module, and resident bridge behavior did not change. No live workload, credentials,
content, release or PR changed; source/artifacts remain uncommitted.

This is initial product-owned setup, not complete installation acceptance. Protected
installer dispatch, native configuration/account writer coordination, actual KyIdentity
OIDC/event delivery, ongoing profile/account provisioning, dedicated-administrator ordinary
workflow separation, recovery/restore and production packaging still require work. Continue
with installer dispatch and actual KyIdentity qualification, preserving the other Ky/external
adapter, task-category selection and full deployment scope. The all-product goal remains
active with no user-input blocker.


## Checkpoint: explicit Immich installer dispatch over SSH/Docker — 2026-10-10

Registered the implemented community module in `productAdapters` as `immich`, using its
absolute container-local immich-admin binary. `immich.go` requires bounded native setup
capabilities, exact v3.2.0/module/bundle contracts and initial-account/ownership/check
features before mutation. It validates explicit check mode, nonmissing arrays/role bits,
distinct native v4 account IDs/subjects and atomic creation labels. Planned setup is
incomplete, not ready. After apply, independent check must retain installation UUID and
exact native UUID/subject/role ownership. Only fixed resource labels and native UUIDs enter
the sanitized product report; arbitrary native fields and raw subjects remain unrendered.

The module now advertises capabilities without a bundle and refuses combined setup flags.
Its input installation UUID is explicitly lowercase, aligning with workstation report
validation before any native mutation. The fixture proves uppercase intent is refused
with unchanged configuration.

Existing/saved/guided explicit integration supports this initial setup on Docker only.
Batch validation rejects a later Immich Kubernetes selection before any earlier product
connects or changes. Direct dispatch rejects the unsupported target before connection.
Production catalog admission is unchanged; stock Immich lacks this module contract.

New native installer qualification uses an immutable local image built from the existing
e2e SSH fixture. A capped owned loopback-only SSH sidecar mounts the Docker socket for
this disposable target; generated host/client keys and a joined private agent stay outside
workstation state. Actual saved integrate-stack first refuses unconfigured inspection and
a contended target lock with no success output. It then creates native configuration and
both accounts through SSH/Docker, verifies and releases the target lock, inspects and
repeats with retained host-key pins. Reports/state contain neither native credentials nor
raw owner subjects. Native signed login uses the created account IDs afterward; bridge
retry/revocation, photo/profile retention, unrelated owner access, permanent new-login
denial and refusal to resume revoked setup still pass. No live target is used.

Verification: final combined native installer/setup/module/bridge race run passed in
112.457 seconds (111.42 seconds in the test); the earlier full SSH run passed in
149.609 seconds. Capability/report/ownership/Docker restriction boundary tests passed.
Complete source type checking, changed-source lint/formatting/build and 114 upstream
authentication/admin/CLI tests passed. A clean detached pinned checkout applies and exactly
reproduces the final patch; it was removed. Final `make ci` passed (offboard 29.346 seconds)
and both binaries built. Whitespace checks passed; no owned Immich containers/networks
remain. Memory-limited compilation and all fixture work stayed serial.

Current patch SHA-256:
`b913c57ced47e0853e5983b0b2413263aefac906f1a9a10641d4bccb27a3c5c6`.
Current overlay:
`sha256:57c13c66a8976c17e849d553ec59306000aee6122485708530555e948a98515a`.
`immich-dispatch-image.id` and canonical `immich-module-image.id` reference it. The older
setup-image pointer remains historical and lacks the current capability contract. These
are local fixture artifacts, not a published release or production provenance. The SSH
fixture image is pinned in `immich-dispatch-ssh-image.id`. Logs under the existing disk
cache: `immich-dispatch-boundary.log`, `immich-dispatch-native-final.log`,
`immich-dispatch-typecheck.log`, `immich-dispatch-lint.log`,
`immich-dispatch-source-build.log`, `immich-dispatch-unit.log`,
`immich-dispatch-ci-final.log` and `immich-dispatch-installer-build.log`.

DOX updated CLI/module/adapter contracts, module/guided runbooks and catalog evidence.
Root/offboard/stack contracts and child indexes remain unchanged because ownership stays
in the existing domains and neither bridge nor stack wire schema changed. No live workload,
credential, product content, release or PR changed. Changes remain uncommitted.

Continue with actual KyIdentity OIDC/event delivery, ongoing account/profile lifecycle,
administrator ordinary-workflow separation, external-writer coordination, recovery/restore
and production deployment, plus all other Ky/external adapters and task-category selection.
Initial Immich dispatch is a milestone, not completion or a reduction of the all-product
goal. The full goal remains active with no user-input blocker.


## Checkpoint: actual KyIdentity OIDC through Immich installer setup — 2026-10-10

Resumed from the disk checkpoint after memory termination. Added an opt-in real-provider
mode to the existing disposable Immich fixture rather than a separate deployment harness.
`KYQ_TEST_IMMICH_IDENTITY_BINARY` requires an explicit absolute executable regular static
binary and native setup qualification. The chosen binary runs in the owned Docker network
with private data, 512 MiB memory/no swap growth, one CPU, dropped capabilities and no host
port. The existing TLS provider container becomes an exact-scoped reverse proxy; only its
TLS port is published on loopback. KyIdentity trusts that proxy's exact /32. The workstation
client verifies the private CA and maps only that issuer to the owned loopback endpoint;
redirects and environment proxies remain refused.

The fixture invokes actual KyIdentity product-owned setup for two independent owned account
pairs and one confidential assigned-only Immich client, with administrator/everyday roles.
Protected files hold credentials; native UUID subjects come from creation receipts. Checks
and repeated CLI processes retain objects, and the native registry confirms Assigned users
only. The original synthetic fixture secret was too short for KyIdentity's 32-byte minimum;
real-provider mode now uses a sufficiently long disposable credential. Native validation
remains unchanged.

Those actual UUID subjects enter the protected Immich initial bundle. Actual saved
integrate-stack creates the Immich accounts and provider over SSH/Docker and independently
checks/repeats them under the target lock and retained SSH host pin. Tests now also prove
all provider subjects stay out of workstation state and rendered reports.

Native S256 authorization/code exchange uses actual KyIdentity cookies, CSRF, signing and
roles. It lands on the exact setup-created Immich account UUIDs. The owner can call Immich's
administrator API; the everyday account receives denial. Unassigned ordinary and global
KyIdentity administrator accounts receive access_denied without an app authorization code.
Native grants/repeats/demotion and identity/profile preservation still pass. After controlled
SCIM worker retry revokes the everyday binding, a freshly obtained genuine KyIdentity code
cannot authenticate to Immich; keys/sessions/sockets remain denied, and native account/profile,
photo record and original bytes remain unchanged. Setup resume cannot restore the fence.
Synthetic signed back-channel logout stays in the synthetic mode; this run does not claim
actual KyIdentity logout/event delivery.

Verification: initial real-identity combined run passed in 112.354 seconds. Final stronger
real-identity SSH/setup/module/bridge race run passed in 100.193 seconds (99.16 in the test),
including administrator API and global-administrator assignment refusal. Final synthetic
SSH/setup/module/bridge regression passed in 112.079 seconds (111.02 in the test).
`make ci` passed all tidy/format/vet (including e2e-tag compilation) and race checks;
offboard completed in 28.485 seconds. Whitespace checks passed. All build/test handles are
terminal and all owned Immich containers/networks were removed. Builds and fixture runs
stayed serial with the disk-backed scratch and memory limits. Production Go/TypeScript
code and the native module patch did not change; its previously built binaries/source
qualification remain applicable.

Disk artifacts under `/home/yoshi/.cache/kyquickstart-work/`: `kyidentity-fixture`,
`identity-fixture.work`, `immich-identity-build.log`, `immich-identity-boundary.log`,
`immich-real-identity-native.log`, `immich-real-identity-native-final.log`,
`immich-identity-synthetic-regression.log` and `immich-real-identity-ci.log`.
The actual local identity binary SHA-256 is
`a44f86ffa791d0559d4c514da0be6a0cfae3c7a635655d477c7793f7666336bf`, built from
`KyIdentity-installer-setup` at base HEAD `8cf7cf9f424e14eaf9d7bea8e822af904f779528`
plus its existing uncommitted installer changes and local unpublished primitives.
The module patch and overlay remain
`b913c57ced47e0853e5983b0b2413263aefac906f1a9a10641d4bccb27a3c5c6` and
`sha256:57c13c66a8976c17e849d553ec59306000aee6122485708530555e948a98515a`.
These are fixture artifacts, not published releases or production provenance.

DOX updated Immich/parent ownership and verification, module/guided runbooks and catalog
qualification. Root/CLI/offboard/stack contracts and child indexes remain unchanged: the
new fixture stays within existing ownership and production command/schema/bridge behavior
did not change. No live service, credentials, product content, commit, PR or release changed.
Changes remain uncommitted.

Next: implement verified ongoing Immich profile/role projection and its native ownership/
reconciliation receipts, wire the resident directory target, then qualify actual KyIdentity
SCIM worker/event delivery. Current Immich receiver returns 405 for active-user PUT because
it has no directory implementation; a real sign-in proof must not hide that missing lifecycle.
Keep initial ownership checks compatible with explicitly owned directory updates rather than
silently allowing drift. Creation, dedicated administrator ordinary-workflow denial and
mixed-use migration, external-writer coordination, recovery/restore and deployment remain
gates, alongside the other Ky/external adapters and unresolved task-category choice. The
full all-product objective remains active with no user-input blocker; this checkpoint does
not reduce its scope or admit Immich into production.


## Checkpoint: native owned Immich profile/role projection — 2026-10-10

Built the missing product-side directory interface before enabling the resident connector.
The pinned community module now creates a `ky.immich-directory/1` metadata receipt per
initially owned administrator/everyday account at revision zero. Strict native GET/POST
endpoints require administrator API-key permissions, exact immutable installation/native
account UUID/provider/subject and current native state. POST refuses self-updates, stale
revisions, foreign ownership, absent receipts, configuration-file overrides and any access
fence. A serializable native transaction commits only name/email/administrator and the next
receipt together. Identical repeats retain revision; duplicate-email failure rolls back the
profile and receipt. Read-only proof remains available for the retained revoked profile;
updates cannot restore access. These endpoints manage the two initially owned accounts;
new-account creation/adoption is not implemented.

Setup check/resume still requires the exact original installation intent hash and native
identity bindings, but verifies profiles/roles against the independently owned current
directory receipts. Legitimate directory changes survive the original bundle's inspection
rather than being reset or treated as arbitrary drift. Reports reflect current roles;
workstation verification still requires actual owner administrator access and everyday
administrator denial. Older local overlays without directory receipts are refused and need
explicit reconciliation; no automatic migration/adoption was added.

Managed OAuth authentication verifies the current receipt/native profile and requires the
signed role to agree with the projected role. A stale genuine code/role cannot restore an
older administrator grant. Unreceipted native edits remain reconciliation conflicts; native
configuration/account writers and ordinary profile-edit controls still need production
coordination. The upstream standalone grant API is not the ongoing directory writer.

Added `directory.go` to the installer-side native consumer: bounded verified current
profile/revision/revocation proof, exact compare-and-set request and independent readback.
Missing identity/role/revision fields, foreign/profile conflicts and revoked accounts stop
before writes. Lost writes and incorrect post-write revisions/profiles hold an uncertain
outcome; a later matching native inspection can reconcile an already completed request
without a second mutation. No secret/body enters an error or report.

Native qualification runs after actual SSH/Docker setup and real KyIdentity sign-in. It
proves name/email update/repeat, original setup check with retained new profile, stale and
foreign update refusal, duplicate-email transactional rollback, refusal to silently adopt
an unreceipted native edit, actual administrator API access after a protected role grant,
old-session denial after demotion, session-only directory-authority refusal and denial of
fresh signed OAuth when its role conflicts. The fixture restores its owned original profile
at revision four before the existing photo/revocation checks. Those checks prove the exact
receipt survives permanent fencing and no subsequent directory update is accepted.

Verification: the complete real-KyIdentity SSH/setup/directory/module/bridge race fixture
passed in 103.157 seconds (102.10 seconds in the test). The complete synthetic-provider
regression, including signed back-channel logout, passed in 98.011 seconds (96.98 in the
test). Directory TLS boundary tests passed. Final full server type checking, changed-source
lint/format/build and 114 upstream auth/admin/CLI tests passed. A clean detached pinned
checkout applies and exactly reproduces the full patch and was removed. `make ci` passed
all tidy/format/vet (including e2e-tag compilation) and race checks; offboard completed in
29.790 seconds. Both workstation/resident binaries built. Whitespace checks passed;
all build/test handles are terminal, and no owned Immich containers/networks remain.
Compilation and native fixtures stayed serial with disk-backed scratch and memory caps.

Current patch SHA-256:
`2e6a7cf6a53fc0a26eb3bc5071d59f524ad331bc8a528a26b45a1446f6122ef7`.
Current immutable local overlay:
`sha256:bca02f2be505e70335c5a98664cc1fe6a038a761447d028c3f0b8c2628e7f13a`.
`immich-directory-image.id` and canonical `immich-module-image.id` reference it. Older
setup/dispatch overlays are historical and do not match the current patch. The actual
identity binary/workspace and SSH image remain those recorded in the previous checkpoint.
Disk logs under `/home/yoshi/.cache/kyquickstart-work/`: `immich-directory-boundary.log`,
`immich-directory-typecheck-final.log`, `immich-directory-lint.log`,
`immich-directory-source-build.log`, `immich-directory-unit-final.log`,
`immich-directory-image-build.log`, `immich-directory-native.log`,
`immich-directory-synthetic-regression.log`, `immich-directory-ci.log` and
`immich-directory-installer-build.log`. These source/image artifacts are disposable
qualification evidence, not published releases or production provenance.

DOX updated native module/parent ownership, directory contracts and verification,
module/guided runbooks and catalog qualification. Root/CLI/offboard/stack contracts and
child indexes remain unchanged: ownership stays within the existing adapter/module, and
neither production runtime schema nor bridge behavior was enabled. No live workload,
credentials, product content, commit, PR or release changed. Source changes remain
uncommitted.

Next: wire the native directory consumer into the protected resident Immich target/runtime
(without claiming account creation), then qualify actual KyIdentity SCIM worker profile/role
and revocation delivery using owned receipts and real producer mutations. The receiver still
returns 405 for active-user PUT until that wiring is built. Complete connector audit/restore,
new-account lifecycle, dedicated administrator ordinary-workflow separation/mixed-use
migration, external writer coordination and production deployment remain gates. Continue
all other Ky/external product adapters and the unresolved task-category choice as well.
The full all-product objective stays active with no user-input blocker; this is native
interface completion, not production admission or a reduction of scope.


## Interruption recovery: resident Immich wiring — 2026-10-10

Recovered after the user reported termination for memory usage. The preceding
resident-wiring edits are present in `internal/offboard/target.go`, `runtime.go`,
`scim.go`, `runtime_test.go` and `runtime_directory_test.go`. Protected directory
roles now enable the native Immich directory consumer; account creation and group
provisioning remain unsupported. Immich SCIM userName is bounded informational
metadata, while the immutable subject/native UUID remains the account identity;
reads retain the opaque subject rather than inventing a native username.

The persisted serial, memory-capped offboard race log confirms PASS in 30.330
seconds: `/home/yoshi/.cache/kyquickstart-work/immich-resident-boundary.log`.
This verifies the package tests, not actual KyIdentity SCIM producer delivery or
the new resident path against native Immich. Do not restart the completed package
test merely because its terminal handle was lost.

Next: extend the disposable native bridge fixture to prove active profile/role
PUTs, retained immutable identity, durable results and refusal after revocation;
then qualify actual KyIdentity producer mutations. Update the offboard DOX and
runbooks that still describe directory support as Forgejo-only, and run appropriate
CI/build checks after completing that wiring. These edits are not production-ready
and have not been deployed. The prior native-interface checkpoint remains valid;
all-product implementation, lifecycle, administrator separation, backup/restore
and release gates remain open. Vaultwarden remains the only new password selection.

Keep builds and tests serial, use disk-backed scratch, and retain explicit Go,
Node and container memory limits. No unrelated host workloads were stopped.
This recovery changed only the durable checkpoint; root DOX and child indexes
remain unchanged because it adds no ownership, structure or operating contract.
The full goal remains active with no user-input blocker.


## Checkpoint: resident Immich profile/role path qualified — 2026-10-10

Completed protected Immich directory wiring through `Target`, runtime loading, full
active SCIM PUT and native profile reads. The runtime requires explicit distinct
administrator/everyday roles and exact installation/client scope; omission retains
revocation-only authority. Registration additionally proves the current owned native
directory receipt. Account creation and groups remain refused. Native unknown-write
errors become the bridge's durable directory uncertainty hold; observation does not
release that hold. No new runtime schema or binding shape was introduced.

Immich has no native username. Bounded incoming SCIM userName is informational metadata;
it never selects or rebinds an account. Exact subject/native UUID/client ownership stays
immutable, and reads expose the opaque subject as userName. Forgejo keeps its existing
pinned native username contract. This distinction matches the identity worker's retained
ID/externalId mapping; actual producer delivery has not yet been qualified for Immich.

Extended the separately compiled native bridge helper with explicit directory mode when
its parent enables native setup. It verifies foreign-subject/unknown-role refusal before
profile changes; actual name/email update and repeat with different username metadata;
native profile/role readback; administrator grant, empty-role demotion and restoration of
the original ordinary profile. The four native changes advance the parent's directory
revision from four to eight; repeats and refused requests do not advance it. The existing
unavailable revocation write then exercises durable backoff and actual worker retry.
After cancellation/join and reopening state, a newly constructed TLS receiver proves the
retained directory completion/audit proofs, active PUT refusal and inactive read. The
parent verifies old session/key/socket and fresh genuine OAuth denial, unrelated owner
access, retained native photo records/bytes and exact permanent fence.

The first native run failed at the final reopened-state read because the fixture reused
a receiver attached to the closed store. Reconstructing the receiver against the reopened
store fixed this test lifecycle bug; the complete run then passed. Failed evidence is
retained separately rather than being confused with the passing final log.

Verification: package race tests passed (offboard 30.366 seconds, Immich 1.580 seconds).
The complete real-KyIdentity SSH/setup/directory/bridge native race fixture passed in
110.468 seconds (109.43 seconds in the test). `make ci build` passed tidy, formatting,
vet including the e2e tag, all race tests and both workstation/resident binary builds;
offboard completed in 27.546 seconds. Whitespace checks passed. All test/build handles
are terminal; Docker confirms no owned Immich containers/networks remain. Tests and
builds were serial with disk-backed scratch and the existing explicit resource limits.
Logs in `/home/yoshi/.cache/kyquickstart-work/`: `immich-resident-unit.log`,
`immich-resident-native.log` (first failed fixture), `immich-resident-native-final.log`
(passing complete qualification), and `immich-resident-ci-build.log`.

The native source patch and immutable local overlay remain the exact directory artifacts
recorded above; no TypeScript/native image changed, and no source rebuild was necessary.
DOX updated offboard and adapter contracts/verification, offboarding/guided runbooks,
module README and catalog qualification. Root/adapter-parent/module AGENTS and child
indexes remain unchanged: their ownership and native module contracts already cover this
path, and no new boundary was created. No live service, data or credential changed.
All implementation remains uncommitted; no PR or release was created.

Next: qualify the actual KyIdentity SCIM producer with owned Immich bindings and real
profile/role/assignment mutations through the protected resident service. The current
fixture uses actual KyIdentity for sign-in but a controlled HTTP sender for SCIM; do not
claim event-worker completion. Preserve verified TLS and isolated Docker networking;
identity's container cannot reach host loopback, so its connector needs an explicitly
owned reachable endpoint and public trust before startup. Complete creation, directory
audit/restore/reconciliation, native writer coordination and administrator ordinary-use
separation remain gates. Continue the other product adapters, Vaultwarden-only password
selection, bulk backup/restore and release acceptance toward the full all-product goal.
No human-input blocker exists; the goal remains active and Immich is not production-admitted.

## Recovery checkpoint: actual Immich identity producer qualified, 2026-10-10

Recovered after the reported memory shutdown. The prior complete native test had finished:
`immich-producer-complete.log` records PASS, 169.85 seconds in the parent test and
170.885 seconds for the package. Its separately compiled resident producer helper passed
in 79.25 seconds. These saved terminal results supersede the preceding controlled-sender
checkpoint for registered-account producer qualification; the all-product goal remains open.

Actual KyIdentity worker events now qualify initial owned profiles, name/email changes,
administrator grant/removal, old-session administrator denial and app assignment loss.
Protected resident runtime registers both verified native bindings before TLS service starts.
The test inspects producer revisions and acknowledgments, exercises a failed native write
and durable retry, joins shutdown, reopens stored directory/revocation audit proofs and
checks permanent native session/key/socket denial. Accounts, owner access and photo bytes
remain intact. Retained authorization-code exchange and new identity authorization are
checked separately after assignment loss.

The fixture's initial host-gateway endpoint timed out from the owned identity container;
an initial private-state-directory permission error was also corrected. The final fixture
uses a generated/pinned SSH host key and raw TCP forwarding through a capped 64 MiB owned
SSH container on its isolated Docker network, preserving end-to-end resident TLS. Only
SSH publishes on host loopback; the relay has no Docker socket. Explicit remote forwarding
is enabled in this disposable SSH fixture. No host firewall rules or live service changed.

Producer delivery first passed while the parent exposed a genuine native OAuth error:
Immich wrapped a provider's refused code exchange as HTTP 500. The community source patch
now maps actual openid-client ResponseBodyError with 4xx invalid_grant/access_denied to
fixed-message HTTP 401, without logging provider diagnostics. Unexpected credential,
transport and server failures retain their error path. Five real-SDK token-endpoint tests
cover both denial codes and three unexpected failure cases; no broad 5xx acceptance was
added to the native test. The complete native parent requires HTTP 401.

Authoritative source.patch SHA-256:
`aa831cf0f1adf534fb557f472e7f24ef2b1c2982825e7e55bee34ff5a606cea7`.
Immutable local module image:
`sha256:ede45658e4e1ddedb8eeec380661ca970af3dfc66662b9fc93aa48179b245925`.
Pinned upstream source remains v3.2.0 commit
`1b6098c9dbfffe978bec2d414606ed7a4c8e019a`; earlier overlays mismatch this patch
and must not be used as evidence for the current module. Canonical disk-cache
`immich-module-image.id` now identifies this image. Exact patch application and reproduction
against a clean owned pinned checkout passed; its temporary worktree was removed.

Full native source type checking, lint, server build and 119 selected upstream tests passed
(auth 86, admin 14, CLI 14, OAuth repository 5). Following recovery, `make ci build` passed
tidy, formatting, both vet modes, all race tests and both binary builds; offboard took
28.956 seconds. A separate synthetic-provider setup/bridge regression against the new
image passed in 99.471 seconds (98.43 in the test), including signed back-channel logout
and native permanent revocation with retained photos. Builds/tests were serial with
GOMAXPROCS=2, GOMEMLIMIT=1GiB, GOFLAGS=-p=1 and disk-backed scratch; existing capped
Node/container limits were retained. No running test handle remains.

Logs are under `/home/yoshi/.cache/kyquickstart-work/`: `immich-producer-complete.log`,
`immich-producer-typecheck-final.log`, `immich-producer-lint.log`,
`immich-producer-source-unit.log`, `immich-producer-source-build.log`,
`immich-producer-image-build.log`, `immich-producer-ci-build.log`, and
`immich-producer-synthetic-regression.log`. Earlier failed runs are separate evidence.

DOX contracts/runbooks were updated for the actual producer fixture, registered-account
qualification and native OAuth denial behavior. The root AGENTS.md and child indexes
remain unchanged because ownership and indexed boundaries have not changed. All work
remains uncommitted; no PR, production release or live deployment was created.

Next: complete Immich new-account provisioning and global multi-connector lifecycle,
audit/restore/reconciliation, native-writer coordination and administrator ordinary-use
separation; continue every remaining supported product adapter and the suite bulk
backup/restore and release gates. Vaultwarden remains the only new password selection;
existing KeePass-based KyVault data stays untouched. Runtime domains, edge choices and
target credentials do not block development. Immich remains production-gated, and the
all-product installer request is unfinished.

## UUID creation-state checkpoint, 2026-10-10

The preceding goal turn made progress by recovering terminal native results, verifying the
rebuilt module and repository, and mirroring checkpoint 2624. This continuation changed
the durable creation path toward Immich provisioning; the full objective remains active.

`internal/offboard/creation.go` now validates uncreated scopes and immutable verified
receipts for both numeric Forgejo IDs and Immich UUIDs. Scopes must contain neither native
identity. A validation-only probe reuses the existing binding boundary validator; it never
enters state or native requests. Successful native receipts must retain exact installation,
app/provider/client/subject scope and the product's disjoint identity shape. Restart and
partial-publication repair reuse the existing audit/proof path without replaying creation.

`scim_creation.go` handles the UUID scope without storing informational userName; Immich
creation responses retain the opaque subject as userName. No protected production target
currently enables this lane: NewTarget and LoadRuntime still refuse Immich creation mode.
The native API, adapter dispatch and actual worker creation fixture remain unfinished.
This is internal durable storage/transport support, not native creation qualification.

New tests exercise a valid UUID receipt followed by failed binding publication, restart,
repair without another create, SCIM initialization before 201, reopened completion proofs
and refusal after deletion of the immutable result audit. Separate cases prove uncertain,
foreign-client, mixed-ID and missing-UUID receipts remain held through restart and later
profiles, with no binding or replay. Scope tests refuse caller-supplied native identities,
unsupported product kinds and malformed client pins. Existing Forgejo creation tests remain
unchanged and pass through the same code.

Focused creation race tests passed in 1.208 seconds. After the final scope cases,
`make ci build` passed tidy, formatting, both vet modes, all package race tests and both
binary builds; offboard took 31.448 seconds. Logs:
`/home/yoshi/.cache/kyquickstart-work/immich-uuid-creation-unit.log` and
`/home/yoshi/.cache/kyquickstart-work/immich-uuid-creation-ci-build.log`.
Checks were serial with the established Go memory/CPU limits and disk-backed scratch;
all handles are terminal. No native image/source patch or live service changed.

DOX updated `internal/offboard/AGENTS.md` for internal UUID receipt behavior and the
controlled-callback verification limits. Root/adapter/module docs and indexes intentionally
remain unchanged: their public runtime/native contracts and ownership have not changed.
All work remains uncommitted, with no PR or release created.

Next concrete work: add product-owned atomic Immich creation/absence and creation ownership
receipts, then consume them through verified HTTPS before opening the protected runtime
opt-in. Current `SystemMetadataRepository.projectKyQuickStartDirectory` explicitly allows
only installation.administratorId/everydayId; a new account must prove independent durable
native creation ownership rather than relaxing this check to provider/email matches.
Preserve exact installation/provider/subject, collision refusal, ordinary initial authority,
separate audited administrator projection and uncertain-outcome holds. Qualify native
creation/repeat/conflicts and actual KyIdentity new-assignment delivery before admission.
Continue remaining products and suite lifecycle/backup/release gates; no human-input blocker
exists, and Vaultwarden-only password selection remains in force.

## Native Immich creation interface checkpoint, 2026-10-10

The preceding goal turn made progress by implementing and verifying UUID creation state.
This continuation built and qualified the missing product-owned native creation interface
and its HTTPS consumer. The full all-product installer goal remains active and unfinished.

The community source extension now exposes GET/POST
`/api/admin/users/kyquickstart/creation` with exact installation/issuer/client/subject scope.
Both require native administrator API-key authority and the corresponding read/create
permission; session-only administrator access is refused. Subject absence includes distinct
retained native metadata owners, so unlinking does not authorize adoption. Existing subjects
and email collisions refuse creation. A serializable transaction creates one passwordless
ordinary account, its cluster group, revision-zero directory metadata and immutable
`ky.immich-creation/1` ownership metadata. The latter binds native UUID, original profile,
installation/provider/subject and a verified intent digest.

Exact native repeats retain the UUID and verify actual current directory/profile/authority;
they do not restore initial names or roles. Directory inspection/projection now accepts
either the original setup IDs or an independently verified creation receipt, not arbitrary
provider/email matches. Created accounts retain the ordinary initial role; later administrator
projection remains separate. Revoked/deleted/unreceipted/conflicting accounts refuse repeat.
No reactivation or content-deletion operation was added.

`internal/adapters/immich/creation.go` requires product-owned absence before a POST, then
verifies the creation receipt, exact initial ordinary profile/revision and native access state
before returning its UUID. Missing/foreign/contradictory proofs and lost/unverified writes
never publish a binding. The native client's private refusal type preserves HTTP status
without exposing server bodies or changing its generic error text. Definite 400/403/409
refusals remain conflicts; transport/server/verification failures require reconciliation.

Review caught and corrected an important commit-outcome distinction before final image
qualification: the new service initially wrapped every repository exception as 409, which
could misclassify a database commit whose acknowledgment was lost. It now returns a fixed
409 only for explicit precondition conflicts, and fixed-message 503 for unexpected native
failures. Three service tests prove session-only refusal before I/O, known-conflict handling
without diagnostics and unknown commit acknowledgment held as unavailable. The first
type check exposed the upstream repository mock's missing method; that was added. Lint
also caught three array-length checks; the final source passes both checks.

`KYQ_TEST_IMMICH_CREATE=1` requires setup/module mode. Its native qualifier proves scoped
installation/provider/subject/email/role collision rollback, session-only authority refusal,
verified ordinary creation, exact repeat, subsequent name/email/administrator projection
without repeat rollback, existing-account retention and permanent revoked-account refusal.
The preliminary synthetic setup/bridge fixture passed in 84.870 seconds (83.83 in the test);
that intermediate image predates the commit-acknowledgment correction and is superseded.

Final source type checking, lint, server build and 122 selected upstream tests passed:
auth 86, admin 17, CLI 14, OAuth repository 5. The final unit run used one Vitest worker
with file parallelism disabled. Exact patch application and byte-for-byte reproduction from
a clean pinned checkout passed for all 17 changed files; the owned temporary worktree was
removed. The final complete race fixture, with actual KyIdentity, SSH integrate-stack,
native creation and the registered-account resident producer, passed in 168.086 seconds
(167.06 in the parent, 93.91 in its separately compiled producer helper). It reverified
real PKCE/Assigned users only, profile/role delivery and acknowledgments, scoped native
session/key/socket revocation, retained owner/account/photo bytes and reopened runtime proofs.

Authoritative patch SHA-256:
`43cc55c08cc00572e4993eb4ca07642948937dc44adfd33e9f2ebc31892ae0b3`.
Final immutable local module image:
`sha256:03d5619eaa604ea672ae305ef98ebaf46c071d74df32bec9fda175577862d6b9`.
Source remains upstream v3.2.0 commit `1b6098c9dbfffe978bec2d414606ed7a4c8e019a`.
Disk-cache `immich-creation-final-image.id` and canonical `immich-module-image.id` now name
the final image. Earlier overlays mismatch the current patch and cannot qualify it.
The official base/dependencies remain pinned, with no new package dependency or production
image/release. SDK source did not change; previously built SDK outputs were reused.

Final `make ci build` passed tidy, formatting, both vet modes, all package race tests and
both binary builds; Immich took 1.290 seconds and offboard 31.618. Whitespace checks passed.
All handles are terminal and no owned Immich containers/networks remain. Tests/builds were
serial under the established Go/Node/container limits and disk-backed scratch.
Logs under `/home/yoshi/.cache/kyquickstart-work/`: `immich-creation-unit.log`,
`immich-creation-typecheck-complete.log`, `immich-creation-lint-complete.log`,
`immich-creation-source-unit-complete.log`, `immich-creation-source-build-complete.log`,
`immich-creation-image-build-complete.log`, `immich-creation-native.log` (superseded
preliminary native evidence), `immich-creation-native-producer-complete.log` (final),
and `immich-creation-ci-build.log`.

DOX updated adapter-parent/Immich/module ownership, contracts and verification, plus the
module README; stale initial-account-only directory language was removed. Root/offboard
docs and child indexes intentionally remain unchanged: public runtime creation is still
closed and no owning boundary/index changed. No live service, data or credential changed.
All implementation remains uncommitted; no PR, deployment or production release was created.

Next: wire the native consumer into protected Immich creation target/runtime mode and
qualify actual KyIdentity new-assignment creation, subject-bound SSO, revocation and reopened
creation/directory proofs. The current complete fixture uses real identity for the existing
accounts but controlled API requests for the additional created account; do not claim actual
new-account worker delivery. NewTarget and LoadRuntime still refuse Immich create_accounts.
Coordinate native writers before production: inspected upstream oauthId has no unique column
constraint and first-login auto-registration is enabled by initial setup, so concurrent stock
registration must not race owned provisioning. Complete global multi-connector lifecycle,
audit/recovery/reconciliation, administrator purpose separation and remaining product,
bulk-backup/restore and release gates remain. No human-input blocker exists.


## Recovery checkpoint: protected Immich worker creation qualified, 2026-10-10

Recovered after the reported memory termination. The completed final native test log survived;
its terminal PASS was inspected before repeating any work. The all-product objective remains
active. This checkpoint supersedes the previous statement that Immich runtime creation is closed.

Protected runtime now accepts Immich `directory.create_accounts:true` as a separate per-app
opt-in, with explicit role mapping and installation ownership. Omission leaves creation
disabled. Subject absence is scoped to installation/issuer/client/subject; creation returns
a verified immutable native UUID and ordinary profile receipt before a separate audited role
projection. Email/username never authorizes adoption. Known refusals remain conflicts; unknown
writes retain creation uncertainty and require reconciliation. No runtime schema change or
inline credential was introduced. Unit coverage verifies the opt-in and read-only loading.

The final actual KyIdentity producer fixture passed in 168.451 seconds (parent 167.41,
joined resident helper 102.22). A previously unassigned owned identity was assigned, the real
identity worker created an ordinary native account, and its durable acknowledgment followed
verified creation/initialization. Subject-bound PKCE sign-in and native session/API-key use
passed. Actual assignment loss revoked both credentials while retaining the profile and
immutable ownership. Joined shutdown reopened creation, initialization and revocation proofs.
The same run reverified initial account profile/role delivery, administrator demotion,
unavailable-write retry, scoped session/key/socket denial and retained owner/account/photo
bytes. New-account profile retention is verified; new-account photo retention was not a
separate scenario. Logs: `immich-worker-creation-native-final.log`.

The first complete attempt failed at the newly created ordinary account's OAuth callback
(401); its subsequent helper missing-proof failure followed the interrupted parent scenario.
KyIdentity correctly emits an empty application-role array for an assigned ordinary account.
The module incorrectly required a nonempty role even for a verified non-administrator
directory receipt. Owned authentication now permits missing/empty roles only for a matching
ordinary receipt; missing administrator authority, stale grants and unknown/malformed values
remain denied. Nine role cases prove no native updates. Full source type checking, changed
source lint, server build and 131 selected upstream tests passed (auth 95, admin 17, CLI 14,
OAuth repository 5), with one Vitest worker and file parallelism disabled. Source SDK did not
change; existing built SDK outputs were reused. Clean pinned patch application and
byte-identical reproduction passed for all 17 source files; the owned temporary checkout
was removed. The failed first log is `immich-worker-creation-native.log`, superseded by the
final PASS above.

Authoritative patch SHA-256:
`a6bab1732771928378b5a91aa67d644cfac229f696cd851572354dcdbdfde6a2`.
Immutable local overlay:
`sha256:045db7de24847dbf5fe3e3635e9cf615a4931da1c5c98b03d1fc955be93d4ccd`.
Pinned upstream remains v3.2.0 commit `1b6098c9dbfffe978bec2d414606ed7a4c8e019a`.
Disk-cache `immich-worker-creation-image.id` and canonical `immich-module-image.id` both
name this current image. Earlier images mismatch the current patch. No production release
or dependency change was made. Static fixture identity binary remains the previously
qualified local build, not a released artifact.

Final `make ci build` passed tidy, formatting, both vet modes, every package race test and
both binary builds; offboard 31.293 seconds, Immich 1.377. Whitespace checks passed. The
final test/build handles are terminal and no matching owned Immich test containers remain.
Builds/tests stayed serial: Go GOMAXPROCS=2/GOMEMLIMIT=1GiB/GOFLAGS=-p=1, Node bounded
heap and one test worker, disk-backed TMPDIR and established per-container CPU/memory caps.
The Go heap limit is soft; container caps and serial operation remain necessary.
Logs under `/home/yoshi/.cache/kyquickstart-work/`: `immich-worker-creation-unit.log`,
`immich-worker-creation-typecheck.log`, `immich-worker-creation-lint.log`,
`immich-worker-creation-source-unit.log`, `immich-worker-creation-source-build.log`,
`immich-worker-creation-image-build.log`, both native logs above and
`immich-worker-creation-ci-build.log`.

DOX updated the offboard and Immich/module contracts and verification; module README,
guided setup, offboarding and catalog evidence describe the opt-in and actual new-assignment
proof. Root and adapter-parent docs/indexes remain unchanged because ownership boundaries
and their existing contracts still apply. All implementation remains uncommitted on the
existing branches. No PR, live deployment, service data or credential change occurred.

Next: coordinate native account writers/first-login registration before production, then
qualify global multi-connector disablement and complete audit/restore/reconciliation. Stock
oauthId has no unique constraint and initial setup enables first-login auto-registration;
this local creation success does not prove safety against concurrent native writers.
Administrator ordinary-workflow separation, remaining product adapters and admission gates,
bulk backup/restore and release/full-installer acceptance remain in scope. This Immich
milestone does not complete the all-product request. No human-input blocker exists.


## Managed Immich OAuth provisioning boundary qualified, 2026-10-10

Previous goal turn was progress: recovered the completed native fixture, completed CI/build
and mirrored checkpoint 2632. This turn traced native first-login/email linking and explicit
OAuth identity changes before editing. The current managed installation marker now requires
an existing native OAuth account and owned directory receipt before login. A signed unknown
subject cannot reach email adoption or first-login registration, even when autoRegister is
true. An existing unreceipted account is refused. Managed link/unlink routes refuse before
provider exchange or session/account writes. Metadata read failures propagate and do not
fall back to stock registration. Without the marker, upstream behavior is retained.

This uses the existing system metadata ownership marker and native auth service, with one
bounded marker-existence query; no dependency, auth proxy, migration or new shared lock.
Native administration DTOs inspected in this turn do not expose oauthId; the normal writer
paths are OAuth callback/link/unlink and the bulk reset command. Bulk reset, direct external
writes, configuration replacement and operations already in flight at initial setup still
need coordination. The upstream oauthId column remains nonunique. Do not interpret this
login boundary as a database uniqueness constraint or complete native-writer qualification.

Three focused source tests prove refusal before native writes for first login/email adoption,
unreceipted accounts and link/unlink. Together with the existing owned role tests, all 134
selected upstream tests passed: auth 98, admin 17, CLI 14, OAuth repository 5. Full source
type checking, changed-source lint and server build passed with capped Node heaps; Vitest
used one worker and disabled file parallelism. SDK source/dependencies did not change.
The patch applied to a clean pinned checkout and reproduced all 17 changed files exactly;
the owned check worktree was removed.

The synthetic native setup/creation/resident-bridge fixture passed in 77.913 seconds
(parent 76.89). A genuine signed unprovisioned subject received 401 and native subject
absence remained true afterward. Native link/unlink returned 403 and preserved the
managed everyday binding. Existing back-channel logout, directory projection, permanent
revocation, session/key/socket denial and owner/account/photo retention checks also passed.
The separately run actual KyIdentity/SSH installer/new-account producer fixture passed
in 170.383 seconds (parent 169.36; joined resident helper 98.92). It reverified actual
assignment-driven ordinary creation, subject-bound PKCE sign-in, profile/role changes,
assignment-loss revocation, retained content and reopened creation/directory/revocation
proofs. Native link/unlink refusal also passed with actual identity. Synthetic first-login
refusal is a native product check; do not mislabel that particular scenario as actual
KyIdentity unprovisioned-assignment delivery. Both runs used the same current immutable image.

Current patch SHA-256:
`a1b9a274ca20682044d7327bf992302be0076d2675cf8a590b1a72bb0f25411f`.
Current immutable local fixture image:
`sha256:a341f683688166bea6be22659e8bc33b55d118609bb95c900ce55da083fdffae`.
Pinned upstream source remains v3.2.0 commit `1b6098c9dbfffe978bec2d414606ed7a4c8e019a`.
Disk-cache `immich-managed-oauth-image.id` and canonical `immich-module-image.id` name
this image; earlier images mismatch the new source patch. No production release/image
provenance claim is made. Actual identity remains the explicitly selected unreleased local
fixture build.

Final `make ci build` passed tidy, formatting, both vet modes, every package race test
and both workstation/resident builds; offboard 29.038 seconds, Immich 1.371. Whitespace
checks passed. Test/build handles are terminal and no owned fixture containers remain.
Source build, synthetic native test, actual producer test and installer CI/build ran serially
under the established Go/Node/container limits with disk-backed scratch.
Logs under `/home/yoshi/.cache/kyquickstart-work/`: `immich-managed-oauth-format.log`,
`immich-managed-oauth-source-unit.log`, `immich-managed-oauth-typecheck.log`,
`immich-managed-oauth-lint.log`, `immich-managed-oauth-source-build.log`,
`immich-managed-oauth-image-build.log`, `immich-managed-oauth-native-synthetic.log`,
`immich-managed-oauth-native-producer.log`, `immich-managed-oauth-ci-build.log`.

DOX updated Immich/module contracts and verification, module README, guided setup,
offboarding and catalog evidence. Root Work Guidance now records serial memory-bounded
verification and disk scratch so recovery sessions retain this operating rule. Adapter-parent
ownership and child indexes remain unchanged because boundaries did not move. Shared suite
text above the KyQuickStart heading did not change. All code remains uncommitted on the
existing branches; no PR, live deployment, service data or credential change occurred.
No callable salt.md MCP connector was exposed; no standing block was installed without
reading workspace rules. Repository evidence and the existing myslop claim remain the
shared recovery record. This tooling absence does not block implementation.

Next: qualify global disablement across owning connectors, using the existing native
identity/adapter fixtures under the memory limits, and continue the all-product setup gaps.
Native external-writer coordination, complete connector audit/restore/reconciliation and
administrator purpose separation remain gates. Product-owned recovery enrollment, initial
service configuration, bulk backup/restore and release/full-installer acceptance still need
completion across the full suite. Do not narrow the goal to Immich or declare the installer
ready from these passing local milestones. No human-input blocker exists.


## Global disablement: multi-connector producer and native Immich checks, 2026-10-10

Previous goal turn was progress: implemented and qualified the managed Immich OAuth
provisioning boundary, completed source/native/installer checks and mirrored checkpoint
2633. This turn adds missing global-disablement evidence without changing live services.

KyIdentity worktree now has `internal/sync/installer_multi_connector_test.go`. Native
ApplyInstallerSetup creates three owned clients/connectors with distinct bearer tokens: two
own the ordinary account and one is management-only. Separate SCIM resource stores behind
app-specific verified TLS endpoints receive only their own credentials and roles. Two cases
prove direct global disablement and single-app assignment loss followed by global disablement.
App loss deactivates only its connector; global disablement deactivates both held resources,
retains their exact connector-local IDs/mappings and records separate delivered acknowledgments.
A recreated dispatcher uses the existing store/key rather than old engine memory. The
management-only connector never receives an ordinary account or create marker, and the
unassigned global administrator never reaches any target. Independent administrator resources
stay active. The receivers are SCIM wire/resource fixtures, not native products. The store
connection is not reopened; do not claim a database-restart or multi-product native proof.

The initial new-test assertion misread SCIMUserLink's boolean as an uncertainty flag. Reading
the implementation showed it means a mapping row exists, including a completed mapping.
The assertion now requires presence with the exact remote ID. No production bug or fan-out
implementation change was needed. Focused race test passed in 4.883 seconds. The entire
KyIdentity `internal/sync` package passed uncached race tests in 64.770 seconds, and vet passed.
Commands use the explicit disk-cache `identity-fixture.work` with the local KyIdentity and
ky-primitives worktrees; this remains unreleased development-source verification.

The actual Immich producer fixture now disables its newly created ordinary identity globally
through operation-bound admin PUT `/api/admin/users/<subject>` with `status:disabled`, rather
than removing that new account's app assignment. It waits for the real identity worker's
new inactive acknowledgment, verifies native old-session/API-key denial, zero credentials,
permanent fence and retained profile/revision/UUID ownership. The existing-account portion
continues to remove the app assignment and proves the separate scoped cause, unavailable-write
retry, socket/session/key denial, retained photos and owner access. Joined helper shutdown
reopens creation/initialization/directory/revocation proofs. No controlled SCIM sender replaces
identity in this mode.

The full actual KyIdentity/SSH installer/native creation/global-disablement/resident fixture
passed in 171.358 seconds (parent 170.33, joined helper 103.57). This proves native global
revocation through one Immich connector, separately from the multi-connector wire test. These
are complementary checks; they do not yet prove one actual identity disablement reaching two
independent native products through the resident runtime. That full native fan-out gate remains.

Immich source patch/image did not change. Current patch SHA-256 remains
`a1b9a274ca20682044d7327bf992302be0076d2675cf8a590b1a72bb0f25411f`;
canonical local image remains
`sha256:a341f683688166bea6be22659e8bc33b55d118609bb95c900ce55da083fdffae`.
Actual identity uses the previously selected local static fixture binary, not a production
release. No runtime source rebuild was required for the test-only KyIdentity addition.

Final installer `make ci build` passed tidy, formatting, both vet modes, all package race
tests and both binaries; offboard 29.037 seconds. Whitespace checks passed in both repositories.
All test/build handles are terminal; no owned native fixture containers remain. Heavy builds
and tests stayed serial under the established Go/container limits and disk-backed scratch.
Logs under `/home/yoshi/.cache/kyquickstart-work/`: `identity-multi-connector-focused.log`,
`identity-multi-connector-sync-race.log`, `identity-multi-connector-vet.log`,
`immich-global-disable-native.log`, `global-disable-installer-ci-build.log`.

DOX updated KyIdentity root verification for the multi-connector producer test and its limits.
Immich/module/offboard verification and module README, guided setup, offboarding and catalog
evidence now distinguish new-account global disablement from existing-account assignment loss.
KyQuickStart root/adapter-parent and child indexes remain unchanged because ownership and
operating contracts did not move. Workspace shared rules did not change. All work remains
uncommitted on the existing branches; no PR, deployment, live data or credential change occurred.

Next: compose independent native targets behind the protected resident runtime and prove
one real identity global disablement reaches every held connector, with retained identities,
credential denial and reopened per-app proofs. Preserve app-only assignment-loss isolation.
Then continue the full product-owned setup/recovery/initial-configuration gaps, audit/restore/
reconciliation, dedicated administrator separation, bulk backup/restore and release/full-
installer acceptance. These qualification milestones do not complete the all-product goal.
No human-input blocker exists.


## 2026-10-10 memory recovery and mixed native connector checkpoint

Recovered the interrupted session from this ledger and disk-backed logs. No old native
process was still running. The diagnostic run had failed definitively at Forgejo
`POST /user/repos`: the disposable ordinary token lacked native `write:user`. Corrected
only that fixture token scope; retained ordinary repository creation rather than substituting
administrator creation. The earlier first/diagnostic failures are not lifecycle passes.

Added `native_multi_test.go` and optional `KYQ_TEST_IMMICH_MULTI=1` (requires producer/create).
One actual disposable KyIdentity has separate Immich/Forgejo OIDC clients, assigned-only apps,
connectors and SCIM credentials. One protected resident runtime loads distinct native targets.
The actual identity worker creates the same ordinary subject in native UUID Immich and numeric
Forgejo; real sign-in and API credentials are usable before one global disablement. Both old
sessions/keys lose access, profiles/ownership remain, and the exact ordinary-owned Forgejo
repository ID survives. Subsequent Immich-only assignment loss leaves the other user's Forgejo
account and existing session usable. Joined shutdown reopens numeric and UUID
creation/directory/revocation audit/completion proofs and checks that no Forgejo revocation
job exists for the app-only loss. No production runtime or product source changed this checkpoint.

Authoritative verification:
- Full race-enabled actual native fixture PASS: package 177.135s, parent 176.09s,
  resident helper 105.23s. Log: `/home/yoshi/.cache/kyquickstart-work/multi-native-global-final.log`.
- `make ci build` PASS: tidy, formatting, ordinary/e2e-tag vet, all package race tests,
  both workstation/resident binaries. Offboard 29.338s.
  Log: `/home/yoshi/.cache/kyquickstart-work/multi-native-final-ci.log`.
- `git diff --check` PASS; owned native containers cleaned up.
- Immutable Immich image remains `sha256:a341f683688166bea6be22659e8bc33b55d118609bb95c900ce55da083fdffae`;
  patch remains `a1b9a274ca20682044d7327bf992302be0076d2675cf8a590b1a72bb0f25411f`.
  Forgejo is `16.0.5-rootless@sha256:5effb7305584aca479b29fde6f9631a6dbe86ae798ae02eeea33a3666f0c0bf8`.
- Serial execution, disk TMPDIR, Go two-process/1GiB soft heap target retained;
  additional Forgejo container has explicit 512MiB/one-CPU/no-swap-growth limits.

DOX pass: Immich owning doc indexes the cross-product fixture and verification; offboard
owning doc records reopened mixed-target proofs. Guided/offboarding/module docs distinguish
this pair from full-suite coverage. Root and adapter parent indexes and module contracts
remain unchanged because ownership boundaries and production interfaces did not change.

Still unfinished: all-product adapter goal, full-suite connector inventory/lifecycle coverage,
native-writer coordination, held-outcome reconciliation, complete creation/directory/group
audit relay and restore, dedicated administrator purpose separation, production release/admission
and installer deployment/acceptance. Vaultwarden only remains the password selection; old
KyVault data/services stay untouched. No live changes, deployment, release, PR or commit.
Next continue outstanding product adapters and lifecycle gates from the inventory above;
this mixed-native pair is a milestone, not completion of the all-product request.


## 2026-10-10 BookStack live setup profile readback

Previous goal turn was progress: the mixed native Immich/Forgejo lifecycle fixture and
repository checks passed. Rechecked the adapter inventory and authoritative source. BookStack
account setup inspection omitted live name/email, so retained operator edits were invisible
in its report. Extended the native module and Go consumer, without adding native profile writes.

Module 0.7.0 now returns `ky.bookstack-account-report/2` with required live name/email.
The request and immutable ownership/assignment receipts retain `ky.bookstack-account/1`;
existing intent is not rewritten. The Go boundary validates profile types/limits/format,
refuses missing or older incomplete reports, and requires newly created profiles to match
input. Retained checks/repeats return actual current profiles while preserving ownership,
passwords, roles and revocation. This is setup readback, not an active directory projection.
Scoped offboarding remains the existing route/contract; its Go consumer and controlled bridge
fixtures now require module 0.7.0. Older native module versions are deliberately refused;
no live module was changed or upgraded.

Verification:
- BookStack adapter race boundary tests PASS, 1.040s, including missing/malformed live
  profiles, old report-contract refusal and contradictory new-account profile rejection.
  `/home/yoshi/.cache/kyquickstart-work/bookstack-profile-boundaries.log`.
- Digest-pinned native BookStack/MariaDB qualification PASS, native Go helper 5.50s /
  package 6.512s. Native API name/email edits followed by check/create/check preserve
  changed values and exact ID/ownership. Existing private-file/source/intent conflicts,
  lost-result resume, retained revocation, old web/API/new-login denial, concurrent retry,
  restart and recreated-subject denial also pass. PHP lint runs in the native image.
  `/home/yoshi/.cache/kyquickstart-work/bookstack-profile-native.log`.
- Actual KyIdentity OIDC qualification PASS with the existing static development binary:
  native owned setup/check/resume, exact subject-bound PKCE, owner administration and Go
  verification, everyday/unassigned denial, audited step-up assignment removal and adapter
  revocation with retained content. HTTPS helper 1.305s; account verification 1.354s.
  `/home/yoshi/.cache/kyquickstart-work/bookstack-profile-identity.log`.
  Assignment loss alone still does not deliver native revocation through the identity worker.
- Final `make ci build` PASS: tidy, formatting, ordinary/e2e-tag vet, all package race
  tests and both binaries; offboard 31.644s. `git diff --check` PASS.
  `/home/yoshi/.cache/kyquickstart-work/bookstack-profile-final-ci.log`.
- All execution serial with disk TMPDIR and bounded Go heap/parallelism. Native BookStack
  fixture containers now cap application 1GiB, MariaDB 768MiB, actual identity 512MiB,
  TLS gateway 256MiB, one CPU each and no swap growth. Owned fixtures cleaned up.

DOX pass: BookStack adapter/module owning docs and module README record versioned live
readback, immutable receipt compatibility and native resource limits. Catalog design reflects
current readback scope without admitting production installs. Root/adapter/offboard parent
contracts and child indexes remain unchanged: production transport, state ownership and
scope boundaries are unchanged; offboard edits only update controlled fixture wire versions.

All-product goal remains active. BookStack active-profile/group provisioning, creation policy,
actual identity-worker delivery, dedicated administrator ordinary-workflow separation,
restore, guided deployment and release provenance remain open. Other product recovery/Yard
initial configuration, full-suite identity composition, sealed-secret/bulk restore and release
acceptance remain in scope. Next build native ongoing BookStack profile/role ownership and
projection before enabling its directory runtime; do not call this readback a full connector.
Vaultwarden only; excluded KyVault and existing services/data stay untouched. No live
service, deployment, commit, PR or production release changed.


## 2026-10-10 BookStack native ongoing profile interface

Previous goal turn was progress: versioned live setup readback and native/identity checks
passed. Continued the actual missing native interface rather than enabling an unsupported
bridge lane. Extracted UserRepo/OidcService sources from the pinned image using an owned
non-running capped container, removed immediately; source copies are disk-backed under
`/home/yoshi/.cache/kyquickstart-work/bookstack-source`.

Module 0.8.0 adds `kyquickstart:profile` / `ky.bookstack-profile/1` in `module/profiles.php`
and the Go native consumer in `profiles.go`. Explicit initialization pins the exact current
name/email of an already receipt-owned active native account. It cannot adopt an email/name
collision or change a profile to make initialization succeed. One current receipt and an
immutable audit setting per revision bind setup, native ID, issuer/client/subject and profile.
Updates compare the expected revision and commit native UserRepo name/email, receipt and
audit together. Check is read-only, requires the current audit proof and permits retained
revoked profile inspection. Exact desired repeats preserve revision. The consumer inspects
before and after writes; retry after a lost reply observes the committed desired profile
without another revision. Foreign/stale identities, profile drift, email collisions, missing
proof and revoked writes refuse. This is a profile interface; no administrator role is granted
or projected, and the existing HTTP offboarding token gains no new authority.

The shared native UserRepo writer checks the profile receipt under the same user-row database
transaction as initialization/projection. Once initialized, name/email/external subject edits
and native deletion return 403 before mutation. Unrelated native fields and uninitialized
accounts retain native behavior. Actual OIDC resolves the owned user and leaves projected
profile fields intact. Other native writers and role ownership remain explicit gates.

The first native test reached successful profile delivery but correctly failed its overall
run when BookStack rendered native UserUpdateException as HTTP 500. Corrected the shared
writer refusal to a framework HTTP 403 and serialized its receipt check with native profile
initialization. The failed run is not recorded as a lifecycle pass.

Authoritative verification:
- Pinned BookStack/MariaDB native adapter fixture PASS, with profile helper 4.26s /
  package 5.270s in `/home/yoshi/.cache/kyquickstart-work/bookstack-owned-profile-audit.log`.
  Explicit initialization, CAS update, stale/foreign/email-collision refusal, stable repeat,
  lost-result resume and retained revoked readback pass. Fault injection occupies the next
  native audit key: publication failure rolls back the preceding user-profile write.
  Removing current audit proof makes inspection fail. Native edit/delete APIs return 403
  and preserve the account/profile. Existing old web/API/new-login denial, retained content,
  concurrent offboarding retry, restart and recreated-subject denial also pass. PHP lint runs
  inside the pinned image.
- Actual KyIdentity fixture PASS: projected name/email survives fresh real PKCE/OIDC login,
  everyday authority stays ordinary, owner grant/everyday and unassigned denial remain valid,
  and native scoped HTTP revocation retains identity/content. HTTPS helper 1.315s;
  account verification 1.360s. Log:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-owned-profile-identity.log`.
- Final `make ci build` PASS, including all profile wire/refusal tests, all package race
  tests, tidy/formatting/ordinary and e2e-tag vet, and both binaries.
  `/home/yoshi/.cache/kyquickstart-work/bookstack-owned-profile-final-ci.log`.
  `git diff --check` PASS; owned fixtures cleaned up.
- Serial disk-backed scratch and established Go/container caps retained. No live target used.

DOX pass: BookStack adapter/module owning docs index the native profile consumer/command,
transaction/audit/guard boundaries, explicit initialization and remaining role/runtime gates.
Module README documents the command and backup members. Offboarding runbook and catalog
spec distinguish this interface from an operational directory receiver. Root/adapter parent
and offboard owning contracts/indexes are unchanged: native transport and bridge configuration
scope did not change; offboard test edits update only controlled wire-version expectations.

All-product objective remains active. Next implement owned BookStack role projection and
signed-login authority consistency before exposing protected profile/role HTTP transport and
enabling its resident directory runtime; then qualify actual identity-worker delivery.
Do not treat the existing revocation-only receiver as a complete connector. Creation/group
lifecycle, dedicated administrator ordinary-workflow separation, native alternate writers,
audit delivery and restore/guided deployment/release remain open. Other suite recovery/Yard
initial configuration, full-suite lifecycle/sealed-secret/bulk restore and installer acceptance
remain in scope. Vaultwarden only; excluded KyVault and existing services/data untouched.
No live deployment, commit, PR or production release changed.


## 2026-10-10 checkpoint: BookStack 0.9 owned roles and memory-kill recovery

BookStack community module and Go consumer now expose explicit native role initialization,
inspection and administrator projection (`ky.bookstack-roles/1`). Ordinary role IDs, native
claim/default mapping, role definitions and permissions are pinned. Initial administrator
capture requires source-bound verified native OIDC authority. Updates use the native writer
and one user-row transaction for membership, current receipt and immutable revision audit.
Stale/foreign/drifted/missing-proof and revoked writes refuse. Unknown outcomes inspect before
retry; exact repeats preserve revision. Profile and role receipts have separate owned keys.

Managed native role/subject edits and deletion return 403. After native signed-token
verification, login claims must agree with projected authority; managed native group sync
is skipped. Missing/empty roles permit ordinary access only; unknown, duplicate, mixed-type,
scalar and stale administrator claims refuse. Authenticated requests verify current native
role policy and audit proof. The existing scoped revocation token gains no new authority.
This remains a native-command interface, not operational BookStack directory delivery.

Qualification corrections: initialization initially missed upstream display-name/default
role mapping; it now matches actual native mapping and pins definitions. The demotion helper
initially reused ordinary-account verification while the account legitimately held a projected
administrator grant; it now uses exact owned role-source verification. Helpers require explicit
PASS, never a skipped test. Synthetic PKCE counts now follow every attempted authorization.
Those failed intermediate runs are not lifecycle passes.

Authoritative evidence:
- Native profile/retained-account regression PASS, package 5.286s:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-owned-roles-profile-regression.log`.
- Final signed-provider fixture PASS, including malformed/missing/empty claims and exact
  authorization/exchange counts:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-owned-roles-signed-verified.log`.
- Final actual KyIdentity fixture log ends in PASS, including grant/demotion, lost-result
  recovery, audit-failure rollback, old-session authority changes, real signed-role consistency
  and retained revoked-account proof:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-owned-roles-identity-verified.log`.
  Following harness restart its old process handle was unavailable; the complete saved log
  was checked rather than inventing a recovered process exit code. Grant package 2.369s,
  demotion 2.718s, retained phase 1.816s. These are controlled native command projections
  with actual identity role changes, not KyIdentity worker delivery to a directory endpoint.
- Recovered session ran final `make ci build` serially, terminal exit 0: tidy/formatting,
  ordinary/e2e-tag vet, all package race tests and both binaries PASS.
  `/home/yoshi/.cache/kyquickstart-work/bookstack-owned-roles-final-ci.log`.
  `git diff --check` PASS. No owned BookStack fixture containers remain.
- Builds use GOMAXPROCS=2, GOMEMLIMIT=1GiB, GOFLAGS=-p=1 and disk-backed TMPDIR.
  Established per-container memory/CPU limits remain; unrelated workloads were untouched.

DOX pass: nearest BookStack adapter/module contracts, module README and fixture gateway
instructions document role projection and native guards. Offboarding runbook and catalog
spec distinguish it from a resident directory receiver. Parent contracts and Child DOX
indexes remain unchanged because ownership and directory structure did not change.

Next: protected native atomic profile/role directory command and HTTPS transport, then bridge
runtime wiring and actual KyIdentity worker qualification. A combined update must commit its
shared native-user effects together. Keep directory opt-in separate from revocation authority.
Global role/alternate writers, administrator ordinary-workflow separation, creation/group
lifecycle, audit delivery, restore, guided deployment and source/release provenance remain gates.
All-product objective remains unfinished; suite recovery/Yard configuration, full-suite
lifecycle and sealed-secret/bulk restore acceptance remain in scope. Vaultwarden only;
existing KyVault/data and live services unchanged. No deployment, commit, PR or release.


## 2026-10-10 checkpoint: BookStack 0.10 atomic directory command

Previous goal turn was progress: recovered completed native role evidence and completed
final repository checks. This turn builds the next native interface; it does not narrow
or complete the all-product objective.

BookStack community module 0.10.0 adds `kyquickstart:directory` / `ky.bookstack-directory/1`.
A bounded protected-file request contains complete profile and role requests. Validation
requires identical action, native user/setup and issuer/client/subject. The native user-row
transaction encloses both existing commands, membership/profile changes, current receipts
and their separate immutable revision audits. A role refusal or failed audit rolls back a
preceding profile write or initialization. No extra shared directory receipt is introduced.
The Go consumer exposes InitializeDirectory, InspectDirectory and ApplyDirectory, reusing
strict native profile/role report parsers. It checks coherent lifecycle, policy and exact
revision changes, and rereads both receipts. Lost replies resume by inspection before retry;
exact desired repeats preserve both revisions. The existing HTTP revocation token gains
no directory authority, and directory HTTP/runtime remain disabled.

Native fixtures now attempt administrator initialization while ordinary: the second
command refuses and neither receipt/audit remains, demonstrated by refused checks followed
by successful exact initialization. Mismatched action/user/setup/subject pairs refuse.
The real Go combined demotion changes profile and authority together. A stale role revision
and an occupied role audit key both roll back the preceding profile update and preserve
both receipts. Lost committed reply resumes without another revision; repeats remain stable.
Existing sessions lose administration, stale signed roles refuse fresh login, ordinary
signed access remains valid, and retained revoked directory updates refuse.

Evidence, serial with established disk-backed scratch and Go/container caps:
- Actual KyIdentity native fixture terminal exit 0, final source PASS:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-atomic-directory-identity-verified.log`.
  Grant package 2.393s; combined demotion package 3.259s; retained phase 1.881s.
  Native identity accounts/client/roles and real step-up role mutations are used. Combined
  projection is still a controlled native command, not identity-worker directory delivery.
- Signed-provider final fixture terminal exit 0, PASS:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-atomic-directory-signed-verified.log`.
  Includes missing/empty ordinary roles, unknown/duplicate/mixed/scalar/stale claim refusal,
  invalid signatures, exact PKCE exchange counts, native old-session/API-token/new-login
  revocation and retained identity/content.
- Initial signed run also passed before expanded initialization tests. Its originally
  mistaken identity log label was corrected to `bookstack-atomic-directory-signed-first.log`;
  it is not evidence of real KyIdentity. The first expanded real run failed the Python
  rejection helper, which only recognized the offboarding error string. Native directory
  refusal was correct; helper now requires each supported command's own fixed error and
  exit 1. Failed log: `bookstack-atomic-directory-identity-final.log`.
- Final `make ci build` terminal exit 0, PASS: tidy/formatting, ordinary/e2e-tag vet,
  all package race tests (including combined wire/lifecycle/foreign-proof rejection),
  workstation and resident binaries. Log:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-atomic-directory-final-ci.log`.
  `git diff --check` PASS; no owned BookStack fixture containers remain.

DOX pass: BookStack adapter/module owning docs index directory.go/directory.php, combined
transaction and verification. Module README documents request composition, separate audits,
no adoption and remaining HTTP scope. Offboarding runbook and catalog spec distinguish
atomic native commands from active directory delivery. Fixed the runbook's misplaced Numeric
BookStack/Forgejo sentence. Closest Child DOX Index updated; root/adapter parent and offboard
contracts/indexes remain unchanged because deployment/runtime authority did not change.
Offboard fixture reports only update the expected controlled module version.

Next: protected BookStack directory HTTPS transport with separately provisioned authority,
then resident directory adapter/runtime wiring and actual KyIdentity worker qualification.
Do not extend the revocation credential implicitly or acknowledge active SCIM updates through
its revocation-only receiver. Initialization remains explicit over verified owned accounts.
Global role/alternate writers, dedicated administrator ordinary-workflow separation,
creation/group lifecycle, audit delivery, restore, guided deployment and source/release
provenance remain gates. Other Ky suite initial service/recovery/Yard configuration,
full-suite lifecycle, sealed-secret/bulk restore and installer acceptance remain in scope.
Vaultwarden only; KyVault and existing/live services and credentials untouched. No live
deployment, commit, PR or production release changed. All-product goal remains active.


## 2026-10-10 checkpoint: BookStack 0.11 protected directory HTTPS

Previous goal turn was progress: native atomic directory command and consumer plus actual
provider/signed fixtures and final CI were completed. This turn builds and verifies the
network interface needed by the resident adapter; the all-product objective remains active.

BookStack module 0.11.0 exposes `POST /api/kyquickstart/directory` over verified HTTPS with
its separately provisioned `KYQ_BOOKSTACK_DIRECTORY_TOKEN_FILE`. The canonical protected
application-owned private file contains exactly a 64-character lowercase hexadecimal token,
using the existing token JSON format. It must differ from the valid offboarding credential;
shared tokens disable directory access. The route permits only check/apply over already
initialized owned profile/role receipts. Native initialization stays container-local. There
is no creation/adoption, revocation/reactivation or content authority. Cookies and native API
credentials cannot authenticate it; the offboarding token gains no directory authority.
Common credential/authentication/raw-body boundary code is reused without changing the
revocation endpoint's scope. Errors and audit events omit tokens/request bodies/profile data.

`DirectoryHTTPClient` uses the existing verified TLS, bounded body/response, timeout,
no-proxy/no-redirect transport. Native and network clients share directory validation,
strict source/policy/revision reports and the exact inspect-before-write/reinspect algorithm.
Projection verifies coherent lifecycle and both native proofs. Unknown committed responses
resume by inspection and exact repeats preserve revisions. Its exported interface has no
initialization, creation, revocation or content operation.

Final native qualifications grant AND remove actual administrator authority over HTTPS.
The old session gains/loses native administration; fresh signed roles must agree with the
projection. Stale native role revisions and an occupied audit key roll back the preceding
profile update through the actual route. A deliberately lost committed HTTP response resumes
without another write; cross-transport repeats preserve state. Distinct-token tests refuse
cookie/native/offboarding credentials, shared credentials, insecure file permissions,
uninitialized/unowned identities, oversized/mismatched requests and HTTP initialization.
Directory credentials cannot revoke and neither scoped token can read native content.
Revoked retained receipts remain readable; directory writes cannot reactivate them.

Authoritative evidence, serial with established disk-backed scratch and Go/container caps:
- Final actual KyIdentity fixture terminal exit 0, PASS:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-directory-http-grant-identity-verified.log`.
  Grant package 2.181s, combined demotion package 1.700s, retained phase 1.843s.
  Native KyIdentity setup/PKCE/step-up role mutations and actual BookStack HTTP/native
  authorization are used. These are controlled adapter invocations, not identity-worker
  directory delivery or deployment admission.
- Final signed-provider fixture terminal exit 0, PASS:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-directory-http-grant-signed-verified.log`.
  Malformed/missing/empty/stale roles, invalid signatures, exact PKCE counts, native
  session/API-token/new-login revocation and retained identity/content continue to pass.
- Earlier identity/signed HTTP runs also passed. Final grant variants additionally prove
  an actual HTTP administrator grant rather than using the CLI for that half of the cycle.
- Final `make ci build` terminal exit 0, PASS: tidy/formatting, ordinary/e2e-tag vet,
  all package race tests, workstation and resident binaries. Unit transport refusal tests
  cover redirect/error/oversized/foreign reports before projection and error-body redaction.
  `/home/yoshi/.cache/kyquickstart-work/bookstack-directory-http-final-ci.log`.
  `git diff --check` PASS. No owned BookStack fixture containers remain.

DOX pass: closest BookStack adapter/module docs own the new directory client/endpoint and
credential boundary and describe actual native HTTPS acceptance. Module README, offboarding
runbook and catalog spec distinguish this completed interface from unsupported resident
BookStack directory delivery. Root/adapter parent and offboard contracts/indexes remain
unchanged: no new directory boundary, runtime authority or deployment workflow was enabled.
Controlled offboard fixture reports only update their expected module version.

Next: resident BookStack directory adapter plus explicit protected runtime opt-in, receipt
binding/init wiring, and actual KyIdentity worker delivery. Never acknowledge active SCIM
updates through the revocation-only receiver or reuse its credential implicitly. Keep native
initialization explicit; provisioning/creation requires its own verified ownership lifecycle.
Global native role/alternate writers, dedicated administrator ordinary-workflow separation,
creation/group lifecycle, audit delivery, restore, guided deployment and source/release
provenance remain gates. Other suite initial recovery/Yard/service configuration, full-suite
lifecycle, sealed-secret/bulk restore and installer acceptance remain required. Vaultwarden
only; existing KyVault/data and live services/credentials untouched. No live deployment,
commit, PR or production release changed. All-product objective remains unfinished.


## 2026-10-10 checkpoint: BookStack registered-account resident wiring

Previous goal turn was progress: protected native HTTPS directory transport and qualification
completed. This turn connects that interface to the resident target/configuration without
claiming native worker delivery or completing the all-product objective.

BookStack directory mode now requires explicit protected `directory.token_file` and
`directory.bookstack` configuration. The latter pins the client, canonical ordinary role IDs,
and per-subject native ID/account setup UUID map (bounded, no duplicate native IDs/setup UUIDs).
Native account setup UUIDs differ from the bridge installation UUID. Existing numeric bridge
bindings stay byte-shape compatible and unchanged; event bodies never supply native IDs or
account receipt scopes. The target clones the protected map/policy and registers only after
native offboarding identity and initialized profile/role receipt inspection. Unknown mapped
subjects refuse before native I/O. Directory read/projection uses the separate credential;
revocation retains its existing binding/credential. Creation/groups remain unsupported.

Runtime validates the complete public policy before credentials, then reads a distinct private
raw directory token basename. It refuses shared credential bytes/files, unsafe files, missing
maps, noncanonical IDs, unsupported admin aliases and creation. Configuration loading stays
read-only and performs no product requests. BookStack SCIM userName becomes bounded informational
metadata, like Immich, while Forgejo keeps exact native username matching. BookStack reads
retain the subject; selection always uses the retained binding and protected receipt map.

SDK directory writes now expose `ErrDirectoryUncertain` for ambiguous transport/server replies,
invalid post-write reports and failed post-write readback. Known native pre-mutation/transaction
refusal stays retryable. The resident target maps uncertainty to a durable held directory job;
restart does not replay or supersede it. Direct SDK retry still inspects native state first.
This preserves the existing bridge reconciliation contract rather than treating a lost write
as a known rejection.

Evidence:
- Focused race tests PASS (1.094s): protected runtime mapping/credential rejection, read-only
  loading, exact native account UUID/client scope, separate HTTPS credentials, registered
  projection, unchanged numeric binding and a committed-then-503 write held across restart
  without another native request. Wire fixture only, not a running native product.
  `/home/yoshi/.cache/kyquickstart-work/bookstack-runtime-focused-verified.log`.
  Initial focused run correctly refused its test state's unprotected mode; fixture now uses
  the existing private-store helper. Failed log: `bookstack-runtime-focused.log`.
- Actual KyIdentity/BookStack fixture terminal exit 0, PASS with the SDK uncertainty changes:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-runtime-sdk-identity.log`.
  Native HTTPS grant/demotion, rollback, lost reply, signed authority consistency, scoped
  credential rejection and retained revoked-account access denial still pass. This is controlled
  SDK invocation, not the newly wired resident target or actual directory-event production.
- Final `make ci build` terminal exit 0, PASS: tidy/formatting, ordinary/e2e-tag vet, all
  package race tests and both binaries. Offboard package 31.616s.
  `/home/yoshi/.cache/kyquickstart-work/bookstack-runtime-final-ci.log`.
  `git diff --check` PASS; no owned BookStack fixture containers remain. Established serial
  Go/container resource caps and disk-backed scratch retained; unrelated workloads untouched.

DOX pass: nearest offboard owning doc records protected BookStack account scopes, separate
credential, informational userName and held outcomes; BookStack adapter doc records typed SDK
uncertainty. Offboarding runbook supplies an explicit opt-in example, native-vs-bridge UUID
and raw-token-vs-product-JSON distinctions, no binding rewrite, initialization and remaining
qualification. Catalog spec records local wiring without admission. Root/adapter-parent/module
and Child DOX indexes stay unchanged because domain ownership, directory layout and native
module HTTP contract did not change; module version remains 0.11.0.

Next: run the resident TLS service against disposable native BookStack with controlled SCIM
updates/reopened audit proofs, then integrate the actual KyIdentity worker and qualify native
profile/roles, assignment/global disablement and receipt retention. Explicitly initialize and
verify each account's native profile/ordinary-role policy before registration; do not assume
all existing accounts share a policy. Creation/group lifecycle, uncertainty reconciliation,
global role/alternate writers, dedicated administrator ordinary-workflow separation, audit
relay, restore, guided deployment and provenance remain gates. Other suite initial service,
recovery/Yard configuration, full-suite lifecycle and sealed-secret/bulk restore acceptance
remain required. Vaultwarden only; KyVault/data and existing/live services and credentials
untouched. No live deployment, commit, PR or production release changed. Goal remains active.


## 2026-10-10 checkpoint: BookStack real resident directory qualification

Previous goal turn was progress: protected registered-account resident wiring and SDK
uncertainty classification completed. This turn qualifies the real resident TLS service
against pinned native BookStack with a controlled authenticated SCIM sender; the actual
KyIdentity worker is still the next producer gate. All-product objective remains active.

`TestNativeBookStackDirectory` accepts only its nonce-labeled/digest-pinned disposable
application and loopback gateway. It reads fixture credentials only from protected native
files and configures separate private resident product/directory/SCIM tokens, native account
setup UUID/client/role scopes and TLS trust. Registration verifies native initialized receipts.
The shared native directory harness loads the actual runtime, starts Serve, sends authenticated
SCIM grant/profile/demotion/repeat and reads native role/profile through GET. It joins shutdown
before reopening the unchanged numeric binding and every started/success directory audit.
BookStack records three completed controlled attempts: grant, ordinary replacement and repeat.
The parent independently checks native name/email, old-session administrator denial, fresh
actual KyIdentity OIDC ordinary access and subsequent retained-account revocation/content.
The retained native role proof now expects revision four when this explicit fixture flag runs;
ordinary fixture mode keeps revision two. Missing final audit still refuses inspection.

The same harness remains used by Forgejo rather than duplicating runtime/TLS orchestration.
Forgejo keeps its native username/source scope, two ordinary replacements, old-session
administrator denial and actual-provider assertions. Its native regression also passes with
joined shutdown and the stronger all-audit/binding checks.

Evidence, serial with existing disk-backed scratch and Go/container resource caps:
- Final BookStack/actual KyIdentity fixture terminal exit 0, PASS:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-resident-directory-identity-verified.log`.
  `TestNativeBookStackDirectory` 0.35s / offboard package 1.355s. Final retained native role
  phase package 1.932s. Protected actual resident TLS SCIM updates, native profile/readback,
  reopened proofs, real PKCE ordinary access and native revocation/content checks pass.
- First fixture run failed because a Python edit assertion prevented the new runtime hook
  while the retained-phase expectation had already switched to revision four. It did not
  exercise the new service. Hook and dynamic final audit key were corrected; failed log
  `bookstack-resident-directory-identity.log` is not service evidence.
- Native Forgejo/actual KyIdentity regression terminal exit 0, PASS:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-shared-resident-forgejo-regression.log`.
  Shared resident helper 0.10s / offboard package 1.108s; full native account/offboarding
  fixture 6.86s / Forgejo package 7.866s. Pinned image remains
  `codeberg.org/forgejo/forgejo@sha256:5effb7305584aca479b29fde6f9631a6dbe86ae798ae02eeea33a3666f0c0bf8`.
- Final `make ci build` terminal exit 0, PASS: tidy/formatting, ordinary/e2e-tag vet, all
  package race tests and both binaries. Offboard package 31.281s.
  `/home/yoshi/.cache/kyquickstart-work/bookstack-resident-directory-final-ci.log`.
  `git diff --check` PASS; no owned BookStack/Forgejo fixture containers remain.

DOX pass: closest offboard and BookStack adapter docs describe the explicit directory fixture
and its controlled-sender scope, native access and reopened proofs. Offboarding runbook and
catalog spec replace the unqualified resident-service gate with this evidence while keeping
actual worker/full-lifecycle/deployment gates. Root/adapter parent/native module and Child DOX
indexes remain unchanged: no production behavior, native module contract, ownership boundary
or deployed workflow changed. Native module stays 0.11.0.

Next: actual KyIdentity worker delivery through the protected BookStack runtime, with native
profile/role changes, assignment/global-disablement denial and reopened receipt/audit proofs.
The current controlled sender does not prove event production/acknowledgment or complete
connector lifecycle. Explicitly initialize each owned account and verify its ordinary policy;
never infer another account's setup UUID or adopt by name/email. Creation/groups, uncertainty
reconciliation, global role/alternate writers, dedicated administrator ordinary-workflow
separation, audit relay, restore, guided deployment and source provenance remain gates. Other
suite initial recovery/Yard/service configuration, full-suite lifecycle and sealed-secret/bulk
restore acceptance remain required. Vaultwarden only; KyVault/data and existing/live services
and credentials untouched. No live deployment, commit, PR or production release changed.
All-product goal remains unfinished.


## 2026-10-10 recovery after reported memory kill

User reported that the previous agent was killed for memory usage. Source changes and
prior qualification logs remain on disk. Recovery inspection found no running installer
fixture/build process and no BookStack fixture container; unrelated containers were left
untouched. The most recent run, `bookstack-producer-account-scope.log`, ends in
`Native OIDC qualification failed: AssertionError` / `native offboarding command failed`
after passing earlier signed OIDC and role projection phases. This is a failed native
initialization attempt, not proof of actual KyIdentity worker delivery. Its cause is not
yet diagnosed; the log does not establish that memory caused this test failure.

Existing memory policy remains: serial jobs, disk-backed scratch, Go heap/parallelism
limits and explicit container limits. No fresh heavy test was launched during recovery.
Next action: diagnose the owner-account directory initialization failure before registering
both identities or wiring the actual worker. Preserve the preceding passing controlled
SCIM fixture evidence separately. All-product goal remains active and unfinished.

DOX review: root memory rules already cover this recovery; no ownership, interface or
workflow contract changed, so AGENTS.md files and child indexes stay unchanged. Only
this recovery checkpoint was added; no deployment, release, commit or PR changed.


## 2026-10-10 checkpoint: BookStack independent owner role policy

Previous recovery turn yielded authoritative failure/terminal evidence, rather than merely
restating status. This turn reproduced and diagnosed that failure: pinned native BookStack
has no ordinary roles on the verified owner, while the everyday account has IDs [2, 5].
The non-empty role-array contract prevented owner directory initialization before any actual
identity worker ran. Observation log `bookstack-owner-policy-observation.log` terminated
with exit 1 and printed those nonsecret IDs; no memory cause is claimed.

Community native module/SDK now require 0.12.0. Explicit empty ordinary arrays are accepted
without adding membership; missing/null native request or report arrays refuse. Existing
role/source ownership, signed initial administrator proof, policy fingerprints, audit and
transaction checks remain required. Native account and directory wire contracts remain
unchanged; old module versions are refused by the SDK. No deployed module was upgraded.

Protected resident `BookStackAccount.ordinary_role_ids` optionally pins each account's
verified native policy independently. Absent/null uses the existing app policy; explicit []
preserves no ordinary roles. Validate bounded positive sorted unique IDs and clone each
account array before serving. SCIM cannot choose this policy. Account setup UUIDs and old
numeric binding shapes stay independent/unchanged. The fixture explicitly initializes both
owned accounts, registers both before serving, and reopens both exact bindings on shutdown.
It continues controlled SCIM grant/profile/demotion/repeat on the everyday account. This
is preparation for the actual identity producer, not proof of its delivery.

Evidence, serial with disk scratch and resource caps:
- Focused race tests PASS, terminal exit 0:
  `bookstack-account-policy-unit.log` in `/home/yoshi/.cache/kyquickstart-work`.
  Checks include administrator-only empty native proof, missing/null array refusal,
  per-account canonical validation and actual HTTPS dispatch with a different app policy
  and caller-mutated policy arrays (protected copy is preserved).
- Full disposable pinned native BookStack + actual KyIdentity OIDC fixture PASS, exit 0:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-account-policy-native.log`.
  Native owner/everyday policies [] / [2, 5]; `TestNativeBookStackDirectory` 0.44s PASS.
  Both native receipt scopes register; resident TLS projection, joined shutdown/reopened
  bindings/audits, old/fresh ordinary access and retained account/session/token/content
  revocation checks pass. The fixture containers cleaned up.

DOX: updated closest BookStack/module/offboard contracts, native module README, offboarding
runbook and catalog evidence for module version and per-account policy. Root and adapter
parent ownership/workflows and all Child DOX indexes are unchanged; no boundary was added.
Next is trusted private-network TLS transport and actual KyIdentity SCIM connector worker
delivery with acknowledgment/revision, native profile/role effects, assignment-loss and
global-disablement access denial and reopened durable proofs. Creation/groups, dedicated
administrator workflow separation, reconciliation, audit relay, restore and provenance
remain gates. All other suite/catalog gates remain required; Vaultwarden only. Existing
services/data/credentials untouched; no live deployment, commit, PR or release changed.
All-product objective remains active and unfinished.

Final verification: `make ci build` terminal exit 0, PASS (tidy, formatting, ordinary/e2e
tag vet, every package race suite and both binaries), log
`/home/yoshi/.cache/kyquickstart-work/bookstack-account-policy-final-ci.log`; offboard
race suite 31.573s. `git diff --check` passes. No owned BookStack fixture remains.


## 2026-10-10 checkpoint: actual BookStack identity producer

Previous turn was progress: independent native owner/everyday policies and empty owner
roles were fixed and qualified. This turn wires actual KyIdentity SCIM delivery into the
protected resident service using both initialized native accounts. No production interface
or module version changed (BookStack remains 0.12.0); all changes are qualification and DOX.

The parent compiles the resident helper; `native_producer.py` runs it and owns
an immutable-image SSH fixture (64 MiB/one CPU). Fresh private host/client keys and pinned
known_hosts protect raw TCP forwarding from the private Docker network to the loopback
TLS service. Generate a server certificate (CA:false, serverAuth, exact relay DNS/loopback
SANs); only public trust is installed in the disposable identity target. Private callback
opt-in is explicit there. The native setup bundle creates/checks/resumes the app-scoped
SCIM connector and protected token; no existing service is touched. Native account ID/setup
UUID/policy scopes, private runtime material and later stop control travel over bounded
stdin, rather than secrets in environment JSON/output. The parent compiles helpers serially
and retains root disk-backed scratch/resource caps.

The shared `TestNativeIdentityProducerBridge` now accepts explicitly flagged BookStack
stdin input, independently verifies nonce/digest native ownership, validates the two
registered bindings and separate product/directory/SCIM authority, and starts actual Serve.
Only the identity worker mutates SCIM. The parent requires newer acknowledged delivered
revisions, checks real native profile changes and old-session administrator grant/demotion,
then removes the actual daily app assignment with step-up. It checks worker acknowledgment,
old-session/API-token denial and fresh issuer refusal, retains content/identity, and joins
shutdown before reopening both unchanged numeric bindings, every started/success directory
audit and completed revocation proof. Existing scoped SDK partial-fence repair/resume tests
also run after the actual worker's completed revocation; they cannot substitute for the
producer acknowledgment/access checks that precede them. Forgejo keeps its separate input
and native source behavior through the shared harness.

Evidence:
- Full BookStack + actual identity fixture terminal exit 0, PASS:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-actual-producer-verified.log`.
  Owner active revisions 1 then 2; daily active 3, 4, 5, 6 then inactive revision 7,
  each with acknowledged delivered event. Native profile/role effects, assignment-loss
  session/token/fresh-login denial, retained content and reopened resident proofs pass.
  Shared producer helper 22.47s; final retained role phase package 1.827s.
- Actual Forgejo producer regression terminal exit 0, PASS:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-producer-forgejo-regression.log`.
  Shared helper 9.94s; full fixture 15.52s / package 16.531s.
- Earlier attempts are failed qualification, not end-to-end evidence: first
  `bookstack-actual-producer.log` failed compiling a mismatched int/int64 test assertion;
  `bookstack-actual-producer-second.log` rejected the fixture CA-marked server certificate;
  `bookstack-actual-producer-third.log` passed native delivery and helper proofs but the
  parent failed a positional argument to the keyword-only denial helper. Those fixture
  errors were corrected, including the exact filtered readiness query. Failed preliminary
  unit log also reflects a shared-helper owner-variable conflict, corrected before native
  qualification. Runtime TLS checks were preserved, not weakened.

DOX pass updates closest BookStack/module/offboard docs, module README, offboarding runbook
and catalog evidence. They distinguish the controlled sender from the newly verified actual
producer scope. Root/adapter parent and all Child DOX indexes remain unchanged: no new
ownership boundary, deployed workflow or native module contract was introduced.

Next: BookStack actual-worker global disablement, including separate surviving identity
administrator to observe it and retained native access/content/audit proofs. Then creation/
groups, reconciliation, audit relay, restore, dedicated administrator workflow separation
and release/source provenance still require qualification. Other Ky-suite initial recovery/
Yard/service configuration and full-suite lifecycle/backup/restore acceptance remain required.
Vaultwarden only; older KyVault data untouched. No live deployment, commit, PR or release
changed. All-product goal remains active and unfinished.

Final `make ci build` terminal exit 0, PASS: tidy, formatting, ordinary/e2e-tag vet, all
package race suites and both binaries. Offboard race suite 31.226s; log
`/home/yoshi/.cache/kyquickstart-work/bookstack-actual-producer-final-ci.log`.
`git diff --check` PASS; no owned BookStack/Forgejo fixture container remains.


## 2026-10-10 checkpoint: BookStack actual global disablement

Previous turn was verified progress: actual registered-account identity-worker profiles,
roles and assignment loss passed. This turn extends that fixture to globally disable the
owner, using a separately provisioned surviving identity administrator to perform native
operation-bound step-up and observe delivery after the owner session is revoked.

The parent retains owner directory state, disables its actual identity with
`PUT /api/admin/users/<owned subject>` / `status:disabled`, and requires a newer acknowledged
inactive connector revision. It proves old identity administration, old native product
administration, the native API token and fresh owner OIDC are denied. Separate scoped
directory/offboarding reads verify the exact retained owner profile/roles with only disabled
flags changed, completed native fences and zero tokens. A read-only native BookStack model
lookup verifies retained content after all assigned product administrators are disabled;
it does not restore a grant, provision another product account or edit the database.
The surviving identity administrator remains able to observe the native provisioning report.

The shared resident helper now reopens both completed BookStack revocation jobs and all
started/success revocation audits, in addition to existing immutable bindings and every
directory audit. Forgejo's helper branch remains unchanged. These are qualification-only
changes: native module stays 0.12.0, no production product/runtime interface changed.

Evidence: full pinned disposable native BookStack + actual identity producer fixture
terminal exit 0, PASS, log
`/home/yoshi/.cache/kyquickstart-work/bookstack-global-disable-native-verified.log`.
Owner inactive revision 3 acknowledged after active revisions 1/2; daily assignment-loss
revision 7 and prior profile/role changes remain passing. Native access/content/receipt
checks pass. `TestNativeIdentityProducerBridge` 87.21s PASS after joined shutdown; retained
role phase package 1.842s. The first command's malformed digest was refused at preflight
without starting a fixture (`bookstack-global-disable-native.log`); the verified run uses
the unchanged pinned MariaDB image. No memory/process restart or weakened validation.

DOX pass updates closest BookStack/module/offboard contracts and fixture guidance, module
README, offboarding runbook and catalog scope. They qualify global disablement for this
single BookStack connector; this is not evidence of cross-product global delivery through
BookStack. Parent/root ownership and all Child DOX indexes remain unchanged: no new
boundary, deployed operating rule or native contract was introduced.

Next: ordinary BookStack account creation and receipt-backed resident delivery, plus its
cross-product global lifecycle. Group provisioning, uncertainty reconciliation, audit relay,
restore, administrator workflow separation, production/source provenance and the other
suite/catalog acceptance gates remain required. Full-suite initial recovery/Yard/service
configuration and backup/restore acceptance remain unfinished. Vaultwarden only; older
KyVault and existing/live services, credentials and data untouched. No deployment, commit,
PR or production release changed. All-product goal remains active and unfinished.

Final `make ci build` terminal exit 0, PASS: tidy, formatting, ordinary/e2e-tag vet, all
package race suites and both binaries; offboard package 31.384s. Log:
`/home/yoshi/.cache/kyquickstart-work/bookstack-global-disable-final-ci.log`.
`git diff --check` PASS; owned BookStack fixture containers cleaned up.


## 2026-10-10 checkpoint: native atomic BookStack ordinary creation

Previous turn was progress: actual BookStack global disablement qualified. This turn builds
the missing native birth transaction needed before resident new-account delivery. Community
module and strict SDK now require 0.13.0; no deployed module was upgraded. Existing account,
profile, roles, directory and offboarding wire contracts retain their shapes.

`creation.php` exposes only private container-local `kyquickstart:create` with
`ky.bookstack-creation/1`: installation UUID, per-account setup UUID/immutable ordinary
account spec, everyday alias and non-empty canonical native ordinary IDs. Both actions must
agree. Native role policy validates definitions/permissions before initial assignment. One
transaction commits new account/assignment, ordinary membership, initialized profile/role
receipts/audits and immutable creation receipt/audit. Management roles and adopting a prior
setup account without creation ownership refuse. Failed policy or audit publication rolls
back the birth. No HTTPS creation route or extra directory-token authority was added.

`creation.go` provides source/module-probed `CreateManagedAccount`, read-only
`CheckCreatedAccount` and strict joined account/directory readback. Shared account/directory
report parsers are reused. Original creation intent remains pinned across repeat, projected
profile changes and revocation. Creation-owned checks may observe a separately projected
administrator only behind matching creation/audit and directory proof; they never grant it.
The existing standalone account command keeps its ordinary management-authority refusal.
Unknown CLI writes inspect the identical immutable spec before retry; resident uncertainty
classification/composition remains unfinished.

Native qualification uses only owned digest-pinned disposable containers and actual identity.
`KYQ_TEST_BOOKSTACK_CREATE=1` is separate from producer mode until resident creation is built.
It tests policy/audit rollback and then succeeds with the same source/profile, simulates a
lost committed response and recovers through check, refuses foreign installation/subject
collisions and prior setup-account adoption without changing that account, preserves a
projected profile on repeat, assigns the new subject in actual KyIdentity, proves native
ordinary OIDC/session/API use and management denial, then proves retained revocation and
old/fresh access denial. Target-generated passwords are not supplied or returned.

Evidence, serial with existing Go/container caps and disk-backed scratch:
- Final focused race suite PASS, exit 0: `bookstack-creation-unit-verified.log` in
  `/home/yoshi/.cache/kyquickstart-work`. Joined-proof omissions, contradictory lifecycle,
  management at birth and invalid roles before native I/O are covered. Initial compile
  log `bookstack-creation-unit.log` lacked a test import; corrected before qualification.
- Full native creation/actual identity fixture PASS, terminal exit 0:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-native-creation-verified.log`.
  Creation helper 5.43s and signed/retained helper 1.12s; final retained existing role phase
  package 1.880s. Earlier `bookstack-native-creation.log` also passed before the explicit
  prior-setup adoption counterexample was added; final log includes it.
- Actual registered-account worker/module regression PASS, exit 0:
  `/home/yoshi/.cache/kyquickstart-work/bookstack-creation-module-producer-regression.log`.
  Profiles/roles, assignment loss, global disablement, retained content and reopened proofs
  pass against module 0.13.0; shared resident helper 86.30s.
- `make ci build` PASS, terminal exit 0: tidy, formatting, ordinary/e2e-tag vet, every
  package race suite and both binaries. Offboard race suite 27.555s; log
  `/home/yoshi/.cache/kyquickstart-work/bookstack-native-creation-final-ci.log`.
  `git diff --check` PASS; owned BookStack fixture containers cleaned up.

DOX pass updates closest BookStack and native-module contracts, module README, offboarding
runbook and catalog evidence/version. Root/adapter parent responsibilities and Child DOX
indexes are unchanged: the new command/SDK stay inside existing owned boundaries. Restore
docs require creation receipt/audit settings in the same consistent database backup.

Next: protected native HTTPS creation/absence/receipt authority and SDK, then explicit
resident creation opt-in with durable native scope resolution and actual worker delivery.
The resident still refuses BookStack creation; current registered-account authority must
not be widened silently. Cross-product global lifecycle, groups, reconciliation, audit relay,
restore, administrator workflow separation, source/release provenance and all other suite/
catalog gates remain required. Full-suite recovery/Yard/service configuration and backup/
restore remain unfinished. Vaultwarden only; old KyVault and existing/live services, data and
credentials untouched. No deployment, commit, PR or production release changed.
All-product goal remains active and unfinished.


## Memory-kill recovery checkpoint — 2026-10-10

Recovered the surviving worktree and terminal evidence after the user reported the agent
was killed for memory usage. No source reset, live deployment, commit or PR performed.
The all-product objective remains active and unfinished.

Current BookStack module 0.14.0 is work in progress: `creation_http.go`, the separately
credentialed `/api/kyquickstart/creation` endpoint, private fixed creation policy and
read-only native absence proof survived on disk. The Go SDK distinguishes unknown
creation outcomes and inspects retained ownership before retry; these new HTTP semantics
have NOT yet received focused transport tests or disposable native fixture qualification.
The last fully native-qualified creation milestone remains module 0.13.0 described above.
Do not treat the version bump or generic passing package suite as qualification of the
new endpoint. Some BookStack DOX text still describes creation as native-only; reconcile
those contracts as part of completing the in-progress HTTPS feature. Resident BookStack
new-account creation remains refused, and existing KyVault/live services stay untouched.

Recovered focused adapter race log `bookstack-creation-http-unit.log`: PASS, 1.084s.
Re-ran `make ci build` serially against the surviving tree with `GOMAXPROCS=2`,
`GOMEMLIMIT=1GiB`, `GOFLAGS=-p=1`, disk-backed
`TMPDIR=/home/yoshi/.cache/kyquickstart-work`: terminal exit 0. Tidy, formatting,
ordinary/e2e-tag vet, all package race suites and both binaries pass; offboard 27.574s.
Full output: `/home/yoshi/.cache/kyquickstart-work/memory-recovery-ci.log`.
`git diff --check` passes. Root DOX and indexes intentionally unchanged: memory limits
and serial testing are already documented, and this checkpoint changes no contract.

Next resume: add focused HTTPS creation/absence/uncertainty tests, qualify separate
credential and policy boundaries in the owned native BookStack fixture, run actual
registered-worker regression for module 0.14.0, reconcile closest DOX, then implement
protected resident creation opt-in and durable native scope resolution. Preserve all
remaining suite lifecycle/backup/restore/admin-purpose/provenance gates above.


## BookStack scoped HTTPS creation qualified — 2026-10-10

The preceding recovery turn made progress by recovering surviving changes and proving
serial CI/build; it did not qualify the unfinished endpoint. This checkpoint supersedes its
module 0.14.0 work-in-progress status for the HTTPS adapter milestone only. The full
all-product objective remains active and unfinished.

Module 0.14.0 now has a separately credentialed `POST /api/kyquickstart/creation` over the
native ordinary-account transaction. Protected canonical application-owned 0600 files
`KYQ_BOOKSTACK_CREATION_TOKEN_FILE` and `KYQ_BOOKSTACK_CREATION_POLICY_FILE` remain outside
public/upload roots. Creation credential must differ from both directory and offboarding;
the exact policy pins installation UUID, configured issuer/client, everyday alias and sorted
ordinary-role IDs. Probe/create/check preserve byte-exact bounded input and generic failures.
Cookies, native API, directory and offboarding credentials have no creation authority;
creation credentials cannot project directory state, revoke or read content.

Read-only `ky.bookstack-creation-absence/1` checks source/account/install/ordinary policy
pins and refuses absence on ownership, native subject/email collision or subject revocation.
Actual creation rechecks native ownership inside its atomic transaction. Go
`CreationHTTPClient` refuses incomplete absence evidence before writes, distinguishes known
ownership refusal with `ErrAccountConflict`, and exposes `ErrCreationUncertain` for
transport/5xx/invalid proof after a create. Direct retry probes again and checks the identical
immutable creation receipt rather than adopting an account or resetting its profile.

Focused tests share existing joined native report fixtures: unknown commit and malformed
post-write proof require a hold; retry uses `probe,create,probe,check` with exactly one write.
Known refusal and foreign ownership refuse, and missing source/install/setup/role/absence
proofs prevent writes. Administrator birth and invalid intent are refused before HTTP I/O.
No new library or runtime factory abstraction was introduced.

The disposable actual-KyIdentity creation fixture now optionally uses
`KYQ_TEST_BOOKSTACK_CREATE_HTTP=1` alongside `KYQ_TEST_BOOKSTACK_CREATE=1`. Its verified
nonce/digest-owned gateway supplies standard verified TLS with the public fixture CA. Both
creation and retained phases use the HTTPS SDK. A test-only transport drops an actually
committed create reply; exact receipt recovery observes the same account without another
write. Native immutable-audit fault injection proves birth rollback. Policy/source/install/
management/byte-exact/oversized request failures, insecure token/policy files and shared
credentials refuse. Prior setup accounts and subject collisions cannot be adopted. Explicit
native directory projection survives creation retry. After actual identity app assignment,
fresh native OIDC and ordinary session/token work with administration denied; retained
revocation preserves ownership/profile and denies old session/token and fresh sign-in.
Projection and revocation remain separate native/scoped authorities, not creation powers.

Evidence (all heavy work serial with the existing Go heap/process/container caps and
disk-backed scratch under `/home/yoshi/.cache/kyquickstart-work`):
- `bookstack-creation-http-focused.log`: focused package race PASS, exit 0, 1.427s.
- `bookstack-creation-http-helper-unit.log`: updated helper package race PASS, exit 0.
- `bookstack-native-creation-http-corrected.log`: full native HTTPS/actual identity fixture
  terminal exit 0; atomic creation helper 3.11s, signed/retained helper 0.99s. All native
  boundary, sign-in, no-adoption, projection and old/fresh denial checks pass. Initial
  `bookstack-native-creation-http.log` stopped on a fixture TypeError: a cookie-only helper
  received a token keyword. Replaced it with the existing token-aware `denied` helper and
  added explicit creation-token revocation refusal before the successful full rerun.
- `bookstack-creation-http-producer-regression.log`: actual registered-account identity
  worker regression terminal exit 0 against module 0.14.0. Profile/role grants/removal,
  assignment loss, global disablement, retained content, joined shutdown and reopened
  resident audit proofs pass; helper 76.77s. This is registered-account delivery, not birth.
- `bookstack-creation-http-final-ci.log`: `make ci build` terminal exit 0; tidy, gofmt,
  ordinary/e2e-tag vet, all package race suites and both binaries. Offboard race 33.404s.
- Python AST syntax and `git diff --check` pass. No owned BookStack fixture containers
  remain after the terminal fixture cleanup.

DOX pass updates closest BookStack/module contracts, adapter parent ownership, module README,
offboarding runbook and catalog evidence. Fixed stale native-only creation language and
clarified remaining production resident deployment qualification. Root responsibilities and
Child DOX indexes intentionally unchanged; these interfaces stay within existing boundaries.
Back up creation credential/policy and consistent native creation/account/directory receipts
and immutable audit settings with the existing retained fence state.

Next: protected resident BookStack creation opt-in and durable native scope resolution, then
actual identity-worker birth delivery. The current fixed protected account map applies to
registered accounts; new accounts require receipt-owned resolution of their separate native
account setup UUID and ID without shared mutable runtime maps. Preserve immutable initial
profile/creation intent across retry and separately audited subsequent directory projection.
Current resident `create_accounts:true` for BookStack remains refused until that work is
implemented and qualified. Cross-product global lifecycle, groups, uncertainty reconciliation,
audit relay, restore, administrator workflow separation, provenance and suite/cross-product
configuration, recovery/Yard/service connections and bulk backup/restore remain unfinished.
Vaultwarden only; old KyVault, existing live workloads, credentials and data remain untouched.
No live deployment, commit, PR or production release changed.


## BookStack resident ordinary-account creation qualified — 2026-10-10

Current module version: **0.15.0**. This checkpoint supersedes the preceding statement
that resident BookStack creation is refused. The all-product request remains unfinished.

Implemented:
- Protected BookStack `directory.creation_token_file` and explicit `create_accounts:true`.
  Credentials are separate from SCIM, offboarding and directory credentials, private and
  unique. Other adapter kinds reject this BookStack field. Loading runtime configuration
  makes no native writes.
- Stable UUIDv8 account setup identity derived from installation/app/source/subject, separate
  from installation identity. No shared mutable account map or new bridge storage schema.
- Native read-only `ky.bookstack-creation-scope/1`: subject absence checks existing native
  subjects, fences and primary/assignment/creation/audit keys. Orphan ownership never becomes
  absence. Scope resolution requires matching source/install/setup/subject/ordinary roles,
  account intent, immutable creation audit and current native directory proofs. Legacy setup
  accounts cannot be adopted through this path. Responses expose verified identity/digests,
  not profile or content.
- Resident account birth uses the existing durable creation hold, native receipt publication
  and initialization audit path. New accounts are always ordinary. Current native scope is
  resolved from receipts after profile changes, revocation and process restart.
- Turning creation off prevents birth and absence callbacks; retaining the protected creation
  token permits read-only scope lookup for previously created accounts. Restore requires the
  credential/policy, all native ownership/audit receipts and bridge receipts.
- All three scoped native HTTP endpoints treat database exceptions and failures reporting a
  completed operation as 503/uncertain. Known validation/business refusals remain refusals;
  an uncertain result requires receipt inspection before retry.

Verification evidence (disk-backed logs under `/home/yoshi/.cache/kyquickstart-work`):
- `bookstack-resident-creation-scopes-unit.log`: focused scope/pin/runtime refusal tests pass,
  including missing/wrong installation, setup, source, subject, native ID and intent proofs.
- `bookstack-native-resident-birth-reload.log`: terminal exit 0. Actual KyIdentity worker
  assigns a previously unassigned subject, creates exactly one native ordinary account,
  acknowledges birth revision 1, projects Unicode name/email at revision 2 and acknowledges
  global disablement at revision 3. Native ID, profile and authored content remain; old
  session/API token/fresh OIDC are denied. Joined shutdown and reopened creation,
  initialization, directory and revocation audits pass. Fresh runtime reconstruction with
  creation disabled resolves the retained account without adding it to the protected map.
  Native worker helper 95.84s.
- `bookstack-native-scope-refusal.log`: terminal exit 0. Actual HTTPS native fixture rejects
  orphan creation audits, wrong setup scope and legacy setup adoption, proves transactional
  rollback and preserves signed ordinary use and retained revocation.
- `bookstack-birth-forgejo-creation-regression.log`: terminal exit 0. Existing actual-worker
  Forgejo creation remains qualified after the shared runtime/probe/helper changes; package
  race test 23.176s.
- `bookstack-native-scope-and-database-outcomes.log`: recovered after interruption. All native
  phases end in PASS, including the new assertion that a real SQL audit collision is reported
  as `ErrCreationUncertain` over HTTPS while independently proving rollback. Native ordinary
  sign-in, HTTP revocation and retained content checks finish successfully. The old process
  handle no longer exists, so its exit status could not be recovered; do not invent one.

DOX pass updates BookStack/module and offboard contracts, module README, offboarding runbook
and third-party catalog evidence. Root and parent Child DOX indexes remain unchanged because
these interfaces stay inside existing ownership boundaries. No new child domain was created.

Remaining: full connector/group and cross-product lifecycle, uncertain-write reconciliation,
audit relay, restore, dedicated administrator workflow separation, provenance/release gates,
suite service/recovery/Yard configuration, app-aware bulk backup/restore and complete guided
installer acceptance. Initial product setup interfaces do not satisfy all these gates.
Vaultwarden remains the only password choice. Existing KyVault and live workloads, credentials
and data remain untouched. No deployment, commit, PR or production release changed.

Resource discipline after memory interruption: run builds and fixtures serially with
`GOMAXPROCS=2 GOMEMLIMIT=1GiB GOFLAGS=-p=1`, disk-backed TMPDIR/cache logs and capped disposable
containers. Never stack heavyweight fixtures or assume `/tmp` is disk-backed.

Final verification: `bookstack-resident-birth-final-outcomes-ci.log` terminal exit 0 after
the database/reporting outcome fix and DOX changes. `make ci build` passes tidy, formatting,
ordinary/e2e-tag vet, all package race tests and both workstation/resident binaries.
`git diff --check` passes; no owned BookStack/Forgejo fixture containers remain.


## LibreChat native lifecycle prerequisites qualified — 2026-10-10

Previous goal turn classification: **progress** (BookStack v0.15 resident creation/restart,
actual native worker evidence and fresh full CI/build). Current turn makes new authoritative
progress on the next catalog product; the all-product objective stays unchanged and active.

Pinned LibreChat v0.8.8 commit `e8f3be08623663d4ad7f7241e693c94469b63bb0` in an isolated,
read-only source checkout at `/home/yoshi/.cache/kyquickstart-work/librechat-v0.8.8`.
Added `internal/adapters/librechat/native_qualification.cjs` plus a Go test runner. Both the
commit and hashes of the four executed native modules are pinned. Node heap is 256 MiB,
VM source initialization is bounded and the Go subprocess timeout is 20 seconds.

The fixture executes unchanged native `banViolation`, `checkBan`, `jwtStrategy` and admin
user route-registration modules with substituted dependency/storage boundaries and clock:
- Native ban removes refresh sessions and returns 403 while its duration is active.
- After expiry, the native JWT strategy still resolves the same retained native user and
  ban middleware allows it. No product-side permanent account disablement was published.
- Disabling `BAN_VIOLATIONS` bypasses an otherwise live native ban.
- The native admin user router registers only list/search, no account disable or token
  revocation. Its source list projection also omits the OIDC issuer/subject needed to verify
  a foreign account binding; email/provider alone cannot authorize adoption or mutation.

This is **prerequisite source/module qualification**, not a running MongoDB/HTTP deployment,
signed-token proof or exhaustive coverage of every authorization path. It changes the next
implementation action: build product-owned exact source/subject inspection and persistent
retained revocation rather than wiring temporary bans or deleting user data. Native OIDC
claim-based administrator setup remains reusable. No LibreChat setup adapter or resident
connector is enabled and admission stays gated. User data and live deployments are untouched.

Evidence: `librechat-native-prerequisites.log` terminal exit 0; focused
`librechat-native-prerequisite-race.log` terminal exit 0 (package 1.041s), with all three native
behavior assertions and route-registration assertion printed. Upstream primary-source links
and corrected offboarding admission status are in the catalog spec.

DOX: added the owning LibreChat child AGENTS.md and parent ownership/index entry. Root index
is intentionally unchanged because the existing adapters parent owns this new child. Other
product/bridge contracts did not change. Source/model/API provenance, actual native setup,
retained identity/content and old/fresh session/token denial, administrator workflow separation,
lifecycle, backup/restore and deployment still require implementation/qualification.

Next: product-owned LibreChat native inspection and permanent revocation with authorization
hooks; then Go SDK and real disposable setup/OIDC/offboarding fixtures before resident delivery.
All preceding Ky, Forgejo, BookStack, Immich, Vaultwarden and other catalog acceptance gates
remain in scope. No live changes, PR, commit or release occurred. Full objective is unfinished
and has no blocker requiring a human decision.

Final evidence: `librechat-prerequisite-ci.log` terminal exit 0 for serial memory-limited
`make ci build`, with `KYQ_TEST_LIBRECHAT_SOURCE` set so the pinned native prerequisite
probe ran rather than skipped. Tidy, gofmt, ordinary/e2e-tag vet, all package race tests
and both binary builds pass. `git diff --check` passes.


## LibreChat native account inspection and Go consumer implemented — 2026-10-10

Previous goal turn classification: **progress** (new pinned native-module evidence changed
LibreChat's offboarding implementation action). Current turn implements the first missing
native identity interface and consumer; the complete all-product objective remains active.

Community module **0.1.0**, targeting LibreChat v0.8.8 commit
`e8f3be08623663d4ad7f7241e693c94469b63bb0`:
- `module/account.cjs` accepts only `ky.librechat-account-inspect/1` with exact native
  lowercase ObjectId, configured HTTPS issuer and stored OIDC subject/provider. It selects
  only native identity/role through product-owned `getUserById` under `runAsSystem`.
  Missing/foreign native identity, unsupported custom roles and malformed/unknown input
  refuse. Inspection does not adopt, provision, modify or reactivate any account.
- Actual filesystem input boundary: canonical nonsymlink 0600 regular file owned by the
  application user, protected root/application-owned ancestors with sticky-root scratch
  exception, forbidden public/client/upload/image paths. Validate byte bounds before JSON
  parse; reject invalid UTF-8, malformed issuer/subject and lone surrogate input. Use bounded
  reads and nonblocking/no-follow open so FIFOs cannot block inspection.
- `module/command.cjs --capabilities` advertises only account inspection without native
  dependencies, environment/database reads or writes. `--file` requires canonical application
  root and native package metadata LibreChat v0.8.8, uses target-held native environment/.env,
  exact configured issuer and disabled OpenID token reuse. It resolves native packages inside
  the product and disables automatic collection/index creation before database connection;
  it never invokes model seeding. Unexpected native errors return only a fixed refusal.
- Report `ky.librechat-account-report/1`: exact module version/native ID, SHA-256 binding
  digest of issuer+NUL+subject, and ADMIN/USER role. No native profile, subject, tokens or
  database credentials leave this inspection interface. Role readback is not proof of
  actual administrator capability coverage, ownership receipts or workflow separation.
- `client.go` adds the explicit container-bound native Go consumer: canonical target paths,
  bounded private input, capability discovery, strict unknown-field/report/version/source/ID
  validation, 30-second execution timeout, fresh owned request directory per invocation and
  cleanup on both success and refusal. Subject is sent through stdin to a private request
  file, never included in command argv. Errors omit native diagnostics.

Verification scope and evidence:
- `librechat-native-inspection-unit.log`: terminal exit 0; real protected-filesystem tests
  cover normal read/nonmutation, world-readable input, symlink, FIFO, unsafe ancestor,
  public-root refusal, invalid/oversized/malformed UTF-8/JSON, unknown fields and invalid
  issuer/subject. Native inspection tests use a substituted model boundary and verify
  exact provider/source/subject/ID, ordinary/admin readback and no profile/data mutation.
- `librechat-inspection-final-race.log`: terminal exit 0, package 1.402s. Native module tests
  run from Go with bounded Node heap; actual production CLI capability discovery works with
  a nonexistent app root and foreign-release CLI refusal returns only the fixed diagnostic.
  Real shell/Node private staging executes the module reader/inspector twice and on a foreign
  subject, then proves all per-invocation directories are removed. Report pins, missing/
  wrong fields, oversized/suffixed/private-field reports, pre-I/O invalid bindings and
  unsupported capability refusals pass. Native storage in this transport fixture is substituted.
- The pinned upstream ban/JWT/admin-router prerequisite probe continues to pass.

DOX: native module has its own AGENTS.md; parent LibreChat purpose/ownership/contracts/index,
adapter parent ownership/index and catalog evidence are updated. Root Child DOX index and
other product contracts intentionally unchanged: existing adapters ownership still contains
this boundary. Module README records execution, target-secret boundaries and verification
limits. No initial setup/revocation/resident/deployment capability is falsely advertised.

Unverified: the actual installed native packages and MongoDB/container transport, all native
identity writers, initial OIDC/account setup ownership, persistent revocation and every native
authorization hook, actual owner/everyday/OIDC proofs, and consistent backup/restore/release
provenance. Next implement native persistent retained-account revocation with source/user
fences, native session/credential cleanup and authentication guards, then qualify the native
container and signed identity flows before resident delivery. Preserve existing native users,
roles/profiles, chats and files; never substitute expiry bans or destructive user deletion.

All preceding Ky and other third-party adapter lifecycle/configuration/backup/admin/guided
acceptance gates remain in scope. No live deployment, existing data/credentials, commit, PR
or production release changed. The goal is unfinished, and no human decision blocks progress.

Final verification: `librechat-inspection-final-ci.log` terminal exit 0 for serial,
memory-limited `make ci build`. Native probe source was configured explicitly; the new
module/CLI and SDK tests ran. Tidy, gofmt, ordinary/e2e-tag vet, all package race tests
and both binaries pass. `git diff --check` passes.


## LibreChat actual native MongoDB/CLI/SDK inspection qualified — 2026-10-10

Previous goal turn classification: **progress** (implemented native inspection module and
Go consumer). Current turn replaces the outstanding model/transport substitute with actual
pinned native packages and MongoDB evidence. The original all-product objective stays active.

Native build evidence, isolated disk cache only:
- Clean source v0.8.8 commit `e8f3be08623663d4ad7f7241e693c94469b63bb0`; final
  `git diff HEAD --exit-code` passes, including staged/unstaged tracked source.
- `librechat-native-dependencies.log`: terminal exit 0, pinned-lockfile `npm ci --ignore-scripts`
  (2982 packages). Lifecycle scripts were not run automatically. No package lock edits.
- `librechat-native-data-provider-build.log`, `librechat-native-data-schemas-build.log`,
  `librechat-native-api-build.log`: each terminal exit 0, serial upstream package builds.
  Node container was capped at 1536 MiB/no extra swap, one CPU, 128 pids and 1024 MiB heap.
  Later builds also bound Rayon and libuv threads. Native API package build passed although
  the final inspection path deliberately does not use its broad model/cache loader.
- Native build/runtime Node image: `node@sha256:5711a0d445a1af54af9589066c646df387d1831a608226f4cd694fc59e745059`
  (fixture Node v24.18.0). MongoDB 8.0.20, matching upstream Compose's selected version:
  `mongo@sha256:098862b1339f031900ca66cf8fef799e616d6324fa41b9a263f2ec899552c1ef`.

Actual execution found native initialization facts hidden by the unit model substitute:
- Native shared methods require schemas to be registered first. The inspection CLI now uses
  the product's `createModels(mongoose)` and `createMethods(mongoose)` directly, under the
  existing `runAsSystem` boundary, with automatic collection/index creation disabled.
- The broad `api/models/index.js` loader initializes unrelated cache stores. Inspection avoids
  that loader rather than granting it configuration/cache mutation authority.
- Importing the native data-schemas library opens its log destination. The fixture uses the
  supported `LIBRECHAT_LOG_DIR=/fixture/logs`, preserving a read-only native source mount.
  Logging is not claimed to be absent; product database/configuration changes remain forbidden.

Added `native_mongo_qualification.py` and a native Go helper. The fixture checks clean source
commit, required compiled native artifacts and immutable Node/Mongo image pins. It owns a
unique network, unexposed MongoDB (768 MiB, one CPU) and Node worker (512 MiB, one CPU,
256 MiB heap), plus private disk-backed scratch/logs. Native fixture users are disposable,
created through the native schema, not any live database. Cleanup removes only its named
containers/anonymous volumes/network and its owned scratch.

`librechat-native-mongo-inspection.log`: terminal exit 0:
- Real compiled native schemas/shared `getUserById`, actual MongoDB and production module
  CLI inspect ordinary USER and ADMIN records with exact native ID/configured issuer/subject.
- Foreign subject, local provider and missing native ID refuse with only the fixed diagnostic;
  successful reports expose neither subject nor native email/profile.
- Complete snapshots of every native collection, its options/indexes and all documents remain
  exactly equal before/after inspection. No collection, index or document creation/update.
- Actual Go SDK over container exec runs inspection twice and foreign refusal, preserving
  native ID/role/binding proofs. Helper race suite passes (3.044s); request staging directories
  are absent afterward, and the full native database snapshot still matches.
- All owned build and MongoDB fixture containers are gone after terminal cleanup. No live
  service, credentials or existing user/content data were mounted or changed.

DOX pass updates LibreChat/module ownership and verification, command loader/logging contracts,
module README and catalog evidence. Existing root/parent Child DOX indexes are unchanged;
these additions stay inside the already indexed LibreChat/module boundaries. The source probe
still explicitly identifies its substituted storage boundary; this new fixture is the actual
native package/MongoDB/CLI proof, not a full native server OIDC/authentication proof.

Remaining: persistent revocation with account/source fences, native session/agent credential
cleanup, coordinated native identity writers and all authentication paths; actual signed
KyIdentity owner/everyday/assigned-only flows and administrator workflow separation; native
initial setup ownership/interface and resident lifecycle; full backup/restore/provenance and
production/guided suite acceptance. Account inspection does not adopt or provision anyone,
grant administrator capabilities or enable a deployable catalog entry. Existing Ky suite,
Forgejo, BookStack, Immich, Vaultwarden and other catalog gates remain in scope. The next
implementation action remains native persistent retained-account revocation, using native
cleanup methods and authorization hooks rather than the expiring ban operation.

No commit, PR, production release or live deployment changed. Full goal unfinished; no human
input blocks further local implementation.

Final verification: `librechat-native-mongo-final-ci.log` terminal exit 0 for serial,
memory-limited `make ci build` after the native loader correction and DOX changes. Tidy,
gofmt, ordinary/e2e-tag vet, all package race tests and both binaries pass. The source
prerequisite probe is configured; real MongoDB qualification is the separate terminal
fixture above, not inferred from the CI helper's skip. `git diff --check` passes.


## LibreChat internal atomic revocation primitive qualified — 2026-10-10

Previous goal turn classification: **progress** (real native MongoDB/model/CLI/SDK inspection).
Current turn implements and verifies the next required native primitive. The all-product
objective remains unchanged and unfinished; no human decision blocks continued local work.

Added product-internal `module/revocation.cjs` and actual-MongoDB verification
`module/revocation.native.cjs`, run by the existing disposable fixture:
- `ky.librechat-revocation/1` takes exact native ID/configured issuer/stored subject with
  explicit check/revoke. Reuses native inspection; malformed actions/fields, foreign identity,
  unsupported roles and duplicate native source/subject mappings refuse before publication.
- A compare-and-set on the exactly matching native User publishes `kyquickstartRevocation`
  with source/subject digest, native ID, operation UUID, timestamp and local publication audit
  in one journaled majority write. Identity/fence/audit binding is a single atomic invariant;
  it works on upstream's standalone MongoDB without a new shared lock or global counter.
  Module retries never overwrite a retained record or rewrite its operation/audit identity.
- Inspect publication before invoking native `deleteAllUserSessions` and
  `deleteAllAgentApiKeys`. Unknown committed publication/cleanup replies require inspection,
  never a claimed rollback. Exact repeats preserve native role/profile/identity and content.
- `ky.librechat-revocation-proof/1` describes physical state only: publication, active session
  and agent-key counts, retained operation ID and `session_and_agent_keys_cleared`. The latter
  covers those two native stores only; it is not a claim about stateless JWTs, passwords,
  provider tokens, every credential kind or authenticated access denial.
- Account/subject guard functions read native records without cache and fail closed on corrupt
  records, missing accounts and lookup failures. Errors contain fixed diagnostics only.
  These helpers are **not yet integrated into server authentication or native writers**.
  Public CLI capabilities therefore remain read-only account inspection; no Go offboarding
  consumer, resident revocation connector or deployable LibreChat catalog entry is enabled.

`librechat-native-revocation-physical-proof.log`: terminal exit 0 after final wire field naming:
- Existing native MongoDB/CLI inspection, complete database nonmutation snapshots and Go SDK
  repeat/refusal/staging checks still pass (native SDK helper package 3.138s).
- Real native Session and AgentApiKey schemas seed active/expired sessions and a retained
  other account's credentials. Real native Conversation schema seeds authored chat content.
- Foreign/unknown requests and duplicate source/subject identity refuse with no native User
  mutation. A committed User publication with an artificially lost reply retains a valid
  record/audit, leaves credentials untouched, and explicit guards deny that ID/subject.
- A lost reply after actual native session deletion leaves agent keys for inspection/retry.
  Same operation ID survives both lost replies; retry clears keys and repeat returns the same
  proof. Native all-session deletion removes the expired session too. Other account's native
  session/key remain. Original native User document fields match exactly after excluding only
  the new publication field; role/profile and authored conversation content are retained.
- Corrupt binding digest refuses inspect/revoke/guards and is never repaired by the module.
  Native lookup failures in guards and prepublication reads surface only fixed diagnostics.
- Test failure handlers now disconnect MongoDB so a validation error cannot hold the fixture
  open and hide diagnostics until timeout. The original missing native conversation endpoint
  fixture field was corrected; no production behavior was relaxed.
- Terminal cleanup leaves no owned LibreChat containers/network/volumes. All operations use
  disposable data, digest-pinned images, existing bounded CPU/memory and disk-backed scratch.

`librechat-revocation-core-ci.log`: terminal exit 0 for serial memory-limited `make ci build`
after the core error sanitization and DOX changes. Tidy, gofmt, ordinary/e2e-tag vet, all race
suites and both binaries pass. The final internal proof field rename was additionally verified
by the real native fixture above; the public inspector/Go binary contracts did not change.
Node syntax check and `git diff --check` pass.

DOX updates module purpose/ownership/contracts/verification, LibreChat and adapter parent
ownership, module README and catalog evidence. Root and Child DOX indexes stay unchanged;
these files remain inside existing owned module boundaries and introduce no user preference
or new deployment domain. The local publication audit is immutable by module operations;
coordinating all other native writers and cleanup completion/native audit relay remain gates.
Back up the User publication field with the native database; no reactivation path is provided.

Next: wire guards into native JWT acceptance, verified OIDC subject/account handling before
registration/linking, token issuance/refresh, agent API-key/machine authentication and optional
cookie-based file access. Coordinate native User deletion/replacement/identity/record writers
and session/key creation; qualify actual signed old/fresh access denial, durable completion
auditing and restart/restore before exposing a public revocation command/SDK/connector. A
physical record alone does not protect stock server routes, so do not deploy this primitive
as finished offboarding. Initial native apply-setup ownership/configuration, administrator
workflow separation and the full suite/cross-product lifecycle/backup/release/guided gates
remain in scope. Vaultwarden only; existing KyVault and all live services/data stay untouched.
No commit, PR, production release or live deployment changed; goal stays active.


## Recovery checkpoint — native JWT guard, 2026-10-10

Recovered after the reported memory termination; the interrupted `runtime.cjs` and
`source.patch` were present. Work remains owned by basalt on myslop. All heavy checks ran
serially with GOMAXPROCS=2, GOMEMLIMIT=1GiB, GOFLAGS=-p=1 and disk-backed TMPDIR; disposable
Node and MongoDB containers retained their 512/768 MiB hard limits, one CPU and bounded pids.
No live service, existing product data, commit, PR or deployment changed.

The candidate native patch covers JWT acceptance, OIDC subject/account handling, token
issuance, agent user resolution and optional cookie file authentication. The fixture applies
it only to owned copies of the pinned clean v0.8.8 checkout and syntax-checks all five files.
`jwt.native.cjs` executes the patched native Passport strategy using actual native models,
MongoDB and JWT cryptography. Only its application model alias resolves through the fixture;
it calls real native methods. Valid tokens are accepted before record publication. Invalid
signatures and previously issued/freshly signed valid tokens after publication are refused;
another account retains access. Corrupt records refuse, guard lookup errors fail closed,
and existing lost-reply, repeat, scoped cleanup and retained user/chat checks still pass.

Verification: `librechat-jwt-native-final.log` terminal exit 0 (native CLI/SDK nonmutation,
patch syntax, real MongoDB primitive and signed Passport strategy). `librechat-jwt-ci.log`
terminal exit 0 for `make ci build` (tidy, format, vet including e2e tag, all race tests,
workstation/resident binaries). `git diff --check` and clean pinned source checks pass.
Owned fixture containers/volumes/network are cleaned by the runner.

DOX updates the existing module and LibreChat boundary docs, module README and catalog
proof/limits. Root, adapters parent and Child DOX indexes intentionally stay unchanged:
no new boundary, selection preference, deployment contract or ownership domain is added.
The all-product goal remains active; this does not admit LibreChat or enable public revoke.

Next: qualify the other candidate hooks against running native HTTP and signed OIDC;
cover alternate authentication paths, concurrent User identity/record/deletion/replacement
and session/key writers, cleanup completion auditing/native audit relay and restart/restore.
Then build product-owned initial setup/ownership, public revoke/SDK/resident connector and
administrator purpose separation. Physical cleanup and this JWT proof alone do not satisfy
complete offboarding. Preserve the broader suite lifecycle, backup, release provenance and
guided deployment gates; Vaultwarden only, older KyVault untouched.


## Native cookie and issuance checkpoint — 2026-10-10

Previous goal turn classified as progress: candidate source patch and real native signed-JWT
revocation evidence changed authoritative state. Board checked since post 2693: claimed by
basalt with no newer posts. Continued isolated LibreChat qualification without changing live
services, product data, commits, PRs or deployments.

Extended the native probe to execute patched `setAuthTokens` and `optionalShareFileAuth`
with actual native methods, MongoDB sessions/hashes and signed refresh/access tokens.
Cookie middleware runs on ephemeral loopback Express HTTP inside the owned fixture container;
no host ports are exposed. Configuration/email dependencies are unused refusing fixture
boundaries, not qualified integrations. This is not the full LibreChat server or share ACL.

Fixed runtime account guards to accept actual native ObjectId values as well as strings;
stock `setAuthTokens` legitimately takes both. Issuance now passes with either before
publication. Native issued access tokens and refresh cookies authenticate before revocation;
missing/invalid signed cookies refuse. A committed publication with a lost reply denies the
still-valid daily refresh cookie before cleanup, while the owner's cookie continues to work.
New token issuance for the recorded account refuses and the complete session collection
remains unchanged. Existing atomic publication, retained content, credential cleanup,
corruption/lost-reply/repeat and old/fresh signed JWT checks continue to pass.

Verification: `librechat-cookie-native.log` terminal exit 0, including production CLI/SDK
nonmutation and staging cleanup, patch syntax, real native MongoDB/Passport and cookie HTTP/
issuance evidence. `librechat-cookie-race.log` terminal exit 0 for the affected Go package
race tests/private-file checks. `git diff --check` and pinned checkout `git diff HEAD
--exit-code` pass. Owned fixture containers, volumes and network are removed. Heavy work was
serial under GOMAXPROCS=2/GOMEMLIMIT=1GiB/GOFLAGS=-p=1 and disk-backed TMPDIR; Node/MongoDB
retain 512/768 MiB hard caps, one CPU and bounded pids. No new Go binary behavior changed;
the previous full CI/build result remains relevant, with this turn's changed native runtime
additionally executed by the actual fixture.

DOX updates module and LibreChat contracts/verification, module README and catalog evidence.
Root, adapters parent and indexes intentionally stay unchanged: existing boundaries and
ownership still apply; no new deployment contract or preference. Public CLI stays read-only,
LibreChat admission remains closed, and the full all-product goal remains active.

Next: qualify native agent authentication and signed OIDC registration/linking/refresh paths,
find and cover alternate authentication paths; coordinate identity/record/deletion/replacement
and session/key writers against concurrent revocation; add cleanup completion auditing/native
audit relay and restart/restore. Then complete product-owned initial setup/ownership, public
revocation SDK/resident integration and dedicated administrator purpose separation. Preserve
all remaining suite lifecycle/backup/provenance/guided acceptance gates and existing data.
Vaultwarden only; old KeePass KyVault untouched. Token issuance guard-before-write is not a
proof against concurrent revocation and session creation; writer coordination is still required.


## Native agent API-key checkpoint — 2026-10-10

Previous turn classified as progress: native cookie identity and token issuance proofs plus
ObjectId compatibility changed authoritative evidence. Board checked since post 2694:
claimed by basalt, no new posts. Continued the full all-product objective with disposable
LibreChat authentication qualification; no live services, product data, commits, PRs or
release/deployment state changed.

The real native fixture now generates agent API keys through `createAgentApiKey`, retaining
native random generation, hashing, credential model writes, validation, user resolution and
principal checks. The patched actual agent middleware loads native authentication factories.
Explicit fixture configuration enables API-key mode and disables OIDC; unused email and
agent permission boundaries refuse. This is an authentication qualification, not actual
agent authorization, signed OIDC/M2M, config delivery or strict multi-tenant acceptance.

The first run failed with native HTTP 403 because the fixture put the request in system
tenant context. Native request middleware correctly rejects that context. Removed the
fixture system-context wrapper; HTTP requests now execute normal request context, while
native product operations retain their required explicit system context. No production
protection was weakened to make the test pass.

With normal request context, native generated keys authenticate over isolated Express HTTP
before publication; missing/invalid keys refuse. A real committed revocation publication
with a lost reply denies the daily key while the credential row still exists and before
cleanup. After cleanup the same key remains denied, while the owner's key keeps access.
All earlier CLI/SDK nonmutation, native atomic publication/lost-reply/repeat/content,
Passport signed-token and cookie/issuance checks still pass.

Verification: `librechat-agent-native.log` terminal exit 1 records the fixture system-context
failure; `librechat-agent-native-fixed.log` terminal exit 0 records the corrected native
HTTP run, all source patch syntax checks and preceding native evidence. Affected package
race tests pass in `librechat-agent-race.log` (terminal exit 0). `git diff --check` and clean
pinned native source `git diff HEAD --exit-code` pass. Owned fixture containers/volumes/
network removed. Serial checks kept GOMAXPROCS=2, GOMEMLIMIT=1GiB, GOFLAGS=-p=1 and disk TMPDIR,
with Node/MongoDB hard caps 512/768 MiB, one CPU and bounded pids. Go binary contracts and
production source hooks did not change this turn; the new proof qualifies the existing hook.

DOX updates LibreChat/module verification, module README and catalog proof/limits. Root,
adapters parent and Child DOX indexes stay unchanged because existing boundaries, ownership
and deployment contracts still apply. Public CLI remains inspection-only; LibreChat admission
stays gated and the all-product goal active.

Next: actual signed remote OIDC and M2M token/account binding, native browser OIDC registration/
linking/refresh and alternate authentication paths; coordinate native User and credential
writers with revocation, durable completion audit and restart/restore. Product-owned initial
setup/ownership, public revoke SDK/resident integration, dedicated administrator purpose
separation and all suite lifecycle/backup/provenance/guided acceptance gates remain in scope.
Vaultwarden only; older KyVault and all existing installations/data stay untouched. Denied
API-key validation may still update its native last-used metadata before user resolution;
this proof claims no authenticated identity, not no credential metadata write.


## Signed remote OIDC/M2M checkpoint — 2026-10-10

Previous turn classified as progress: real native generated agent API keys and HTTP denial
before/after cleanup changed evidence. Board checked since post 2697: claimed by basalt,
no newer posts. Continued isolated LibreChat native qualification; full all-product goal
remains active. No live services, existing product data, commits, PRs or deployments changed.

The owned worker now serves JWKS over ephemeral loopback HTTPS using a disposable private
TLS key/certificate and trusts that CA only through its `NODE_EXTRA_CA_CERTS`. TLS verification
is preserved. RSA token keys live in worker memory. Native `@librechat/api` fetches the JWKS
and verifies RS256 without substituting its verifier; patched native agent factories then
resolve actual native source/subject and tenant-bound machine principal/client bindings.
Seeded daily/owner native users explicitly use the fixture tenant. Fixture configuration is
explicit and limited to these modes; unused email/permission dependencies still refuse.

Native signed remote-user and machine tokens authenticate before publication. Invalid
signatures, expired machine tokens, foreign issuer/audience, missing required scope, unknown
machine client, wrong machine subject and conflicting client identifiers refuse. Actual
revocation publication denies old and freshly signed remote-user/machine tokens before
credential cleanup, while owner tokens continue working. The old tokens remain refused after
cleanup. All prior native CLI/SDK nonmutation, atomic record/lost-reply/repeat/content, native
JWT/cookie/issuance and API-key checks continue to pass. The owned key server closes in finally;
TLS files, containers, volumes and network are removed with the fixture.

Verification: `librechat-oidc-native.log` terminal exit 0 for initial signed native transport;
`librechat-oidc-native-final.log` terminal exit 0 adds signature/expiry/unbound-client negatives
and post-cleanup denial. `librechat-oidc-race.log` terminal exit 0 for affected package race/
private-file checks. Source patch syntax, `git diff --check` and pinned native checkout
`git diff HEAD --exit-code` pass. Checks stayed serial with GOMAXPROCS=2, GOMEMLIMIT=1GiB,
GOFLAGS=-p=1, disk TMPDIR and Node/MongoDB 512/768 MiB hard caps, one CPU, bounded pids.

DOX updates existing LibreChat/module verification/contracts, module README and catalog
proof/limits. Root, adapters parent and indexes remain unchanged: no new ownership domain,
user preference or deployment contract. CLI remains read-only and LibreChat admission gated.
These proofs qualify native remote authentication with controlled HTTPS JWKS/configuration;
they do not prove KyIdentity browser OIDC, production configuration delivery, strict
multi-tenant acceptance, actual agent authorization or the full native server.

Next: native browser OIDC registration/linking/refresh and alternate auth paths, ideally
against disposable actual KyIdentity; protect concurrent User identity/record/deletion/
replacement and session/key writes, completion auditing/native audit relay and restart/restore.
Then product-owned initial setup/ownership, public revoke SDK/resident integration and
administrator purpose separation. All remaining suite lifecycle/backup/provenance/guided
gates stay in scope. Vaultwarden only; older KyVault and live installations/data untouched.


## Actual KyIdentity browser callback checkpoint — 2026-10-10

Previous turn classified as progress: signed remote OIDC/M2M native HTTPS/crypto and revocation
proofs changed authoritative evidence. Board checked since 2698: claimed by basalt, no newer
posts. Continued the full all-product objective with disposable browser-flow qualification;
no live deployment, product data, commit, PR or production release changed.

`native_mongo_qualification.py` now optionally accepts an explicit absolute
`KYQ_TEST_IDENTITY_BINARY`. Selected the existing local static fixture artifact
`/home/yoshi/.cache/kyquickstart-work/kyidentity-fixture`, SHA-256
`a44f86ffa791d0559d4c514da0be6a0cfae3c7a635655d477c7793f7666336bf`.
`browser.native.cjs` runs it inside the owned Node worker with protected private setup files,
GOMAXPROCS=1/GOMEMLIMIT=128MiB, no host ports and fixture-local data. Native KyIdentity
`apply-setup` creates administrator/everyday accounts and the assigned-only OIDC client.
A verified HTTPS proxy fronts real KyIdentity; the callback also uses verified HTTPS. Private
callbacks are explicitly allowed only in the owned fixture and both runtimes trust its CA.

The first browser run was refused by actual identity setup because the callback was HTTP.
Changed the fixture callback to HTTPS, preserving the product requirement. The native
LibreChat OpenID strategy executes actual discovery, Passport session/PKCE, code exchange,
provider userinfo and native account methods with explicit fixture config, native Keyv memory
cache and unused refusing avatar/email boundaries. Ordinary first login creates the native
USER account. A seeded USER owner becomes ADMIN from the actual signed app-scoped `roles`
claim. After real account/subject revocation, fresh complete authorization/PKCE login for the
daily subject refuses with 403 before changes to the entire native User/session collections;
control administrator login remains accepted. No setup ownership or real admin route/purpose
proof is inferred from a native role projection.

Verification: `librechat-browser-native.log` terminal exit 1 records the HTTPS callback
requirement; `librechat-browser-native-tls.log` terminal exit 0 qualifies existing browser
accounts; `librechat-browser-native-create.log` terminal exit 0 additionally qualifies ordinary
registration and signed app ADMIN role projection. All preceding native inspection/SDK,
record/lost-reply/repeat/content, JWT/cookie/issuance, API-key and remote OIDC/M2M checks pass
in the same runner. `librechat-browser-race.log` terminal exit 0 for the affected package.
`git diff --check` and clean pinned LibreChat source checks pass. Owned identity process,
HTTPS servers, private setup/TLS data, Docker containers/volumes/network are cleaned up.
Checks remain serial with disk TMPDIR, GOMAXPROCS=2/GOMEMLIMIT=1GiB/GOFLAGS=-p=1 and existing
Node/MongoDB 512/768 MiB hard caps, one CPU, bounded pids.

DOX updates existing module/LibreChat contracts and verification, module README and catalog
proof/limits. Root, adapters parent and Child DOX indexes are unchanged because ownership
boundaries and deployment contracts remain current. Public CLI stays read-only, LibreChat
admission remains gated and the all-product goal active.

Next: inventory/protect alternate native auth paths (images/proxy/reused provider tokens,
refresh and local account flows), coordinate concurrent identity/record/delete/replace and
session/key writers against revocation, add durable completion auditing/native audit relay
and restart/restore. Then product-owned initial setup/ownership, public revoke SDK/resident
integration and full administrator capability/purpose separation. The exercised strategy is
qualified; full LibreChat routes, configuration delivery and production release provenance
remain open. Preserve all suite lifecycle/backup/guided gates, existing data and services.
Vaultwarden only; older KeePass KyVault stays untouched.


## Native image/common bearer checkpoint — 2026-10-10

Previous turn classified as progress: actual KyIdentity browser discovery/PKCE/registration/
signed app ADMIN role and fresh revoked login evidence changed state. Board checked since
2701: claimed by basalt, no newer posts. Continued full all-product development without live
services/data, commits, PRs or release/deployment changes.

Inventoried native auth boundaries, including direct image cookie authorization, common
Passport/RUM and refresh/provider-reuse paths. Added candidate native source hooks at common
Passport successful-user boundaries (including RUM) and secure image `findSession` dependency.
Image guard checks the viewer's current native account state before accepting the session;
it preserves the native owner lookup rather than hiding revoked ownership/ACL records.
The native normal refresh controller reaches the already guarded `setAuthTokens`, but its
complete controller/provider-reuse paths are not yet qualification evidence.

Actual native secure image cookie authorization over isolated HTTP works before publication.
After actual committed revocation, while the refresh session still exists, it denies the
recorded user with native 403 and retains control image access. Anonymous secure-image access
returns 401. Common Passport bearer HTTP denies the revoked user and preserves control access.
First run's assertion expected 401 for a supplied-but-refused image cookie; native semantics
correctly returned 403. Corrected the assertion without changing the native response behavior.

Verification: `librechat-images-native.log` terminal exit 1 records that assertion mismatch;
`librechat-images-native-fixed.log` terminal exit 0 includes the corrected native image/common
bearer checks plus all previous actual native MongoDB/CLI/SDK, primitive/content, JWT/cookie/
issuance, API-key, remote OIDC/M2M and actual KyIdentity browser proofs. All seven candidate
source patch files syntax-check. `librechat-images-race.log` terminal exit 0 verifies the
affected package. `git diff --check` and pinned upstream clean source check pass. Owned
fixtures cleaned up. Heavy checks stayed serial with disk TMPDIR and existing Go, Node,
MongoDB and identity process memory/CPU/pid limits.

DOX updates existing module/LibreChat contracts and verification, module README and catalog
limits. Documented candidate image packaging: runtime/revocation/account must be installed
under `/app/api/kyquickstart` for the native alias; private CLI placement can stay separate.
Root, adapters parent and Child DOX indexes unchanged because ownership boundaries and
installer deployment contracts remain current. No deployable release or admission is claimed.
Public inspector continues to refuse provider token reuse and expose no public revocation.

Next: qualify RUM and reused-provider strategy/cache success, legacy image reuse and full
refresh/local authentication controllers; coordinate User identity/record/delete/replace and
session/key writers with revocation, durable completion auditing/native audit relay and
restart/restore. Product-owned initial setup/ownership, public revoke SDK/resident integration,
full administrator capability/purpose and all suite lifecycle/backup/provenance/guided gates
remain in scope. Shared-image ACL coverage is not inferred from own-image access; the new
image guard must not erase retained owner metadata. Vaultwarden only; existing KyVault/data
and all live services untouched. Full all-product goal remains active.


## Native User writer protection checkpoint — 2026-10-10

Previous turn classified as progress: secure image/common bearer hooks and actual native
HTTP evidence changed state. Board checked since 2702: claimed by basalt, no new posts.
Continued full all-product development with native User writer protection; no live services,
existing product data, commits, PRs or deployment/release changes.

`writers.cjs` installs native Mongoose User hooks before model compilation in both native db
entry points. Native query mutations, replacement/deletion, save and bulk operations require
record absence in their actual MongoDB predicates. Ordinary model writers cannot supply or
rename `kyquickstartRevocation`; pipelines refuse. Registration is idempotent and late first
installation refuses. Added the field as private/select-false immutable schema metadata.
Raw collection publication deliberately bypasses ordinary hooks inside the owned module;
raw maintenance/restore and other privileged writers are still unqualified.

Publication now requires absence of native `agentTriggerDeletionStartedAt` and removes
`expiresAt` in the same journaled majority update. Prior deletion admission causes publication
to refuse without publishing a record. After publication, native deletion admission cannot
acquire the User. Actual native profile/identity updates, delete, replacement, updateMany,
bulk and document save/delete attempts leave the exact retained User unchanged, including a
document loaded before publication. Native attempts to supply/remove the record refuse.
Active control update/delete and actual browser registration still work. Existing native
content and all earlier authentication proofs remain intact.

While reviewing generated db source, caught and corrected an intermediate patch that assigned
`register()` to `models` rather than retaining `createModels()` exports. Added actual execution
of the patched native db/models module with a native User export check; final verification
includes that check. The first writer fixture passed model-hook behavior but predates this
export qualification and must not be used as release proof.

Verification: `librechat-writers-native-final.log` terminal exit 0 covers all nine source patch
syntax checks, native db model export execution, real MongoDB writer/deletion ordering and
all preceding CLI/SDK nonmutation, atomic record/content, JWT/cookie/issuance, image/bearer,
API-key, remote OIDC/M2M and actual KyIdentity browser checks. `librechat-writers-race.log`
terminal exit 0 checks the affected package. `git diff --check` and clean pinned native source
checks pass; owned fixtures are removed. Heavy work stayed serial with disk TMPDIR, bounded
Go concurrency/heap, Node/MongoDB hard caps, one CPU/pid limits and bounded identity child.

DOX updates existing module/LibreChat contracts/verification, README packaging (also include
writers.cjs under /app/api/kyquickstart) and catalog proof/limits. Root, adapters parent and
Child DOX indexes stay unchanged: existing boundaries and deployment ownership still apply.
CLI stays read-only and LibreChat admission gated; full all-product goal remains active.

Next: coordinate session/key creation and simultaneous new-account/source claims with
publication, audit privileged raw writers and complete deletion-controller orchestration;
qualify provider reuse/RUM/legacy images and full refresh/local flows, completion auditing/
native audit relay and restart/restore. Product-owned initial setup/ownership, public revoke
SDK/resident integration, administrator capabilities/purpose and all suite lifecycle/backup/
provenance/guided gates remain in scope. Model hooks are not a proof against arbitrary raw
MongoDB writes or cross-collection creation races. Preserve existing data/services; Vaultwarden
only and older KeePass KyVault untouched.


## Native credential write admission checkpoint — 2026-10-10

Previous turn classified as progress: native User model writer guards, deletion ordering and
retained-record evidence changed state. Board checked since 2705: claimed by basalt, no
newer posts. Continued full all-product development with credential publication coordination;
no live services/data, commits, PRs or production deployment/release changes.

`credentials.cjs`, registered by the existing model guard, gives each native session/key
write one private UUID admission key on its User. Source, publication fence and write
admission must be one atomic account invariant on standalone MongoDB: a writer claims only
before publication; publication and User mutations require an empty claim map. This is
account-local coordination, not a global installer lock. Normal/error completion releases
only the owned UUID. Bound 32 claims and validate model/generation/PID/time; no credentials
are stored in claim metadata. Claims/releases are journaled majority writes. Save and
single-owner query writers are guarded; batch credential writes refuse. Native credential
owner removal/rename/rebinding refuses. Privileged raw writers remain outside this boundary.

`credential_writes_in_flight` now joins the internal physical-state proof. Cleanup success
requires zero claims; read and validate that state before cleanup even for repeated revoke.
Actual native session/key inserts held after MongoDB insertion block publication and User
mutation until admission release. After publication native session/key creation refuses
before insertion. All previous login/browser, source/role, image, token and data-retention
proofs still pass with admission enabled.

Failure tests use actual MongoDB writes. A failed insert releases admission and leaves no
credential. A lost claim reply preserves one inspectable claim that blocks publication,
without inserting a credential. Only the known terminated fixture caller's exact injected
claim is cleared by the fixture; this is not production dead-owner recovery. A lost release
reply leaves an inspectable committed credential and zero claims, never a claimed rollback.
Corrupt claim data refuses, including on an already published record. Production must never
infer a dead owner from age/PID alone; the confirmed-offline recovery interface is still absent.

Verification: `librechat-credentials-final-native.log` terminal exit 0 covers native model
registration/db exports, all nine source syntax checks, credential race/unknown outcomes,
User/publication/content, native CLI/SDK nonmutation, JWT/cookie/issuance/image/bearer, API-key,
remote OIDC/M2M and actual KyIdentity HTTPS/PKCE browser registration/ADMIN projection.
`librechat-credentials-race.log` terminal exit 0 checks the affected package. `git diff
--check`, clean pinned native source and owned fixture cleanup pass. Heavy work stays serial
with disk TMPDIR, bounded Go concurrency/heap and existing Node/MongoDB/identity hard memory,
CPU and pid limits. CLI inspection and Go binary contracts are unchanged; no public revoke.

DOX updates existing module/LibreChat ownership/contracts/proofs, README packaging (also
credentials.cjs under /app/api/kyquickstart) and catalog proof/limits. Root, adapters parent
and Child DOX indexes unchanged: existing boundaries and deployment authority remain current.
LibreChat admission stays gated and the full all-product goal active.

Next: confirmed-offline dead-owner recovery/restart/restore, complete native query upsert/
rotation/batch and deletion-controller coverage, simultaneous new-account/source claims and
privileged raw writer audit. Provider reuse/RUM/legacy images/full local/refresh controllers,
durable cleanup audit/native relay, product-owned initial setup/ownership, public revoke
SDK/resident integration and administrator capability/purpose remain in scope, along with
all suite lifecycle/backup/provenance/guided gates. Preserve existing data/services, Vaultwarden
only and older KeePass KyVault untouched. A stale admission is an inspection/recovery gate,
not a reason to silently publish or report cleared state.


### Resume after reported memory termination — 2026-10-10

Recovered the active all-product goal and verified the worktree on `feat/phase-4-edge`.
The latest native credential/browser fixture and affected race logs retain their passing
results; no owned LibreChat fixture containers remain. Shared board remains claimed by
basalt, with no newer posts after 2706. No live deployment was performed, and no new
recovery implementation had been written before this interruption. The cause of this
reported termination has not been established; passing fixture limits do not establish
that the agent process itself stayed within a memory limit.

Continue heavy work serially with the existing root Work Guidance: disk-backed TMPDIR,
bounded Go concurrency/heap, Node worker bounds and hard fixture container limits. Keep
source and tool output focused instead of printing the growing checkpoint or multiple
complete instruction files together. Existing root/module ownership and Child DOX
indexes remain current; no contract change is introduced by this recovery checkpoint.

Next concrete implementation: an internal confirmed-offline recovery primitive for
abandoned credential admissions. Never infer dead writers from age or PID alone. Require
explicit all-writers-offline confirmation, pin the exact validated claim-map digest and
native account/source binding, and clear claims together with a bounded local audit
receipt in one account compare-and-set. Lost replies must be inspectable and retry against
the same receipt. Qualify with an owned actual Node writer stopped after a native MongoDB
credential insert; preserve profiles/content and keep the public CLI read-only until
admission gates pass. This primitive and its native fixture are not implemented yet.
The broader all-product, setup/ownership, lifecycle, recovery, provenance and guided
acceptance gates recorded above remain open. Vaultwarden only; preserve existing KyVault.


### LibreChat confirmed-offline credential recovery — 2026-10-10

Implemented product-internal `module/credential-recovery.cjs` for
`ky.librechat-credential-recovery/1`. Require exact native ID/configured HTTPS issuer/subject,
explicit `all_writers_confirmed_offline: true`, UUID operation ID and the exact validated
claim-map SHA-256. Canonical digest is UTF-8 JSON of sorted
`[UUID,generation,model,PID,started_at]` tuples. Never infer offline state from age/PID.
A journaled majority account compare-and-set clears that precise map and appends an immutable
private `kyquickstartCredentialRecovery` receipt in the same write. Validate bounded history
(maximum 64), bindings and existing publication records; refuse corrupt/exhausted history,
stale maps, duplicate identities and existing native deletion admission. Read publication
validation from the same raw account snapshot used by the compare-and-set.

Lost write replies require inspection, never a rollback claim. Repeating the operation ID
returns its existing matching receipt and reports current pending claims; it cannot clear
newer writer state. Recovery retains sessions/keys and all user/profile/content data.
Normal retained revocation then publishes before native credential cleanup. Ordinary native
User writers cannot modify recovery metadata; replacement/deletion preserve audited accounts,
while normal profile updates remain available. The public module capability, Go SDK and CLI
remain inspection-only. This adds no unauthenticated maintenance endpoint or live action.

Added `credential-recovery.native.cjs` to the disposable native MongoDB runner. It starts an
actual Node/native credential writer, holds it after the real MongoDB session insert, verifies
publication remains blocked, kills only its owned process and awaits authoritative SIGKILL
exit. The durable claim survives. Test stale digest, missing offline confirmation, foreign
source/subject and malformed requests; inject a real CAS race and a committed lost reply.
Recovery retries inspect the single audit receipt. Restart a second actual writer process
with a new generation, kill it at the same boundary, verify the old receipt cannot clear
its new claim, and perform a fresh acknowledged recovery. Corrupt/full history refuses.
The fixture verifies immutable metadata, permitted profile writes, retained credentials and
conversation content, then normal revocation cleans both sessions and retains both receipts.

The initial fixture failed before reaching the crash boundary because this independent native
session signer lacked its disposable JWT refresh key. Added a process-local random fixture
key; no production configuration changed. Assertion diagnostics print source locations only,
since native assertion values can contain credential material. All scratch/resources are owned,
disk-backed and unexposed; Node/MongoDB/identity memory/CPU/PID limits remain in force.

Verification: `librechat-offline-recovery-native.log` terminal exit 0 covers native CLI/SDK
nonmutation, all nine source syntax checks, retained-record/writer/race/unknown outcomes,
signed JWT/cookie/image/API-key/remote OIDC/M2M paths, killed/restarted writer recovery and
actual KyIdentity HTTPS/PKCE browser registration/administrator role projection.
`librechat-offline-recovery-race.log` terminal exit 0 covers the affected Go package with the
pinned source probe. `git diff --check`, clean upstream tracked source and owned fixture
cleanup pass. Heavy work stayed serial.

DOX: updated module ownership/contracts/verification, parent LibreChat verification and its
existing child index, module README and catalog proof/limits. Root and adapters parent remain
unchanged because the existing boundaries, deployment authority and indexes still apply.
Full all-product goal remains active; LibreChat admission remains gated.

Next: expose confirmed-offline maintenance only through product-owned authenticated/protected
transport and qualify actual replica-stop/restore handling. Complete native credential query
upsert/rotation/batch and deletion-controller paths, new-account/source races, privileged raw
writer audit, alternate browser/provider reuse and full local/refresh authorization, cleanup
completion audit/native relay. Product-owned initial setup/ownership, public revoke SDK/resident
integration and administrator capability/purpose remain required. Continue every suite product
and admitted catalog adapter through lifecycle, backups, provenance and guided acceptance.
Preserve existing data/services; Vaultwarden only and older KeePass KyVault untouched.


### LibreChat native credential lifecycle and exact owner writes — 2026-10-10

Previous goal turn made concrete progress: implemented and native-qualified confirmed-offline
crash/restart recovery. This turn fixes credential document/query ownership in the shared
`credentials.cjs` model guard and adds `credential-lifecycle.native.cjs` to the real MongoDB
runner. Existing document saves reject modified owner fields and condition the actual write
on the current owner. Query writes schema-cast the filter before raw collection inspection,
then pin the inspected credential ID and owner. Native APIs pass string IDs; an uncast raw
probe can miss a stored ObjectId and classify an existing row as an initial upsert. Probe the
same typed filter the actual write uses. A non-upsert no-match remains a no-op when a new
row appears afterward. A first upsert requires the admitted owner and avoids duplicate owner
equality paths (MongoDB must be able to infer the inserted document). An inspected existing
row has upsert disabled: concurrent native logout/deletion cannot resurrect that credential.
Multi-row and batch writes remain refused without mutation.

Actual native method qualification covers provider-session insertion/repeated upsert,
refresh-token rotation (old token lookup fails/new lookup succeeds), expiry changes and
API-key validation/last-used updates. Test save/query owner rebind/removal/rename refusal,
insertMany/bulk refusal, multi-row refusal and zero leaked admissions. Inject actual MongoDB
late-match broad-update and no-match races; a foreign-owner row stays byte-for-byte unchanged.
A broad first upsert cannot adopt the late foreign-owner row. Inject a privileged raw owner
change at an admitted save and verify its conditional write does not redirect; only the
fixture restores its own injected row. Run native logout between inspected existing-session
upsert and driver execution: verify deletedCount, driver upsert=false, native method refusal
and absence of a recreated session. An explicit new native upsert can subsequently create
its own session normally. While session/key rows remain, publish revocation with cleanup
paused and prove provider upsert/repeat/new, refresh rotation/expiry and API-key last-use
writes deny without native credential mutation. Control sessions keep working; normal
repeated revocation then cleans only the recorded account.

The first new native upsert failed because the added query shape duplicated owner equality.
Corrected that while retaining owner pinning. The added logout-race fixture exposed the
uncast string-ID ownership probe. Its initial assertion inside an intercepted driver call
could itself induce an expected rejection; moved those assertions outside the interception
and checked driver options and actual database state independently, then fixed the raw probe
by schema-casting. The final proof requires the real write to run, not a fixture-thrown
assertion. Native fixture diagnostics expose source locations only, not credential values.

Verification: `librechat-credential-query-cast.log` terminal exit 0 covers all nine native
source syntax checks, native CLI/SDK nonmutation, retained publication/writer/unknown replies,
actual killed/restarted writer recovery, new credential lifecycle/races, JWT/cookie/image/
API-key/remote OIDC/M2M and actual KyIdentity HTTPS/PKCE browser registration/admin role.
`librechat-credential-lifecycle-race.log` terminal exit 0 covers the affected Go package and
pinned source prerequisite probe. `git diff --check`, unchanged tracked upstream source and
owned fixture cleanup pass. Heavy work remained serial under existing resource bounds.

DOX updates module ownership/contracts/verification, LibreChat parent verification, README
and catalog proof/limits. Root, adapters parent and existing Child DOX indexes stay current:
no new ownership boundary, public command, deployment authority or capability is introduced.
LibreChat admission stays gated and the complete all-product goal remains active.

Next: full native local/provider refresh controller behavior, deletion-controller/source
races, privileged raw maintenance and restore, authenticated protected offline recovery
transport/replica-stop orchestration, cleanup completion audit/native relay and product-owned
initial setup/ownership. Complete public revoke SDK/resident integration and administrator
capability/purpose only after their full acceptance. Continue every supported Ky product and
admitted third-party adapter through lifecycle, backup, provenance and guided deployment gates;
these native method proofs do not replace that scope. Preserve existing services/data,
Vaultwarden only and older KeePass KyVault untouched.


### LibreChat actual native local-session refresh controller — 2026-10-10

Previous turn made implementation progress on exact-owner credential writes and native
query/lifecycle races. Rechecked current installer product registry: twelve Ky server
interfaces plus Immich are registered; LibreChat still lacks complete product-owned setup
and resident acceptance. No registry/capability or admission gate is enabled by this turn.

Added `module/refresh-controller.native.cjs` to the real native MongoDB runner. It executes
the pinned native `AuthController.refreshController` over an owned Express HTTP listener,
using actual native models/methods, signed tokens, cookie handling and the patched native
AuthService. The shared native fixture loader can read unpatched clean pinned modules when
no overlay exists, binds AuthService to the actual patched source, and refuses unused
Graph/provider-refresh/bridge/flight boundaries rather than fabricating responses. This
qualifies the local-session branch used with provider-token reuse disabled; it does not
qualify the provider branch or full server routing.

Actual refresh accepts a signed session and returns a cryptographically verified access
token and refresh cookies before publication. Invalid signatures cause no native writes.
After retained revocation is published with cleanup paused, the current valid refresh
cookie still has a stored native session, yet controller requests (including retry) give
403 with no cookies or User/session mutation. Normal cleanup then removes the account's
sessions and refresh remains denied. A control administrator continues to refresh through
all stages; this is not dedicated administrator ordinary-workflow separation.

Two fixture corrections were necessary. System-context issuance made unscoped sessions
invisible to the explicitly scoped HTTP tenant context; issue with actual native AuthService
under the same ordinary tenant context instead. Requests are never run as system. Also,
refresh normally rotates its cookie: follow every returned cookie and explicitly verify
that the current daily cookie still resolves to a native session before the publication
check. Replaying an old rotated cookie would be weak denial evidence. Tokens remain in
fixture memory and assertion diagnostics expose only source locations. No production
configuration or existing service/data was changed.

Verification: `librechat-refresh-controller-cookie.log` terminal exit 0 covers this actual
native controller proof plus all earlier native source syntax, CLI/SDK nonmutation, retained
revocation/writer/unknown outcomes, killed/restarted writer recovery, credential lifecycle/
query races, JWT/cookie/image/API-key/remote OIDC/M2M and actual KyIdentity HTTPS/PKCE browser
qualification. `librechat-refresh-controller-race.log` terminal exit 0 covers the affected
Go package with pinned-source prerequisite checks. `git diff --check`, unchanged tracked
upstream source and owned fixture cleanup pass. All heavy execution remains serial/bounded.

DOX updated module ownership/verification, LibreChat parent verification, module README and
catalog proof/limits. Root, adapters parent and Child DOX indexes remain unchanged because
existing ownership boundaries and deployment authority still apply. Full all-product goal
remains active, and LibreChat admission stays gated.

Next: product-owned initial setup/ownership and actual administrator capability/purpose,
provider refresh/reuse/bridges and remaining local authentication/controller flows, native
delete/source races, raw maintenance and restore, protected authenticated offline recovery
transport/replica-stop handling, cleanup completion audit/relay and public revoke SDK/resident
integration. Continue the complete supported Ky suite and admitted catalog scope through
identity lifecycle, backups, provenance and guided acceptance. Vaultwarden only; older
KeePass KyVault, existing data and live services remain untouched.


### KyNotes installer administrator purpose and ordinary API denial — 2026-10-10

Previous goal turn made verified native LibreChat controller progress. This turn returned to
the supported Ky suite source adapters and closed part of KyNotes' installer owner-access
separation gate in `/home/yoshi/git/busnes.app/KyNotes-installer-setup` (isolated existing
product worktree). Re-read workspace/product DOX before edits. Preserve all prior uncommitted
product/installer work; no live service, deployment, PR or release changed.

Migration `0021_administrator_purpose.sql` adds `administrator_accounts(user_id,created_at)`.
Only new installer-created administrators are marked: identity, purpose and creation audit
commit atomically in the existing native transaction. Setup/check refuse an existing unmarked
mixed-use identity, including an already privileged account, with a migration conflict rather
than silently converting it, resetting roles or revoking credentials. A previously dedicated
account can still use the existing explicit grant/revocation path. Legacy accounts and content
are otherwise unchanged. The native table is additive; DESIGN and IMPLEMENTATION_PLAN record
the frozen-schema/session-response amendment.

Shared native `OrdinaryAccess` reads account purpose independently of session role. Container,
object, collaboration, share-management, upload, device, sync and push routes now use scoped
ordinary-session/either/device guards. Admin/recovery and authentication routes retain their
existing authorization. A user-scoped SSO token cannot make a marked administrator an ordinary
notes user. Session inspection adds `user.administratorOnly` for the browser's future routing.
No claim alone can create that purpose or overwrite it. Public anonymous share capabilities
and existing confidentiality contracts are unchanged.

New native router tests use actual signed OIDC/JWKS/PKCE callback flows from the product fixture:
owner admin APIs work, ordinary routes return 403, a user-scoped owner token cannot use notes
or gain admin authority, valid retained device credentials cannot use ordinary APIs, and an
ordinary account can use notes but cannot administer. Existing mixed-use setup refusal preserves
roles, session revocation state and audit/marker counts. Existing grant/callback-fence tests now
construct previously dedicated accounts rather than expecting conversion of mixed-use identities.
The sealed capsule snapshot/open/drill test additionally verifies the purpose row survives
native database restoration; no shared recovery library code is copied or changed.

The first whole suite had one fixture-only failure: `t.TempDir` embedded a long test name in
the disk-backed TMPDIR and exceeded Unix socket path limits. Changed only that owned socket
fixture to a short `os.MkdirTemp` basename with cleanup; it still uses the configured disk
scratch and exercises the same privacy/stale-socket behavior.

Verification: `kynotes-purpose-focused.log` terminal exit 0 covers new signed purpose/API
checks and grant/callback tests. `kynotes-purpose-final-race.log` terminal exit 0 covers the
whole product `go test -race -parallel 1 ./...`, including sealed purpose restoration and
socket lifecycle. `kynotes-purpose-vet.log` terminal exit 0 covers `go vet ./...`; `gofmt -l .`
is empty and `git diff --check` passes. Existing resource limits/disk scratch and serial heavy
execution remain in effect. Installer registry/capability contracts are unchanged, so no
new deployability or complete-separation claim is made.

DOX updates product root, DESIGN, binding IMPLEMENTATION_PLAN, installer runbook and backup
child's purpose-retention contract. Workspace, KyQuickStart root and Child DOX indexes remain
unchanged because ownership boundaries and public installer commands still apply. The full
all-product goal remains active.

Next concrete KyNotes work: browser administrative landing using trusted administratorOnly,
explicit content-preserving mixed-use migration and purpose qualification for other local/
UI/directory administrator creation/grant paths. Then compose actual KyIdentity/transport
acceptance and preserve purpose through all restore paths. Continue the full supported server
and admitted external-app adapter scope, including Vaultwarden native administration,
LibreChat setup/lifecycle gates, suite enrollment/backups/provenance and guided deployment.
Vaultwarden only; older KeePass KyVault and every live service/data remain untouched.


## KyNotes dedicated administrator browser qualification (2026-10-10)

Progress, verified and uncommitted in `/home/yoshi/git/busnes.app/KyNotes-installer-setup`
(branch `feat/installer-adapter`). The preceding memory-recovery turn only revalidated the
checkpoint; this turn changed authoritative browser code and completed native rendered checks.
The full all-product objective remains active, with the broader requirements still open.

`web/src/api.ts` parses the session response from unknown and requires a boolean
`user.administratorOnly`. Missing/malformed purpose refuses browser entry rather than assuming
ordinary access. Login and first-run setup fetch that authoritative session before passing
identity to App; ordinary login stores a personal key only after the session check. App checks
purpose before reading cached personal keys, mounting Workspace or opening SharedNote.
Dedicated owners mount existing SettingsView with an explicit null personal key; a user-scoped
owner token shows the administrator-assignment error, without ordinary notes/password controls.
Sign out clears the administrative screen only after native logout succeeds.

Keyless team management lists IDs, creates empty opaque metadata through the native admin
endpoint, and adds members without decrypting names or patching ordinary containers. Naming
remains a workspace operation; no keyless naming/decryption capability is claimed. Legacy
mixed-use administrators keep their existing key-backed UI until explicit migration.

`web/src/session.test.ts` covers dedicated purpose independently of token role, legacy ordinary
purpose, malformed/missing flags and malformed identity/session fields. The production bundle
was regenerated and synchronized in full to `internal/web/dist`; replaced asset hashes are
intentional. Added `internal/httpapi/administrator_browser_test.go`, an opt-in test-only
fixture: KYNOTES_BROWSER_FIXTURE names an owned directory, url is the disposable loopback
endpoint, stop closes it, and ten minutes is the maximum. It uses real RSA-signed OIDC/PKCE
callbacks, native SQLite/router/blob store/backup service and the embedded production bundle.
Its session bootstrap and request-recording routes are absent from the product binary.

T3 native collaborative browser verified the dedicated owner at 1280x800 and 390x844:
Administration visible, no workspace navigation/master notes password, no personal IndexedDB
on initial entry, and request recording contained no ordinary notes/sync/notification/device/
share routes. Native keyless Create team succeeded with no ciphertext or ordinary container
update. After seeding a disposable retained personal key, `/share/disposable#local-capability`
still mounted Administration without ordinary requests. Signed user-scoped owner showed the
assignment error; logout made the session endpoint return 401. The everyday identity with its
own disposable cached key mounted Workspace and its admin users request returned 403. Mobile
document width was 375px at a 390px viewport. Product UI-VERIFICATION and two local screenshots
record the checks. Browser login against a real KyIdentity deployment, all admin actions,
mixed-use migration and other creation/grant paths are not qualified by this fixture.

Verification under disk scratch and bounded serial builders:
- `kynotes-admin-browser-build.log`: terminal 0; npm test maxWorkers=1, 144 tests/14 files,
  TypeScript and production build passed. Node container memory1536MiB, CPU1, heap1024MiB.
- `kynotes-admin-browser-native.log`: terminal 0 after the owned stop file, real browser fixture.
- `kynotes-admin-browser-race.log`: terminal 0, HTTP API race suite (78.205s) and embedded web
  race checks (1.027s); GOMAXPROCS2, GOMEMLIMIT1GiB, GOFLAGS-p=1, disk TMPDIR.
- `kynotes-admin-browser-vet.log`: terminal 0; changed Go packages vet clean.
- gofmt fixture, git diff --check and complete web/dist versus internal/web/dist parity passed.
No live services, existing data, old KeePass KyVault, commits or PRs were changed.

DOX: KyNotes root, DESIGN and frozen-contract amendment in IMPLEMENTATION_PLAN, installer
runbook and UI-VERIFICATION updated. Parent workspace/KyQuickStart ownership and child indexes
remain unchanged: browser and private native router are still product-root-owned; the installer
registry and native bundle/transport contracts did not change. Full ledger mirrored exactly
to myslop before closeout.

Next concrete work: qualify every other KyNotes administrator creation/grant path (local CLI,
bootstrap, UI and directory) without silently converting existing mixed-use accounts, and
build the explicit migration preserving content and verified owner access. Continue actual
KyIdentity/transport acceptance, restoration paths, all supported server adapters, Vaultwarden
native administration, gated external-app setup/lifecycle, suite enrollment, backup/bulk restore,
provenance and guided full-stack deployment. These remain implementation/acceptance gates;
this browser milestone does not mark the installer or all-product request complete.


## KyNotes native administrator management purpose (2026-10-10)

Progress, verified and uncommitted in `/home/yoshi/git/busnes.app/KyNotes-installer-setup`
(branch `feat/installer-adapter`). The previous browser turn was progress; this continuation
changed native account/grant authority and completed new real API/signed-SSO evidence.

`internal/httpapi/admin_routes.go`: POST admin/users retains CSRF/step-up and now creates
account, administrator purpose (when role=admin) and attributed creation audit in one SQLite
transaction. Duplicate usernames remain 409; purpose/audit persistence failures return 500
and roll back account creation. ID mint errors are handled. PATCH admin/users reads current
role/status/issuer/subject/purpose in its transaction. New admin grants to unmarked ordinary
accounts return 409 `administrator_migration_required`, with no account, quota/status, content,
credential or audit changes. Existing legacy administrator grants remain intact for explicit
migration. Dedicated purpose is monotonic through demotion/re-grant. Changed role/status
revokes sessions/devices and fences linked OIDC callbacks, with the user update and attributed
audit committed together. Missing users return 404 rather than a false successful update.

`revokeForRoleChange` in sso_directory.go now creates an OIDC cutoff only for an actual
issuer/subject binding. Local-only accounts still revoke credentials on role/status changes,
but do not create an empty shared OIDC identity. This helper change does not qualify the
unchanged directory account creation/grant purpose path.

`administrator_management_test.go` exercises native password sign-in after real admin/user
creation: dedicated admin API 200, ordinary containers 403; everyday containers 200, admin
403. Refused ordinary promotion compares content ciphertext, ownership, quota/status, sessions,
audits and markers before/after; no mutations and ordinary access is retained. Legacy admin
quota edits preserve its absence of purpose metadata. Dedicated demotion revokes native session
and device rows, a fresh user-scoped local session still cannot use notes/admin, re-grant revokes
that session and fresh login administers with no notes access. SQL-triggered purpose/create-audit/
update-audit failures prove transaction rollback and credential preservation, rather than success
with a lost security record. A real RSA-signed OIDC/PKCE callback pending before native PATCH
grant is rejected without creating a session; a fresh signed app-admin login receives native
administrator access. Existing local step-up, directory role and installer grant tests passed.

Verification, disk scratch and bounded Go settings throughout:
- `kynotes-admin-management-focused.log`: terminal 0, new management and credential step-up,
  app-role/directory/installer grant race checks (14.978s).
- `kynotes-admin-management-race.log`: terminal 0, whole `go test -race -parallel 1 ./...`;
  HTTP API package 81.717s, app/server/backup packages passed, unchanged packages may be cached.
- `kynotes-admin-management-vet.log`: terminal 0, whole `go vet ./...`.
- gofmt-l empty and git diff --check passed. No frontend changes in this continuation, so
  previous rendered/browser/build evidence remains scoped to the preceding browser milestone.
No live deployments, existing data, commits, PRs or old KeePass KyVault were changed.

DOX updates KyNotes root, DESIGN, frozen-contract IMPLEMENTATION_PLAN amendment, installer
runbook and SSO operational grant contract. Workspace/KyQuickStart ownership and Child DOX
indexes intentionally remain unchanged because all paths remain in the existing product HTTP
API boundary; no installer bundle, registry or CLI command changed. Full ledger mirrored exactly
to myslop before closeout.

Next concrete work: native directory creation/grant purpose (new directory admins need durable
purpose, ordinary-account promotion must not silently convert mixed-use identities), then
bootstrap/first-run/CLI creation and an explicit mixed-use migration preserving ciphertext and
verified owner access. Preserve existing signed offboarding/replay/audit and last-admin behavior
while qualifying those paths. The entire supported-server/admitted-external-app scope remains
active, including actual KyIdentity transport acceptance, Vaultwarden native administration,
gated external-app setup/lifecycle, suite enrollment/backups/bulk restores/provenance and guided
full-stack acceptance. The admin API milestone is not complete product/suite separation or a
claim that all adapters are ready to deploy.


## KyNotes signed directory administrator purpose (2026-10-10)

Progress, verified and uncommitted in `/home/yoshi/git/busnes.app/KyNotes-installer-setup`
(branch `feat/installer-adapter`). The preceding native-management turn was progress; this
continuation changed native signed provisioning authority and completed lifecycle/rollback proof.
The entire all-product goal remains active.

`internal/httpapi/sso_directory.go`: newly provisioned active app administrators receive
administrator_accounts purpose in the same transaction as their account, durable event/version
admission and directory.apply audit. Failed entropy generation returns failure rather than using
an assumed dummy verifier. An active promotion of an existing ordinary (unmarked, non-admin)
account fails with the shared administrator-migration policy error. The receiver returns 422
`administrator_migration_required`, never create/409 (the sender acknowledges that as success).
All account/data/binding/credential/purpose/audit and event/version admission mutations roll back;
corrected same-ID/version delivery can apply. Dedicated identities retain purpose through role
loss, offboarding and reactivation/re-grant. Existing legacy administrator grants and last-active-
admin retention are preserved for explicit content-preserving migration. Missing/malformed roles
never block signed deactivation. The native admin API uses the same unexported policy error; its
existing 409 refusal and prior atomic creation/update behavior remain intact.

`administrator_directory_test.go` uses the actual syncauth-signed receiver and real RSA-signed
OIDC/PKCE sessions. It proves new directory owner admin API 200/notes 403, limited owner 403/403,
and everyday 403/200. Identical directory retry creates no second audit. Dedicated demotion,
re-grant, disable and reactivation preserve purpose and native authorization. Disable revokes a
session proved live immediately before delivery. Refused grants for auto-provisioned linked and
unbound local ordinary accounts compare username/role/status, binding, quota, ciphertext and
ownership, sessions/devices, audit, purpose, replay/version and cutoff state before/after; unchanged
and ordinary access stays usable. Corrected same-ID/version events apply after refusal. Triggered
purpose/audit write failures return 500, roll back account/purpose/event/version/audit, and the
exact original event applies after fault removal.

Existing app-role ceiling and pending-directory-promotion tests now seed an already-dedicated
identity through apply-setup before preparing the user-scoped/re-grant condition. They no longer
expect an ordinary account to gain a new admin grant. The first focused run caught an obsolete
notes-device pairing expectation for the dedicated owner (correct native 403). The test now
seeds a valid retained device hash, verifies failed directory audit preserves that credential,
and still proves successful role loss revokes it. Real pairing remains denied; coverage was not
weakened to accept ordinary workflows.

Verification under existing bounded Go settings and disk TMPDIR:
- `kynotes-directory-purpose-focused.log`: terminal 0 after correcting the obsolete fixture;
  directory/purpose/lifecycle, app-role and native-management race checks passed (17.085s).
- `kynotes-directory-purpose-race.log`: terminal 0, whole `go test -race -parallel 1 ./...`;
  HTTP API 91.468s, unchanged packages may reuse valid cache.
- `kynotes-directory-purpose-vet.log`: terminal 0, whole `go vet ./...`.
- gofmt-l empty and git diff --check passed. No frontend/bundle edits or additional rendered
  claims in this continuation. No live services/data, commits, PRs or KeePass KyVault changed.

DOX updates product root, DESIGN, frozen-contract IMPLEMENTATION_PLAN amendment, installer
runbook and SSO receiver/operating contract. Provision a directory administrator before its
first ordinary auto-provisioned login, or use installer apply-setup; a prior ordinary identity
requires migration before a new grant. Workspace/KyQuickStart ownership and Child DOX indexes
remain unchanged because existing product HTTP API ownership and installer contracts still apply.
Full ledger mirrored exactly to myslop before closeout.

Next concrete KyNotes work: dedicated purpose for bootstrap, web first-run and CLI user --admin
creation, then an explicit mixed-use migration workflow that preserves ciphertext/ordinary
identity access and verifies a separate dedicated owner before removing the legacy grant.
Keep existing admin/auth/recovery and sealed restoration semantics intact. Continue actual
KyIdentity transport acceptance, all supported server/admitted external-app adapters, Vaultwarden
native administration, gated external-app setup/lifecycle, suite enrollment, backups/bulk restores,
provenance and guided full-stack acceptance. Native directory qualification is a milestone;
bootstrap/CLI/migration and suite-level readiness remain unproven.


## KyNotes bootstrap, first-run and CLI administrator purpose — 2026-10-10

Recovered the memory-killed session from its checkpoint. Continued in
`/home/yoshi/git/busnes.app/KyNotes-installer-setup` (`feat/installer-adapter`), preserving
all existing uncommitted adapter work. Goal stays active and very unfinished. No live
services/data, commits, PRs or older KeePass KyVault installations were changed.

Bootstrap, native web first-run setup and CLI `user add --admin` now commit account,
dedicated administrator purpose and creation audit atomically. First-run setup rechecks
empty-account state inside the transaction; concurrent real requests create exactly one
administrator. Bootstrap rerun leaves the account/hash/audit untouched. CLI ordinary users
stay ordinary; recovery-code output happens only after commit. Security-purpose or audit
write failure rolls back account/purpose/audit, and CLI emits no false recovery handover.
The CLI delegates to an input/output-injected implementation for real native qualification,
without global stream mutation. First-run internal database errors use an opaque response.

Browser setup and password login cache personal keys only for ordinary accounts. Corrected
misleading first-run encryption-password wording: the screen explicitly creates a dedicated
administration/recovery account and directs notes to a separate everyday identity. Generic
sign-in explains both purposes; authenticated everyday key-unlock copy stays intact. Full
production web output was regenerated and copied to the embedded directory, never hand-edited.

Verification (bounded serial Go/Node settings, disk TMPDIR):
- `kynotes-initial-purpose-focused.log`: race checks for bootstrap/setup/CLI passed,
  httpapi 5.606s, app 8.752s, command 3.152s.
- `kynotes-initial-purpose-web-final.log`: all 144 frontend tests in 14 files, TypeScript
  and production build passed. Node container hard 1536MiB/no swap, heap1024, CPU1, one worker.
- `kynotes-initial-purpose-race.log`: terminal 0, whole race suite, HTTP API 82.348s.
- `kynotes-initial-purpose-vet.log`: terminal 0, whole vet. gofmt-l empty, diff check clean,
  byte-for-byte generated/embedded bundle parity across 16 files.
- Real T3 browser + native embedded bundle + empty SQLite/router/backup fixture:
  first setup opens Administration, role admin/administratorOnly true, no IndexedDB personal
  key and no ordinary notes/device/sync/share requests. Admin users API200, containers403.
  Desktop1280x800 and mobile390x844 screenshots saved; mobile document375px, no overflow.
  Sign-out shows purpose-aware generic login; returning native password login opens
  Administration with the same dedicated purpose, no personal key and no ordinary requests.
  The first returning-login probe ran before async login completed; reran in a fresh owned
  fixture and verified the completed session. Both disposable fixtures stopped, terminal0.
  Logs `kynotes-initial-purpose-native-browser-final.log` and
  `kynotes-initial-purpose-returning-browser.log`; screenshots
  `docs/first-run-admin-{desktop,mobile}.png`, evidence in UI-VERIFICATION.md.

DOX updated product root AGENTS, DESIGN, frozen IMPLEMENTATION_PLAN amendment, INSTALLER,
SSO and UI-VERIFICATION. Parent ownership/Child DOX indexes intentionally unchanged: no
new durable directory boundary or installer deployment contract. Browser fixture now
supports `KYNOTES_BROWSER_SCENARIO=first-run`; no fixture routes enter the product binary.
All work remains isolated and uncommitted.

Next concrete KyNotes work is explicit content-preserving mixed-use administrator migration:
verify a separate dedicated owner before removing a legacy grant, preserve ordinary identity,
ciphertext and access, qualify rollback/replay/session revocation and restoration. This milestone
does not prove the full suite. Continue actual KyIdentity transport/composition, target-held
bundles and roles, every supported server and admitted app, Vaultwarden native administrator
extension (Vaultwarden only), external-app lifecycle/admission, enrollment, backups/bulk restore,
source/build provenance and guided full-stack acceptance. Keep serial memory-limited builds and
read this ledger in bounded slices. Myslop folder stays claimed by basalt; full exact ledger
mirror is required before closeout.


## KyNotes verified-owner mixed-use migration API — 2026-10-10

Previous goal turn was verified progress (bootstrap/first-run/CLI purpose, full race/vet,
rendered browser qualification and exact mirror2731). Rechecked shared claim: basalt, no
newer posts. Continued in `/home/yoshi/git/busnes.app/KyNotes-installer-setup`, branch
`feat/installer-adapter`, preserving all prior uncommitted work. Full goal remains active.
No live services/data, commits, PRs, or KeePass KyVault changed.

New native `POST /api/v1/admin/users/{id}/migrate-administrator` accepts only bounded strict
JSON `{"confirmUserId":"<source-id>"}`, with CSRF and existing fresh local/action-bound OIDC
step-up. The signed-in actor must be a separate dedicated active administrator; the transaction
rechecks its current live session and verified app-admin ceiling. The body cannot nominate an
unverified replacement. Only an active unmarked legacy administrator is migrated. It stays the
same ordinary content-bearing identity: preserve ID, credentials, salts, quota, ownership,
memberships, ciphertext metadata, object versions and key envelopes. Remove only its local admin
grant, revoke sessions/devices, fence linked pending callbacks and commit receipt + attributed
audit together. `0022_administrator_migrations.sql` stores source/verified-owner/time, no secrets.
Retries return present without duplicate audit or further revocation. Dedicated accounts cannot
become ordinary through this route. Active legacy demotion via generic PATCH now returns
administrator_migration_required; ordinary promotion remains refused. Offboarding still works.

Actual verification (bounded serial Go and disk TMPDIR):
- Initial new-test compile failed for an unused auth import; removed it and corrected the test's
  CSRF-cookie name to native csrf_token before qualification. No weakened production guard.
- `kynotes-migration-focused.log`: terminal0, native migration/management race tests HTTP19.046s,
  sealed capsule roundtrip2.477s. Refuses missing step-up, legacy/self replacement, wrong/trailing
  confirmation, CSRF failure, dedicated/ordinary/inactive/missing source, inactive/revoked owner.
  Native login/admin/notes APIs prove old credentials still work for notes, old sessions/devices
  are revoked, new dedicated owner retains admin and cannot use notes. Concurrent requests
  produce one migrated/one present. Purpose/receipt/audit/revocation/callback-fence injected
  failures roll back; same request applies after fault removal. Seeded content/key metadata
  stays byte-for-byte unchanged. This is not a full browser decrypt/edit qualification.
- `kynotes-migration-race.log`: terminal0 whole suite, HTTP100.440s, production changes all included.
- Added real RSA/OIDC/PKCE owner step-up qualification after that whole run. Existing test helper
  reauthStart now delegates to reauthStartAction(path,body). `kynotes-migration-signed.log`:
  terminal0 race migration + all SSOStepUp tests, HTTP37.485s. User-scoped owner token has no
  challenge/admin authority; modified action body rejected, exact body succeeds, proof replay
  refused. Fresh signed legacy app-admin claim cannot restore the removed local grant; native
  admin403/ordinary200. Local tests are not a live KyIdentity sender transport qualification.
- `kynotes-migration-vet.log`: terminal0 whole vet. gofmt-l empty; diff check passed.
- Sealed snapshot/open/native Checks retains migration receipt, ordinary account role/credentials
  and dedicated owner purpose. Backup child DOX updated. No frontend/generated bundle changes
  or new rendered claims; prior browser qualification remains as recorded.

DOX amended product root, DESIGN and frozen IMPLEMENTATION_PLAN together, INSTALLER native
handover runbook, SSO and backup child. Parent ownership/Child DOX indexes intentionally unchanged:
no new durable ownership boundary or installer deployment contract. Full ledger exact mirror
required before closeout. Work remains isolated/uncommitted.

Next concrete KyNotes work: guide this handover in the browser/installer and qualify real encrypted
notes decrypt/edit before and after migration. Coordinate upstream KyIdentity assignment removal
and signed directory events without bypassing verified-owner handover. Current runbook migrates
natively first, then removes old app-admin assignment; directory-transition races/retries remain
an explicit acceptance gate. Do not claim the API alone completes migration or suite readiness.
Continue actual KyIdentity composition, target-held bundles/client/role wiring, all supported
server/admitted external-app adapters, Vaultwarden native administrator extension (only password
selection), full lifecycle/enrollment, backups/bulk restores, source/build provenance and guided
full-stack acceptance. Keep serial memory-limited execution and read the ledger in bounded slices.


## KyNotes guided administrator handover and encrypted-note continuity — 2026-10-10

Previous turn made verified progress: native verified-owner migration/receipt/audit, rollback,
concurrency, signed owner/action proof and sealed snapshot qualification, exact mirror2732.
Shared claim rechecked as basalt with no newer posts. Continued isolated/uncommitted in
`/home/yoshi/git/busnes.app/KyNotes-installer-setup` (`feat/installer-adapter`). Full all-product
adapter goal remains active, not complete. No live services/data, commits, PRs or old KyVault
changed. Builds/fixtures remained serial and memory-limited with disk scratch.

Added guided browser handover under Administration > Users. Legacy administrators get instructions
for separate dedicated owner creation/login. A dedicated owner selects only active unmarked
mixed-use accounts, confirms their username exactly and uses existing local/action-bound SSO
step-up. The screen names preserved notes/credentials and client sign-out, reports completion,
refreshes users/audit, and instructs old-account notes/admin verification and upstream KyIdentity
assignment removal. Failed refresh after a committed handover is reported as completed with
refresh instructions, not a false failed migration. Submission is disabled while busy or before
exact confirmation. The screen offers handover only after authoritative account purposes load;
fetch/decoder failures display an error. No installer GUI was added: this is product-owned UI.

GET admin/users now returns explicit administratorOnly, independently of role, and reports SQL
iteration failures instead of returning a partial list. `parseAdminUsers` validates unknown data,
including purpose, roles/status, safe quota and IDs; missing/malformed purpose cannot make a
dedicated account eligible. Migration response validates status and exact source ID. Added
`AdministratorMigration.tsx`, account-list boundary tests and bounded responsive form styles;
existing themes/vendors preserved. Full generated bundle regenerated and embedded, no hand edits.

Actual native T3 browser qualification (production embedded app, SQLite, blob store, native
router/backup, disposable legacy local administrator only; no fixture crypto/API mocks):
- Legacy password login created encrypted notebook/page through the real UI, saved canvas text
  and title at version3, and created a new dedicated owner in Users. Native JavaScript notebook
  prompt needed a fixture response through preview evaluation; no note crypto was substituted.
  A broad textbox locator was ambiguous; corrected to native .bn-editor contenteditable locator.
- Dedicated owner logged in through the real form. Guide listed only old legacy admin; wrong
  username disabled submission. Missing step-up produced native denial. Password confirmation
  then allowed handover and refreshed source role/user list/audit, with completion instructions.
  Dedicated owner admin API200/notes403 and purpose remained true.
- Old account logged in again with same password/user ID; local role user and purposefalse,
  admin API403, no Admin button. Fresh load decrypted original notebook, title and text. Native
  ciphertext bytes exactly matched pre-handover version3 (552bytes); plaintext absent on server.
  It edited/saved version4 (567bytes), reloaded and decrypted the edited text/title. Native UI
  save/decrypt paths used actual browser crypto. This proves local continuity, not upstream
  identity orchestration or live deployment.
- Desktop1280x800 and mobile390x844 checked; document375px, actionbutton239px wrapping, no
  horizontal overflow. Screenshots `docs/administrator-handover-{desktop,mobile,notes}.png`.
- After adding initial account-loading guard, rebuilt final bundle and repeated legacy login,
  separate owner creation/login/step-up and guided migration in a fresh native fixture. Final
  completion/account refresh passed. Both fixture handles stopped with terminal0; no leftovers.

Verification:
- `kynotes-migration-guided-web.log` and `...-web-final.log`: terminal0,146 frontend tests/15files,
  TypeScript/build/vendor checks passed. Container hard1536MiB/no swap, CPU1/heap1024, one worker.
- `kynotes-migration-guided-race.log`: terminal0 whole Go race suite, HTTP95.693s. The final
  account-loading-only UI adjustment was rebuilt afterward and independently rendered as above.
- `kynotes-migration-guided-vet.log`: terminal0 whole vet after final embed sync.
- `kynotes-migration-guided-embed.log`: terminal0 final embedded-assets race check. Final 16-file
  generated/embedded byte parity passed, gofmt-l empty, git diff --check clean.
- `kynotes-migration-guided-browser.log` and `...-browser-final.log`: terminal0 native fixtures;
  evidence/capture limits recorded in UI-VERIFICATION.md.

DOX amended product root, DESIGN/frozen IMPLEMENTATION_PLAN, INSTALLER browser/API runbook and
SSO operating contract; UI-VERIFICATION records actual evidence. Existing ownership/Child DOX
indexes and unchanged backup child intentionally remain intact. No new directory boundary or
installer deployment contract. Exact full ledger mirror required before closeout.

Next concrete work: coordinate KyIdentity assignment removal/signed directory role transitions
with verified-owner migration, including events arriving before/after handover, rollback/retry,
offboarding and reactivation, without bypassing content-preserving ownership verification.
Installer remains CLI-only; its existing-product/setup handover orchestration still needs proof.
Continue actual KyIdentity suite composition, target-held bundles/client/role wiring, every
supported server/admitted app adapter, Vaultwarden native administrator extension (only password
choice), lifecycle/enrollment, backups/bulk restores, provenance and guided full-stack acceptance.
Native browser handover is a milestone, not all-product or production-readiness acceptance.


## KyNotes directory assignment/handover ordering — 2026-10-10

Previous goal turn made verified progress (guided handover, native encrypted-note continuity,
146 frontend tests, full race/vet, exact mirror2733). Shared claim rechecked: basalt, no newer
posts. Continued in `/home/yoshi/git/busnes.app/KyNotes-installer-setup`, branch
`feat/installer-adapter`. Full all-product objective remains active and very unfinished.
No live services/data, commits, PRs or old KeePass KyVault changed. Serial bounded Go/disk scratch.

Found and fixed a native directory bypass: active assignment loss could demote a content-bearing
legacy account without verified-owner handover when another administrator existed. Existing
lookup now reads durable purpose in the same transaction, reusing it for promotion and retention.
An active unmarked legacy admin retains its local grant until explicit migration, even with
another admin present; signed role loss still revokes sessions/devices and advances callback
fences. A fresh ordinary token cannot use the retained local grant. Audit marks admin_retained;
signed readback reports actual local role. Dedicated last-active-admin recovery retention remains
independent. Inactive events always disable/revoke; offboarding is never blocked by retention.

New `administrator_migration_directory_test.go` uses native HMAC-signed directory delivery,
RSA/OIDC/PKCE owner login and action-bound step-up, SQLite and product admin/ordinary routes.
Qualifies assignment loss before handover and handover before assignment loss; source identity,
credentials, owner membership and ciphertext metadata stay exact. An old session and pre-handover
pending callback are refused. Replayed ordinary events after migration cannot restore a grant.
Delayed new admin assignment fails422 without consuming version/event admission; corrected
same-ID/version ordinary delivery applies. Migrated source offboarding with malformed roles
succeeds/revokes; admin reactivation refuses, corrected ordinary reactivation succeeds and the
same notes remain accessible. Native signed readback proves retained admin before handover and
ordinary role afterward. Directory audit fault rolls back grant/revocation/fences/content and
replay/version admission; the same event succeeds after fault removal, revoking a genuinely
paired native device. Native proof is local fixture evidence, not actual KyIdentity sender transport.

Updated `TestDirectoryRetainsLastActiveAdminGrant` to provision a dedicated owner through native
apply-setup and signed admin login, so it specifically proves the dedicated last-admin recovery
exception instead of accidentally passing through the new legacy handover exception.
Initial focused runs caught fixture mistakes: callback cookies included the cleared SSO state
cookie, then an owned notebook lacked its normal owner membership. Corrected the fixtures to
retain only live cookies and seed owner membership; did not relax any production guard or
content/API assertion. Added signed readback and real paired-device rollback coverage afterward.

Verification, all terminal0:
- `kynotes-migration-directory-focused.log`: final corrected focused race suite9.074s.
- `kynotes-migration-directory-race.log`: whole Go race suite, HTTP109.071s, production changes
  all included; unchanged packages may use valid cache.
- `kynotes-migration-directory-focused-final.log`: race tests after adding readback/device
  coverage11.138s, including migration ordering, dedicated retention, deactivation, app-role
  ceilings and dedicated directory lifecycle.
- `kynotes-migration-directory-vet.log`: whole vet. gofmt-l empty, diff check clean.
No frontend/bundle or backup changes; prior rendered and sealed snapshot qualification stands.

DOX amended root product, DESIGN/frozen IMPLEMENTATION_PLAN, INSTALLER and SSO together. Runbook
still prefers dedicated-owner handover then upstream assignment removal, and now explains either
native ordering and audited retained grants. Parent/Child DOX indexes and backup child deliberately
unchanged: existing HTTP API ownership and backup contracts remain intact. Local uncommitted work
preserved; exact full ledger mirror required before closeout.

Next concrete work: actual KyIdentity sender transport into the product, plus CLI installer
existing-product/handover orchestration and complete suite acceptance. Located authoritative
sender candidate read-only at `/home/yoshi/git/busnes.app/KyIdentity-installer-setup`
(`feat/installer-setup`): `internal/sync/sync.go` sets `/api/sync/events`, uses syncauth.Sign and
handles delivery status; SCIM composition in `scim.go`; installer qualification tests in
`installer_setup_test.go`, `installer_multi_connector_test.go`, delivery/provisioning/reconcile
files. Read its full DOX/claim before edits; no changes made there in this continuation.
Continue target-held bundles/client/role wiring, every supported server/admitted external app,
Vaultwarden native administrator extension (only password choice), lifecycle/enrollment,
backups/bulk restores, provenance and guided full-stack acceptance. Handler ordering is a
milestone, not proof of upstream transport, all adapters or production readiness.

## Real KyIdentity-to-KyNotes directory transport — 2026-10-10

Recovered after the reported memory kill; recovery-only goal turn made no implementation
progress. Revalidated local changes and shared claim (basalt, no posts after2734), then
completed native sender transport qualification. Full all-product goal stays active and
unfinished. No live deployment, commit, PR or old KeePass KyVault changed. Heavy builds/tests
serial: GOMAXPROCS=2, GOMEMLIMIT=1GiB, GOFLAGS=-p=1, disk-backed TMPDIR.

New test pair in the existing isolated product worktrees:
- `/home/yoshi/git/busnes.app/KyIdentity-installer-setup` (`feat/installer-setup`):
  `internal/sync/notes_native_test.go`, opt-in `TestKyNotesNativeDirectorySender`.
  Native account setup, suite connector creation, app role/access hooks and worker delivery
  generate every payload/signature. No event/desired-state SQL inserts or fake wire bodies.
- `/home/yoshi/git/busnes.app/KyNotes-installer-setup` (`feat/installer-adapter`):
  `internal/httpapi/identity_native_test.go`, opt-in `TestKyIdentityNativeDirectoryDelivery`.
  Launches the precompiled sender and hosts the actual native TLS directory receiver.
  Native database/auth/API checks; protected temporary files each have one writer, exact
  certificate trust, refused redirects, bounded deadlines, sender cancellation/reaping.
  `docs/SSO.md` records serial invocation with `KYNOTES_TEST_IDENTITY_SYNC_BINARY` pointing
  at `/home/yoshi/.cache/kyquickstart-work/kyidentity-sync-native.test`.

Proven: only two assigned identities arrive; unassigned global admin excluded. Actual app
roles provision a dedicated owner and ordinary user. Receiver signed login proves owner
admin200/notes403 and everyday admin403/notes200; dedicated device pairing403. The ordinary
native notebook API creates ciphertext and owner membership. Assignment loss/reassignment,
disablement/reactivation and deletion preserve the same user ID/ciphertext/membership.
Inactive state revokes sessions, real device secrets and pending enrollment and refuses
fresh login. Active state restores ordinary notebook access without admin grants or
resurrecting old device secrets. Owner assignment loss disables even the last product admin,
revokes access and retains dedicated purpose. A recreated worker delivers reassignment;
admin access returns while notes stay forbidden, even with an ordinary-scoped proof.
Exactly nine native directory mutation audits; acknowledged source queues and active
connector status checked at every successful phase.

Evidence, all terminal0, under `/home/yoshi/.cache/kyquickstart-work/`:
- `kyidentity-notes-native-build.log`: race sender binary built serially.
- `kynotes-identity-native-focused.log`: focused real-sender proof4.646s before final
  device/pending-enrollment assertions.
- `kyidentity-notes-native-sync-race.log`: whole sender package81.861s; opt-in sender
  skips by default and ran through the receiver below.
- `kynotes-identity-native-http-race.log`: whole HTTP package108.258s with real sender
  enabled and final native device/pending-enrollment assertions.
- `kyidentity-notes-native-vet.log`, `kynotes-identity-native-vet.log`: affected vet.
  gofmt-l empty, both worktree diff checks clean. No frontend/generated assets changed.

DOX updated both product roots and KyNotes SSO/installer verification references. Parents,
Child DOX indexes, backup/mirror children and frozen design/plan deliberately unchanged:
existing ownership/production contracts remain; full sender/migration acceptance is open.
Existing changes preserved. Fixture RSA/PKCE issuer and protected pairing rendezvous do
not qualify actual KyIdentity OAuth, live pairing, setup-owned suite linkage, actual sender
delivery through mixed-use handover, sealed restore, full-stack or production acceptance.

Next concrete gap: KyIdentity `cmd/kyidentity/setup.go` / `internal/store/installer_scim.go`
support only receipt-owned generic SCIM. KyNotes needs a signed suite connector attached to
its OIDC app, with target-held credentials and explicit ownership. Build the native setup
contract with read-only/resume/conflict/audit checks, then installer wiring and combined
native OAuth/directory/setup plus mixed-use handover qualification. Continue every supported
server/admitted app, owner/everyday separation, lifecycle/enrollment, backups/bulk restore,
provenance and guided full-stack acceptance. Vaultwarden only; native KyIdentity admin
extension remains needed. Exact full ledger mirror required before closeout.

## Owned signed suite connector setup — 2026-10-10

Previous turn was verified progress: native KyIdentity-to-KyNotes transport, exact mirror2735.
Rechecked shared claim: basalt, no newer posts. Continued in the same isolated KyIdentity
`feat/installer-setup`, KyNotes `feat/installer-adapter` and KyQuickStart `feat/phase-4-edge`
worktrees. Full all-product goal remains active and unfinished. No live services, data,
commits, PRs or old KeePass KyVault changed. Builds/tests serial, disk-backed scratch,
GOMAXPROCS=2/GOMEMLIMIT=1GiB/GOFLAGS=-p=1; owned SSH/Identity/Notes containers now have explicit
memory, swap, CPU and PID limits. Successful fixture cleanup verified through Docker.

Implemented KyIdentity native bundle `clients[].suiteWebhook`:
`{"url":"https://notes.example.com/api/v1/sync/events","signingSecretFile":"/data/installer/notes.directory-secret"}`.
One SCIM or signed suite connector per client. Strict bounded parsing rejects conflicting
protocols and invalid HTTPS/query/fragment/credential URLs before credential reads. The
private owned target file holds 32–1024 visible ASCII bytes, optional final newline; short,
oversized, whitespace/NUL and shared files refuse with no credential output. Existing
product encryption key is reused; no generated replacement key or report secret.

`internal/store/installer_scim.go` now shares `configureInstallerConnector` for both
protocols; original SCIM ownership receipt bytes stay compatible. Signed type is
`suite_webhook`; receipt binds app identity, protocol, endpoint and secret digest. Owned
app/role/connector linkage, protected credential, initial desired-state work and native
creation/link audits commit together. Audit records the actual protocol. Check/resume
refuses foreign state, endpoint/secret/protocol/group drift, wrong keys or restored holds.
Omission preserves the connector; adding one later preserves the existing app/roles.
Suite connectors send no group resources. Generic SCIM/group behavior remains intact.

`cmd/kyidentity/setup.go` advertises `suite-webhook-connectors`; the installer requires it
before apply. Native CLI tests exercise both protocols against a real database, private
files and credential-free reports; store suite tests cover extension, readonly/resume,
encrypted binding, omission, nine refusal/rollback causes and the actual audit type.
The real sender fixture now uses setup-owned accounts/client/roles/connector on one OIDC
app, with present-only check/resume and native signing-key readback. Its nine real receiver
mutations still prove source scope, administrator/ordinary separation and revocation/data
retention. Fixture OIDC/credential rendezvous remains explicit, not full OAuth acceptance.

Closed another adapter version gap: KyNotes advertises `dedicated-admin`; `notesSetup`
requires it. Older mixed-use builds are refused before product setup despite matching
version-1 schema. Native capability test and installer nonmutation regression cover it.
Matching static product binaries were built, and the real SSH/Docker test uses both.
Identity fixture now creates/checks/resumes eleven receipts including SCIM/groups and signed
suite connector configuration. Notes fixture qualifies its native backup setup/capability
path; it does not configure sign-on or claim the complete combined stack is installed.

Evidence, all terminal0, under `/home/yoshi/.cache/kyquickstart-work/`:
- `kyidentity-suite-setup-focused.log`: store22.550s, CLI6.597s before additional malformed
  signing-file tests; final full suite below includes those additions.
- `kyidentity-suite-native-build.log`: final race sender binary; `kynotes-owned-suite-native.log`:
  real setup-owned sender to actual KyNotes TLS receiver5.219s.
- `kyidentity-suite-setup-build.log`, `kynotes-suite-setup-build.log`: static product binaries
  `kyidentity-suite-setup`, `kynotes-suite-setup` in scratch, no deployment.
- `kyq-suite-identity-ssh-e2e.log`: real Identity SSH create/check/resume10.455s.
- `kyidentity-suite-setup-race.log`: whole Go race suite; CLI14.145s, API315.141s,
  OAuth93.227s, store325.409s, sync66.977s, remaining packages green. Native external sender
  skips without its opt-in and was separately enabled through the receiver above.
- `kyidentity-suite-setup-vet.log`: whole vet.
- `kynotes-dedicated-capability-race.log`: native capability/check dispatch1.021s.
- `kyq-suite-connectors-ci.log`: `make ci`, tidy diff/gofmt/both vet modes/full race suite;
  CLI2.339s, offboard31.338s, all packages green.
- `kyq-suite-connectors-build.log`: both installer/resident binaries built.
- `kyq-suite-identity-notes-ssh-e2e.log`: final real SSH fixture with both current binaries
  and bounded containers11.119s; `kyq-suite-connectors-e2e-vet.log`: final tagged fixture vet.
  All three worktree diff checks and changed-path gofmt checks clean.

DOX updated product roots, KyIdentity setup runbook, KyNotes SSO/installer runbooks,
KyQuickStart root/CLI child and guided setup. Parent shared rules, child indexes,
backup/mirror children and frozen design/plan intentionally unchanged: no ownership/domain,
backup or production acceptance contract changed. No frontend/generated assets touched;
previous matching client verification stands. All previous uncommitted work preserved.

Next: target-held credential generation/transfer and bundle composition, then combined
native KyIdentity OAuth/directory/setup and actual sender delivery through mixed-use
handover. `internal/remote/secret.go` provides bounded private-file reads but does not yet
transfer credentials; existing product references still do not authorize adoption.
Source `internal/oauth/oauth.go` exposes `NewEngine`, `CreateAuthorizationCodeWithNonce`
and `ExchangeAuthorizationCode`; existing `internal/oauth/installer_setup_test.go` proves
native role issuance, but it is not composed with the receiver. Extend the disposable
composition rather than weakening product authority, ownership or secret-state rules.
Continue every supported server/admitted app, owner/everyday purpose, lifecycle/enrollment,
backups/bulk restore, provenance and guided full-stack acceptance. Vaultwarden only; its
native KyIdentity admin extension remains needed. This milestone does not finish all adapters.
Exact full ledger mirror required before closeout.


### 2026-10-10 — native KyIdentity OAuth composed with Notes directory delivery

Recovered the saved verified milestone after the reported memory termination. No owned
Go/build/test processes remained. Previous turn made recovery/resource evidence, not an
adapter milestone. This turn completes the native OAuth/directory fixture gap while
keeping the full all-product objective active. No production service, deployment, credential,
existing data, commit or PR changed; all previous uncommitted work remains.

KyIdentity `internal/sync/notes_native_test.go` is now external package `sync_test`, allowing
production API handlers without an import cycle. The fixture uses actual setup-owned
accounts/client/app roles and signed suite connector, genuine Argon2 password hashes,
production password/CSRF login, OptionalAuth authorization, code/token exchange, discovery,
JWKS and RSA signing. Native login creates every source session; no session/JWT claims are
seeded. Its private rendezvous adds only disposable issuer TLS certificate/client credentials.
The native worker keeps its actual netguard transport, redirect refusal and TLS verification;
only this anchored subprocess permits loopback and trusts its exact receiver CA via isolated
SSL_CERT_FILE/SSL_CERT_DIR. Worker recreation still proves committed connector intent/key.
No production transport injection, new dependency or relaxed enrollment/authorization.

KyNotes `internal/httpapi/identity_native_test.go` now builds native receiver handlers/store
without the unused fake issuer. Every sign-in follows real Notes discovery/nonce/state/PKCE,
source CSRF/password/authorization, native confidential token exchange and receiver RS256
verification. It waits for the next real issuance second instead of inventing a future iat.
Owner admin access/ordinary denial, everyday ordinary access/admin denial, unassigned global
admin exclusion/sign-in denial, scope loss, disable/reactivate/delete and dedicated-owner
reassignment all pass with native proofs. Sessions/devices/pending enrollment revoke while
notebook ciphertext, membership and ownership remain byte-for-byte unchanged.

Native app-role removal first proves a real limited token cannot use the retained local
administrator grant or enter ordinary Notes. It then delivers role loss and restoration in
separate phases, proving old-session revocation and restored owner administration. The
first experiment restored before dispatch and correctly exposed outbox coalescing: that
cannot prove intermediate revocation. Final fixture waits for role-loss delivery before
restoring it. Eleven native directory mutations have eleven receiver audits; no event bodies,
signatures or desired-state rows are manufactured.

Resource containment is now verified with a transient user-systemd cgroup:
MemoryMax=2147483648 (2 GiB), MemorySwapMax=0, TasksMax=128, CPUQuota=200%, alongside serial
GOMAXPROCS=2/GOMEMLIMIT=1GiB/GOFLAGS=-p=1 and disk scratch. Receiver and source child share
that cap. Root Work Guidance and Notes serial command document it. Go's soft heap target
alone is not the bound. No broad process arguments or credentials were printed.

Evidence, terminal exit0, logs in `/home/yoshi/.cache/kyquickstart-work/`:
- `kyidentity-native-oauth-build.log`: final race-compiled source binary
  `kyidentity-native-oauth.test` with native API/OAuth dependency composition.
- `kynotes-native-oauth-delivery.log`: actual combined fixture17.543s before removing
  unused fake-issuer construction; replaced the failed coalesced-delivery experiment.
- `kyidentity-native-oauth-sync-race.log`: complete sync package race suite91.897s,
  including existing connector/lifecycle regressions, under the hard resource cap.
- `kynotes-native-oauth-delivery-final.log`: final fake-free native OAuth/directory
  fixture17.263s under the same cap, all lifecycle/API/data/audit assertions.
- `kyidentity-native-oauth-vet.log`, `kynotes-native-oauth-vet.log`: affected packages vet.
- All three changed worktree diff checks and both changed test gofmt checks clean;
  documented bounded shell command parsed by `sh -n` successfully.
- Final process check found no remaining Go/compiler/linker/test process.

DOX updated KyIdentity root verification, Notes root verification and `docs/SSO.md`, plus
KyQuickStart root resource contract. Parents/child indexes, backup/mirror children, frozen
design/implementation plans and generated frontend assets intentionally unchanged: no
ownership, production API, encryption, backup or frontend contract changed. Previous product
build/full backend/UI evidence remains applicable; this turn changes tests/docs only.

Next: target-held credential generation/transfer and product bundle composition. Native
OAuth/directory acceptance is now proven for this disposable composition; do not reimplement
it using fabricated claims. Installer secret transfer, actual sender through mixed-use owner
handover/action-bound step-up, MFA/enrollment, sealed/bulk restore, product provenance and
whole guided-stack deployment remain unqualified. Continue every supported server/admitted
app and its owner/everyday/lifecycle gates. Vaultwarden only; its KyIdentity administrator
extension remains necessary. This is progress, not completion of the all-product objective.


### 2026-10-10 — Docker target-local credential generation corrected

Previous goal turn was progress: native KyIdentity password/OAuth and Notes directory
composition was implemented and verified. This turn traced the automatic credential/bundle
path and found an existing shared-driver violation of the target-only secret contract.
`internal/dockerhost/secrets.go` generated randomness on the workstation and sent the value
as runner stdin. That path was corrected before reusing it for product setup composition.
All-product scope stays active; no production deployment/credential/data changed, and no
commit/PR created. Existing uncommitted work is preserved.

Docker now creates 32 random bytes from the target `/dev/urandom`, checks the complete read,
encodes them to canonical unpadded base64url through separately checked commands, and
publishes a complete mode-0600 file with no-clobber `ln`. Workstation generation/imports and
credential-bearing stdin/command arguments are removed. Private random/encoded scratch
files have a cleanup trap for normal exit/handled signals; SIGKILL can leave private scratch,
never a partially published final credential. Existing files are retained, not regenerated.
Generation failures return fixed errors, discarding stdout/stderr so newly generated unknown
values cannot leak. Cancellation preserves the context error.

Verify now uses the existing protected `remote.ReadSecret` boundary, including owner,
ancestor path, symlink, exact permission and size checks; canonical 43-byte base64url is
required. Existing values are read back and registered for redaction. Target preflight
checks readable `/dev/urandom` and dd/base64/tr/mktemp/wc/ln/rm/find/id/dirname before deployment.
No new library or generic unused transfer helper was introduced.

Runnable evidence proves actual target origin: a target-side deterministic dd fixture
controls the resulting credential, and a runner refuses any stdin and checks no credential
entered command text. Existing generation/private/readback/resume tests still use real local
shell commands. New regressions cover short random reads, an encoder that emits complete
output but fails (POSIX pipelines must not hide that exit), cleanup without publication,
symlink/malformed readback and suppression of error/partial-output secrets. The real SSH
fixture additionally proves deployment/resume, private files and no credential in output or
workstation state.

Disposable hello Compose templates (demo and test catalog) now cap their diagnostic
container at 64 MiB, equal memory/swap maximum, CPU1 and PID32, matching the standing
resource-containment requirement. SSH/helper fixture containers already have their caps.
No unrelated Docker container was touched. No owned fixture/Go build process remains.

Evidence, all terminal exit0, under `/home/yoshi/.cache/kyquickstart-work/`:
- `kyq-target-random-race.log`: first focused Docker/remote race suite3.555s/1.135s.
- `kyq-target-random-race-final.log`: final focused race suite3.480s/1.119s, including
  failure/encoder/prerequisite tests.
- `kyq-target-random-ci.log`: make ci; tidy diff, gofmt, ordinary/e2e-tagged vet and full
  Go race suite. CLI2.855s, Docker4.306s, edge2.053s, kube2.505s, offboard28.317s,
  remote1.293s, other packages green.
- `kyq-target-random-ssh-e2e.log`: real Docker/SSH `TestApply`13.788s, fixture cleanup verified.
- `kyq-target-random-build.log`: workstation kyquickstart and resident kyoffboard rebuilt.
- Final diff and changed-file gofmt checks clean. Every Go suite/build ran serially under
  the documented hard 2GiB/zero-swap/PID128/CPU200% cgroup plus disk-backed scratch and
  GOMAXPROCS2/GOMEMLIMIT1GiB/GOFLAGS-p1.

DOX updated root credential/preflight contract and guided setup, explicitly naming the
Kubernetes implementation gap. CLI child, parents/child indexes and frozen architecture
spec remain unchanged: their target-only secret invariant already requires this behavior;
there is no new CLI selection, product authority or ownership boundary.

Next highest priority: `internal/kube/secrets.go` has the same workstation randomness
violation (crypto/rand/base64 in secretsStep.Apply). It must generate inside the cluster,
using a pinned, resource-bounded owned helper with typed Kubernetes objects, preserving
existing Secrets, namespace/object ownership, locks and no-secret state/report guarantees.
Do not qualify current workstation-generated new cluster Secret values as target-only.
Random lock identifiers and resident bridge nonces are separate uses, not app-credential bugs.
Then implement target-held credential transfer and automatic native setup-bundle composition;
current integrations still reference operator-supplied protected bundles. Continue mixed-use
handover/MFA/enrollment, every supported server/admitted-app purpose and lifecycle gate,
sealed/bulk restore, provenance and whole guided-stack acceptance. Vaultwarden only; its
native KyIdentity administrator extension remains required. No live service changes.


### 2026-10-10 — cluster-local credential generation implemented and verified

Previous turn was progress: shared Docker target randomness was corrected and verified.
This turn closes the corresponding Kubernetes workstation-randomness gap. Full all-product
scope stays active. No live deployment, existing product data/credential, commit or PR changed;
all prior uncommitted work remains. Only disposable owned fixtures were launched/removed.

`internal/kube/generate.go` creates a uniquely server-named temporary typed Pod in the owned
app namespace. BusyBox 1.37.0-musl is pinned to immutable linux/amd64 manifest
`sha256:dc88b80842580654294472735332ec7c7789a4010ec9a68b69f9b88c19abede6`. Registry tag/index
resolution yielded index `sha256:5cec3fc171c87218698e85a52af7087de727372aae264a787b8112901a5b0092`;
primary packaging/image sources are https://github.com/docker-library/busybox and
https://hub.docker.com/_/busybox. The exact digest was pulled and its generator command
exercised in a confined local container; no mutable tag is deployed. This is pin/fixture
verification, not a claim of complete suite production provenance.

Helper Pod runs as uid/gid65532 with RuntimeDefault seccomp, ALL capabilities dropped,
no privilege escalation, read-only root filesystem, no service-account token or service
links, no app Secret or persistent volume, 32Mi/100m requests=limits, a 1Mi memory scratch
volume, RestartPolicyNever and 180-second active lifetime. It sleeps without logging any
secret; generation happens solely through the existing bounded native exec transport.
Each command checks a full 32-byte target /dev/urandom read and each encoding operation,
returns canonical 43-byte base64url only, and cleans private scratch on handled exits.
No credential enters Pod command arguments/environment or container logs. Workstation
receives only bounded exec output in memory, as already allowed for readback.

The generator waits for its own UID/managed-label pod readiness, refuses stopped/replaced
pods, and removes it with UID preconditions before returning values for Secret publication.
Cleanup uses a bounded context detached from caller cancellation and verifies object removal;
failed cleanup returns no values and prevents publication, with the pod lifetime as a backstop.
Generation/invalid-output/publication errors suppress raw diagnostic values. API update
conflicts retain the existing transient retry behavior without printing credential-bearing
status bodies. Existing keys are never replaced, including invalid/empty keys, which Verify
refuses. Canonical encoding is also checked on readback. No workstation crypto/rand remains
in app credential generation; lock IDs and resident bridge nonces are separate uses.

`internal/kube/secrets.go` requests only missing keys, preserves resourceVersion and all
existing data, and registers new values for redaction before publication. `Client.execCommand`
is an unexported test seam for the native execution boundary; production clients continue
SPDY exec with bounded stdout/discarded stderr, and no fake-client generation fallback exists.
Unit fake API fixtures supply server-generated Pod identity/readiness and run the actual
shell generator through remote.Local. Real kind acceptance uses production client-go exec.

Preflight now checks get/create/delete pods and create pods/exec. `plan`/`review` display the
required pinned temporary helper for each selected Kubernetes app with declared secrets;
planning stays offline/read-only. The actual resource identity is uniquely server-issued,
so stale or foreign generator pods are never adopted. Existing namespace/Secret ownership
and target lock contracts are preserved. No new library, helper API credential or resident
operator service was needed.

`TestKubernetesApply` now caps its node before startup through a fixture-only Docker-provider
wrapper: 2GiB memory and equal memory+swap limit (no extra swap), CPU2, PID512. It asserts
those native Docker settings. Cleanup is registered before creation, covering startup failures.
The real cluster run verifies native generation, private/state-free results, helper removal
(the app is its namespace's sole remaining pod), rerun skip/reuse, container-local product
exec, drift repair and stale lock behavior. Kind cluster removal was verified afterward;
no unrelated container was touched.

Evidence, all terminal exit0, scratch `/home/yoshi/.cache/kyquickstart-work/`:
- `kyq-cluster-random-race.log`: first native kube package race2.430s.
- `kyq-cluster-random-focused.log`: kube2.491s and CLI2.526s, including helper security,
  UID-bound cleanup, malformed/failed exec, cleanup-failure nonpublication, retained empty
  keys, plan dependency and existing namespace/Secret ownership/conflict cases.
- Exact pinned image smoke: uid65532, readonly root, dropped caps, no network, 32MiB memory,
  CPU1/PID32, 1Mi tmpfs; actual command returned43 encoded bytes/32 decoded bytes, empty stderr.
  `kyq-credential-image-pull.log` records the digest pull, no credential values printed.
- `kyq-cluster-random-kind-e2e.log`: actual bounded kind/typed driver fixture69.361s;
  root Go process also ran under the standing hard2GiB/zero-swap/PID128/CPU200% cap.
- `kyq-cluster-random-ci.log`: final make ci, tidy diff/gofmt/ordinary and tagged vet/full
  race suite; CLI2.439s, Docker3.746s, kube3.181s, offboard28.372s, remote1.154s,
  remaining packages green. Final suite includes cancellation/cleanup and new permission tests.
- `kyq-cluster-random-build.log`: workstation kyquickstart and resident kyoffboard rebuilt.
- Final diff/gofmt checks clean; no owned fixture or Go build/test process remains.

DOX updated root credential contract, CLI child plan dependency contract, guided setup
(image sources/update verification, constraints/permissions) and architecture target driver
implementation. Removed the stale Kubernetes workstation-generation gate from current
operating docs. Parent/child indexes, other product/backup/mirror contracts and frontend
assets unchanged: no new ownership boundary, product API or application data behavior.
Existing historical ledger entries intentionally retain their evidence and chronology.

Next: automatic native setup-bundle composition and protected target credential transfer.
Both generic target drivers now originate new app randomness where it is used, but saved
integrations still reference separately supplied container-local bundles. Account/issuer/
role/connector graph construction must use actual native setup receipts and preserve
owner/everyday purpose, protected files, no credentials in local state and resumability.
Check Kubernetes projected secret ownership/mode (current render440/root+fsGroup) against
native setup readers requiring files owned by the product uid and private permissions;
prepare private target-held setup files through an authorized product/helper path rather
than weakening readers. Continue actual mixed-use handover/MFA/enrollment, every supported
server/admitted-app adapter lifecycle/purpose gate, sealed/bulk restore, provenance and
whole guided-stack acceptance. Vaultwarden only; native KyIdentity administrator extension
remains needed. Keep the full objective active; this infrastructure milestone is not all
adapters or production readiness.


## Continuation: protected KyNotes setup files (2026-10-10)

Recovered the prior cluster-local credential milestone after the reported memory termination;
source and saved successful build/CI/kind evidence survived. Revalidated the previous status-only
turn and took the next safe implementation step. No live deployment, credential, service,
commit or PR was changed. All host Go work ran serially in the standing hard 2GiB/zero-swap,
PID128/CPU200% cgroup with disk-backed scratch and constrained Go parallelism.

Tracing automatic bundle preparation found that KyNotes' native reader still used Stat/Open:
it followed links and accepted shared files, contradicting the protected target-file contract.
Fixed the shared reader in the isolated KyNotes-installer-setup worktree (feat/installer-adapter).
It now requires clean absolute paths and walks directories with Openat/O_NOFOLLOW using
opened descriptors. Parents belong to root or the container user and exclude other writers,
except sticky directories. Final opens are nonblocking/no-follow; fstat requires regular,
private files owned by the executing container user. Size is checked before and during bounded
read; failures return no partial content or credential values. Both bundles and every referenced
secret use this boundary. Existing configuration/data loading and the admin socket are unchanged.

KyNotes advertises protected-setup-files. The installer requires that feature alongside
existing dedicated-admin/check capabilities before mutation, refusing older insecure readers.
Native tests cover valid secret loading, shared modes, final and ancestor symlinks, writable
parents, FIFO rejection, relative paths, bounds and no disclosure. Foreign-owner cases require
root and are skipped on an unprivileged host; the descriptor ownership check remains enforced.
The real optional Notes SSH fixture additionally rejects shared, linked and FIFO bundles within
five seconds with no report, then proves valid apply/check/resume against the rebuilt binary.
The native container-check script now uses umask077 and explicit memory/CPU/PID/security limits;
its shell syntax was checked, but this turn's real container proof is the SSH fixture rather
than rerunning that script against a newly published image.

Evidence, terminal exit0, scratch /home/yoshi/.cache/kyquickstart-work/:
- kynotes-protected-setup-focused.log: applysetup race1.012s, CLI race10.106s (initial reader).
- kynotes-protected-setup-race.log: final whole native Go race suite; CLI8.703s,
  app7.964s, httpapi101.297s, storage8.753s, all remaining packages pass.
- kynotes-protected-setup-vet.log: whole native go vet.
- kynotes-protected-setup-build.log: static native product rebuilt as kynotes-protected-setup.
- kyq-protected-notes-ssh-e2e.log: actual TestApply7.375s, native Notes helper executed;
  safe-file rejection, valid create/read-only/resume and existing SSH deployment checks pass.
- kyq-protected-notes-ci.log: final make ci, tidy/gofmt/ordinary and tagged vet/full race;
  CLI2.346s, Docker2.969s, kube2.285s, offboard31.443s, remaining packages pass.
- kyq-protected-notes-build.log: workstation installer and resident bridge rebuilt.
- Both worktree diff checks and Notes gofmt/shell syntax clean. All tool handles terminal.
  Owned SSH/Notes fixtures removed; unrelated kymatrix-accept-471818-synapse-1 untouched.

DOX: Notes root and docs/INSTALLER.md now own native file protection and the advertised
capability. Installer CLI child and docs/guided-setup.md document the new gate and why default
shared Kubernetes projections require private user-owned preparation. Root/parent contracts,
child indexes, backup/mirror and frontend docs remain unchanged: ownership boundaries,
backup behavior, identity grants, schema and UI did not change in this prerequisite fix.

Next remains automatic native setup-bundle composition and protected target credential transfer,
starting with actual Identity/Notes native formats and the owner/everyday/client/connector graph.
Do not write arbitrary files inside products through shell exec: authority remains product-owned
apply-setup. Do not weaken readers to accept shared projected Secrets. Current integrations still
require supplied container-local bundle paths; no automatic composition is claimed by this fix.
Continue all supported server and admitted third-party adapters, purpose/lifecycle/migration,
MFA/enrollment, sealed/bulk restore, provenance and complete guided-stack acceptance. Vaultwarden
only, with the native administrator extension still needed. Full objective stays active.


## Continuation: native KyNotes bundle preparation over both targets (2026-10-10)

The previous goal turn made verified implementation progress in protected setup-file reading.
This turn builds the next concrete path: optional typed secret-free settings in a saved
KyNotes integration now produce the actual native target bundle, without manually copying
JSON or adding arbitrary shell authority inside a product. Full objective remains active;
this is one product's preparation path, not completion of every adapter or the guided suite.
No live deployment, existing data, commit or PR was changed.

Installer changes:
- `stack.Integration.Notes` contains typed version-1 SSO/admin/backup metadata and protected
  credential-file references. There is no inline credential field. Strict YAML parsing and
  validation reject wrong products, schema versions, unsupported output paths, credentialed
  URLs, wrong callbacks, invalid/duplicate identities, unsafe paths, invalid schedules and
  oversized encoded input before any batch connection/mutation. The schedule maximum matches
  the native 366-day bound; `notes` requires an actual selected section.
- `integrate-stack` automatically requests preparation for these saved selections.
  `integrate-product --prepare` requires the exact matching saved product/target/container/
  namespace/pod/bundle selection before connecting. Native capabilities, including
  bundle-preparation and all normal Notes gates, precede publication. Compare the retained
  metadata read-only before ordinary apply; still perform the normal running-state check.
- Docker uses bounded stdin through `docker exec -i`/remote.Run; Kubernetes ExecInput uses
  bounded client-go SPDY stdin. Neither embeds body data in argv nor creates workstation
  transfer files. Existing no-input execution/test seams are retained.
- Plan/review discloses native preparation and existing-target credential prerequisites.
  Guided resume retains typed metadata independently of the old answers file. New interactive
  metadata/owner-graph questions are still pending; this turn implements the unattended saved
  plan path, not full interactive identity/client/connector composition.

Native Notes changes, isolated KyNotes-installer-setup (feat/installer-adapter):
- `apply-setup --stage --file <configured absolute data_dir>/installer/setup.json` reads
  bounded secret-free JSON on stdin. It uses the existing initialized configuration loader,
  which still validates product keys. Referenced client/HMAC/pairing credential files are
  neither read nor generated during metadata preparation.
- Validate metadata before any writes. Reuse the shared native descriptor traversal for
  no-follow/protected ancestry. The data directory is container-owned and closed to other
  writers; it need not exclude other readers. The installer child must be private and
  container-owned. Never chmod/adopt shared storage or accept a symlink.
- Reuse the actually published ky-primitives v0.9.0 keyfile.Store(Raw) for private, fsynced,
  no-clobber publication. Its unpublished RequirePrivateDirectory helper is not imported.
  Compare exact canonical bytes on existing-file resume/check; changed or unsafe content
  is refused and retained. `--stage --check` creates no directory/file and uses no setup
  socket. Fixed preparation schema/status only; no arbitrary metadata or secret output.
- Ordinary apply/check remains the live product-owned configuration authority, with its
  existing administrator-purpose, SSO, backup and audit contracts. Preparation itself
  configures no account/service and does not imply a durable backup or administrator login.

Real acceptance:
- Existing SSH Notes fixture still checks unsafe-file refusal and native apply/check/resume.
  It additionally runs actual integrate-stack missing inspection, target preparation,
  apply/check/resume, changed-intent refusal in both modes and final original-intent inspection.
- The optional Notes fixture also runs inside real kind. It uses the rebuilt static native
  binary in a uniquely tagged disposable image, based on the product's pinned distroless base.
  A restricted uid65532 init container creates an owned child in bounded emptyDir storage;
  the product mounts that child as /data through volume subPath. This preserves private
  directory/file checks despite the shared fsGroup volume root. No shell is invoked inside
  the product container: all setup uses its actual CLI. Restricted pod security, no service
  account credentials, dropped capabilities, read-only root, bounded CPU/memory and default
  deny ingress apply. Fixture data is disposable, not a production PVC/restore qualification.
- All owned SSH/Notes/kind/image fixtures removed. Unrelated
  kymatrix-accept-471818-synapse-1 stayed untouched.

Evidence, all final handles terminal exit0, /home/yoshi/.cache/kyquickstart-work/:
- kynotes-preparation-focused.log: native applysetup race1.028s, CLI10.141s, including
  private publication, read-only noncreation, changed/unsafe input retention, bounded/unknown
  metadata refusal and native stdin dispatch without a setup socket.
- kyq-notes-preparation-focused.log: installer CLI2.376s, stack1.022s, kube1.997s;
  schema/credential boundaries, resume/disclosure, bounded stdin and preparation receipt gates.
- kynotes-preparation-build.log: static actual product binary `kynotes-preparation`.
- kyq-notes-preparation-ssh-e2e.log: actual native SSH/installer TestApply8.836s.
- kyq-notes-preparation-kind-e2e.log: actual native restricted cluster TestKubernetesApply61.335s,
  including preparation and existing generation/drift/stale-lock regressions.
- kynotes-preparation-race.log: final whole native Go race suite, httpapi100.700s, remaining
  packages green; kynotes-preparation-vet.log: whole native vet.
- kyq-notes-preparation-ci.log: final make ci tidy/gofmt/ordinary and tagged vet/full race;
  CLI2.349s, Docker2.938s, kube2.208s, offboard27.114s, remaining packages green.
- kyq-notes-preparation-build.log: workstation installer and resident bridge rebuilt.
- Final both-worktree diff checks and affected Go formatting clean. Go operations serial,
  hard2GiB/zero-swap/PID128/CPU200% cgroups; disk scratch, constrained parallelism and explicitly
  bounded owned containers retained throughout. Initial prototype compile/anchor failures
  were corrected before these final proofs; no process was restarted on an observation timeout.

DOX: root installer contract/verification, CLI child, guided setup and architecture command
contract describe typed preparation, its disclosure and remaining scope. Notes root and
INSTALLER.md describe native staging, its initialized-file exception to ordinary socket
setup, protection and no-overwrite rules. Parent shared-suite preferences, child indexes,
backup/mirror and frontend docs unchanged: no new ownership domain, backup/restore behavior,
identity grant or UI change. Existing guidance that all bundles were supplied separately
was replaced with the accurate Notes exception; graph/credential construction remains open.

Next: extend native preparation and typed composition to Identity and the remaining supported
Ky/admitted-app adapters. Build the actual owner/everyday/client/role/connector graph from
native receipts, with target-held generation and protected transfer of existing/missing
credentials; Notes preparation currently requires referenced credentials already on target.
Do not replace immutable retained bundles as a shortcut around native migration/conflict
handling. New interactive metadata questions, complete purpose/lifecycle/mixed-use migration,
MFA/enrollment, sealed/bulk restore, provenance and whole guided-stack acceptance remain open.
Vaultwarden only; its native KyIdentity administrator extension remains necessary. Keep the
full all-products objective active and continue safe local development, with no live changes.


## Memory-kill recovery — 2026-10-10

Recovered from the saved session capsule and checked the actual working trees. No reset,
cleanup of existing work, live deployment, commit or PR action was performed. Shared board
remains claimed by basalt; there were no posts newer than 2771 at recovery.

The interrupted Identity changes survived in
`/home/yoshi/git/busnes.app/KyIdentity-installer-setup`: setup metadata validation was
extracted from credential reads in `cmd/kyidentity/setup.go`, and
`Store.InstallerSetupExists` was added in `internal/store/installer_setup.go`. These are
preparation groundwork only: native Identity staging, immutable intent/credential receipts,
target-held generation and typed installer Identity preparation are not implemented yet.

Fresh recovery verification passed: native
`go test ./cmd/kyidentity ./internal/store -run '^TestInstaller|^TestPrepareSetup|^TestSetup' -count=1`,
CLI 2.120s and store 7.855s, final process exit0. Log:
`/home/yoshi/.cache/kyquickstart-work/kyidentity-recovery-check.log`. This compiles both
packages and runs matching existing tests; it does not prove the proposed preparation
feature. Executed serially in the required hard2GiB/zero-swap/PID128/CPU200% cgroup,
GOMAXPROCS2, GOMEMLIMIT1GiB, GOFLAGS-p1 and disk-backed scratch. No fixture started.

Prior Notes SSH/kind/full race and installer CI/build evidence remains recorded above;
those suites were not rerun during recovery. All-product work remains active and unfinished.
Next concrete task is native Identity preparation with no-overwrite receipts and target-only
credentials, then saved installer metadata and actual native transport acceptance.
Vaultwarden only; existing KeePass KyVault/data and all live services remain untouched.

DOX recovery pass: no runtime contract or ownership change in this recovery; existing
AGENTS.md hierarchy, resource caps and child indexes intentionally unchanged.


## KyIdentity native preparation and typed installer composition — 2026-10-10

Repo/worktree header above remains authoritative. Installer branch feat/phase-4-edge;
Identity source /home/yoshi/git/busnes.app/KyIdentity-installer-setup, feat/installer-setup.
All work remains uncommitted. No PR, merge, worktree/branch deletion or live deployment.
The all-products goal remains active and unfinished; this is a verified preparation milestone.

Native Identity:
- `apply-setup --stage --file <data>/installer/<canonical setup UUID>/setup.json` accepts
  bounded secret-free kyidentity.setup/1 stdin, deriving all generated paths in that owned
  scope. Reuse pure setupMetadata validation before any file creation or database mutation.
- Generate separate 32-byte crypto/rand values, canonical 43-byte base64url, for dedicated
  administrator/everyday bootstrap passwords, confidential clients and suite webhook HMAC
  credentials. Target only; no credential in arguments, workstation state or JSON reports.
  Generic SCIM tokens remain destination-issued protected references outside that scope;
  connectors require the existing product encryption key, never a newly invented one.
- Descriptor-based ancestry checks refuse symlinks, unsafe owners and other writers;
  sticky ancestors may precede the container-owned protected data anchor. Data can be
  readable by others, but installer and UUID directories are private. Never chmod/adopt
  shared/foreign directories; refuse nonempty unclaimed scopes using a bounded one-entry read.
- Publish private fsynced no-clobber intent, credentials and credential-digest receipt with
  published ky-primitives v0.9.0 keyfile.Store(Raw). Commit the receipt digest to existing
  native installer ownership with `installer.credentials_prepared` audit, then publish the
  usable bundle last. Receipt and audit commit atomically; no accounts or migrations in
  preparation. This remembers credential completion even after filesystem completion markers
  are lost before account creation. Inspection is read-only and never creates prep files.
- Initial interrupted publication resumes retained keys. Completed or already applied native
  ownership never permits replacement missing credentials. Changed metadata, key/token/file
  digests or unsafe files are refused and retained. Ordinary apply/check also requires both
  receipts at the reserved preparation path. Existing manually supplied bundles elsewhere
  retain their account/client transaction and password-preservation behavior. Reports remain
  fixed preparation schema/status; ownership metadata adds no extra account/client report item.

Installer:
- Typed `integrations[].identity` stores owner/everyday names/emails, UUID and native client,
  role, group and connector metadata only; generated path/secret fields are not accepted.
  Validate schemas, counts, canonical UUID/path, roles/groups/URLs, existing-token references
  and the complete integration batch before connections. All bundle paths now refuse control
  characters (also standalone CLI), matching native protected-file expectations.
- integrate-stack automatically stages explicit typed Identity before ordinary apply/check,
  with native capabilities bundle-preparation/target-credentials/protected-setup-files,
  present-only preparation readback, existing target locks and sanitized native reports.
  --prepare standalone requires exact saved product/target/container/namespace/pod/bundle.
- Shared bounded preparation receipt/readback helper has two real consumers, Notes and
  Identity; product-specific validation/generation remains native. Existing Docker/SSH and
  Kubernetes stdin transports are reused. Guided resume retains typed Identity metadata;
  plan/review discloses target generation. Fresh interactive owner/client questions and
  automated whole-suite graph construction are still pending.
- Promoted existing google/uuid v1.6.0 dependency from indirect to direct via go mod tidy;
  no new version or library. Initial tidy classification failure was resolved before CI.

Final evidence, all final process handles terminal exit0, disk scratch
/home/yoshi/.cache/kyquickstart-work/:
- kyidentity-stage-race.log: whole native race sweep; CLI29.515s, API314.605s,
  backup15.793s, MFA44.237s, OAuth103.153s, store315.646s, sync72.201s; remaining
  packages green/cached. CLI ran before the final native audit/lost-marker refinement.
- kyidentity-stage-final-focused.log: final changed-package race regressions after that
  refinement, CLI23.978s/store2.454s. Tests include generated bootstrap password hash
  verification, check/resume, partial initial publication, changed/missing credentials,
  lost completion files before account application, destination-token retention/drift,
  preexisting native ownership refusal without directory creation, malformed/unknown/large
  input, storage boundary refusal, native receipt drift and atomic audit rollback with no
  account creation. These are not owner MFA/browser sign-on or handover delivery proofs.
- kyidentity-stage-vet.log: final whole native vet; kyidentity-stage-build.log: rebuilt
  actual static product binary `kyidentity-preparation`, used in the final transports below.
- kyq-identity-stage-ci.log: final make ci tidy/gofmt/ordinary+e2e-tagged vet/full race;
  CLI2.430s, Docker2.953s, kube2.030s, offboard27.634s, stack1.031s, remaining green.
- kyq-identity-stage-ssh-e2e.log: final verbose actual SSH native TestApply18.910s; legacy
  account/group/SCIM/suite setup, prepared target generation, missing inspection,
  create/check/resume, changed-intent preservation and lost owner credential refusal.
- kyq-identity-stage-kind-e2e.log: final verbose restricted actual native Kubernetes
  TestKubernetesApply80.775s, with target generation/preparation, inspection/resume,
  changed-intent preservation and original drift/stale-lock/generation regressions.
  Product-owned private subPath directory, uid65532/no capabilities/no SA, read-only root,
  bounded resources and default-deny ingress; disposable emptyDir, not PVC/restore proof.
  Initial fixture installed the binary at the wrong path; corrected to the adapter's
  /usr/local/bin/kyidentity before these final proofs. No diagnostic secret output added.
- kyq-identity-stage-build.log: workstation kyquickstart and resident kyoffboard rebuilt.
- Final both-worktree diff checks and affected Go formatting clean. All owned images/SSH/
  kind fixtures removed; unrelated kymatrix-accept-471818-synapse-1 untouched. Builds/tests
  serial in hard2GiB/zero-swap/PID128/CPU200% cgroups, constrained Go and disk scratch.

DOX: installer root, CLI child, guided setup and architecture contract updated for typed
Identity preparation, local prevalidation, native capabilities/readback and remaining scope.
Identity root and INSTALLER-SETUP.md own immutable files, audited native receipt, protected
ancestry, credential generation and check/resume requirements. Parent shared-suite rules,
child indexes, native backup/mirror and UI docs intentionally unchanged: no new ownership
boundary, UI, custody contract or backup implementation change. Backup/restore of the
installer's retained preparation files is still part of whole-stack restore qualification.

Next: native export/import of scoped credentials over protected transport, compose actual
owner/everyday/client/role/connector intent from native receipts across Notes and every
other supported Ky/admitted-app product, and add fresh guided inputs plus real multi-product
acceptance. Current Notes preparation still needs existing credential files on its target.
Finish every admitted app's native adapter, administrator-purpose/lifecycle/mixed-use
migration gates, owner MFA/handover, sealed/bulk restore and source/build provenance before
whole-stack readiness. Production catalog remains gated. Vaultwarden only; its native
KyIdentity administrator extension remains necessary; old KeePass KyVault/data untouched.
Do not regenerate retained credentials, overwrite foreign bundles, silently adopt accounts
or describe these interface/transport proofs as complete deployment acceptance.


## Memory interruption recovery — 2026-10-10

- Recovered the saved worktree state after the user reported the agent was killed for memory usage. No changes were reset, committed or deployed.
- The in-progress native KyIdentity `cmd/kyidentity/setup_export.go` and `--export-client` dispatch survived. They compile; scoped export-specific tests and review remain unfinished. KyNotes import and installer credential-transfer wiring are not yet implemented.
- Re-ran the actual existing `TestSetupPreparation` cases in the native Identity worktree: `go test ./cmd/kyidentity -run '^TestSetupPreparation' -count=1`, passed in 4.719s. An earlier `Test.*Stage` filter selected no tests and is compilation evidence only.
- That verification ran alone under systemd MemoryMax=2G, MemorySwapMax=0, TasksMax=128, CPUQuota=200%, with GOMAXPROCS=2, GOMEMLIMIT=1GiB, GOFLAGS=-p=1 and disk-backed scratch. No verification process remains running.
- Continue with export-specific boundary/ownership/read-only tests, then protected Notes import and explicit saved installer wiring. Do not treat the draft private credential wire as a verified or released interface. Never put credential payloads into workstation state, argv, logs or this ledger.
- All-product completion, Vaultwarden native owner integration and full-stack acceptance remain open. Existing services and old KyVault data remain untouched. No durable architecture contract changed in this recovery-only checkpoint; owning AGENTS.md files are unchanged.


## Native scoped credential transfer interfaces — 2026-10-10

- Made concrete progress after recovery: native KyIdentity export now has scope/read-only regressions; native KyNotes has protected scoped import, CLI dispatch, capabilities, tests and operating docs. Installer transfer wiring is NOT yet implemented. No commits, PR work or live deployment changes occurred.
- Identity worktree `/home/yoshi/git/busnes.app/KyIdentity-installer-setup`: `cmd/kyidentity/setup_export.go` and `setup_export_test.go`, dispatch in `setup.go`. `--export-client ID` verifies complete prepared receipts and applied native ownership read-only, requires explicit configured HTTPS issuer, and emits at most 64 KiB private `kyidentity.client-credentials/1`. Selected OIDC/signing secrets, callbacks/endpoint and native owner subject/username/role only; no bootstrap passwords, other client secrets, SCIM token or recovery material. Normal reports remain secret-free. Capability `client-credential-export`.
- Notes worktree `/home/yoshi/git/busnes.app/KyNotes-installer-setup`: `internal/applysetup/import.go` and `import_test.go`, CLI `cmd/kynotes-server/applysetup.go` and its regression test. `--import-client [--check]` consumes that bounded private frame on stdin. It requires retained canonical `<data>/installer/setup.json`, exact issuer/client/callback, exactly one matching dedicated `kynotes.admin` owner, and exact selected directory endpoint. It writes only reserved `client.secret` and optionally `directory.secret`; canonical generated 32-byte base64url values only. Preflight every retained file before writes, pin source-frame/bundle digests in private `client-import.json`, no-clobber copies and same-source resume. Check writes nothing; foreign/unsafe/conflicting files and lost receipt with retained credentials are refused. Fixed schema/status output only. Import configures no SSO, account, grant or backup; normal socket apply/check must follow. Capability `client-credential-import`.
- Verification, all serial under MemoryMax=2G, MemorySwapMax=0, TasksMax=128, CPUQuota=200%, GOMAXPROCS=2, GOMEMLIMIT=1GiB, GOFLAGS=-p=1 and disk scratch: final full Identity CLI race suite passed 47.207s; whole Identity vet passed. Whole Notes Go race suite passed (`/home/yoshi/.cache/kyquickstart-work/kynotes-client-import-race.log`, HTTP package 140.838s). That full sweep preceded a small publication readback refinement and extra destination-scope cases; final focused Notes race run passed import package 1.187s / CLI 1.036s and whole Notes vet passed after those changes. Formatting and diff checks were clean.
- Rebuilt actual static binaries `/home/yoshi/.cache/kyquickstart-work/kyidentity-client-export` and `/home/yoshi/.cache/kyquickstart-work/kynotes-client-import`. A bounded Python stdlib subprocess fixture initialized each native service serially on loopback/disposable data, stopped servers, staged/applied Identity, exported the private frame in memory, composed owner metadata from native source subject, then staged/imported/checked/resumed Notes. Exact private destination bytes and modes, safe reports, repeated source bytes and absence of a Notes socket were verified. Both services and disposable data were cleaned up. This is real binary composition, NOT installer transport or downstream sign-on acceptance. Private payloads were never printed or saved in workstation installation state.
- DOX pass: both native roots and `docs/INSTALLER-SETUP.md` / `docs/INSTALLER.md` updated with authority, private transport exception, exact scope and verification limits. Shared parents/indexes, backup and browser docs unchanged because no shared ownership/index, backup or browser contract changed. Installer code/docs remain unchanged except this ledger.
- Next: explicit saved Notes opt-in selecting the declared prepared Identity client; validate the complete graph before connecting, resolve native owner metadata without guessing, require both transfer capabilities before destination mutation, connect only selected source/destination, transport private stdout/stdin in memory under destination lock, and import check before ordinary apply/check. Preserve typed guided resume without JSON fields that could persist credentials; disclose transfer in review. Build actual disposable SSH/Kubernetes transfer acceptance. Native source export currently requires all prepared/apply ownership to match; do not bypass that check or read raw target files with cat.
- Full goal remains active: all product adapters and native authorization/lifecycle/owner graph qualification, Vaultwarden native owner integration, fresh guided inputs, MFA/mixed-use handover, bulk backup/restore and full-stack production acceptance remain open. Vaultwarden only for new password installs; older KyVault/data untouched. No verification process remains running.


## Explicit installer Notes credential transfer wired — 2026-10-10

- Previous goal turn was progress: native export/import and actual binary composition. This continuation adds the actual installer path; full goal remains active. No live deployment, commits or PR work occurred.
- `internal/stack/stack.go`: new secret-free integration `identity_client` opt-in. `Stack.IdentitySource` in `integration.go` validates the complete graph after normal integration validation and before connections: Notes SSO with no saved guessed admins; selected client ID, reserved credential paths, declared prepared Identity source, exact `kynotes.admin`/`kynotes.user` roles, callback, selected suite directory endpoint, no SCIM substitution, and matching referenced Identity issuer URL. Source preparation/application still needs native verification.
- `internal/cli/credential_transfer.go`: require native source export and destination import capabilities before destination writes; privately export only the saved client; strict bounded frame/schema/setup ID/issuer/client/callback/signing endpoint/secret/owner validation; derive the canonical native UUID owner subject and username into a LOCAL Notes metadata copy. Stage and inspect metadata, import and inspect credentials, then re-export source and require exact same bytes before ordinary product setup/check. Secret byte buffers are cleared; import/export errors are fixed and never return private payloads. No source password or recovery material is requested, no secret or owner subject is saved to workstation state, and source bytes are never generated or rotated by the installer.
- `identity.go`: `--prepare` matching saved Notes selection connects only declared source/destination via existing Docker SSH or native Kubernetes transports and reuses destination lock. Shared `nativeProductExec` provides both actual drivers. Product setup clears `s.edges` so it has no unrelated NPM connection/credential reads; global deployment edge behavior is unchanged. Native adapter apply and read-only verification follow successful transfer. Source export is read-only and no second target mutation lock is taken. Saved stack batches run Identity first; standalone Notes preparation requires already-applied source ownership.
- `preparation.go` now reuses the bounded native input receipt/readback for both `--stage` and `--import-client`. Identity/Notes capability helpers accept extra required features. Guided resume retains the separate client selection, asks for exact client ID or `none` when typed Notes SSO metadata exists, and review discloses transfer rather than claiming credentials must preexist. Fresh interactive metadata/whole-owner graph composition remains unfinished.
- New `internal/stack/credential_transfer_test.go` verifies valid graph resolution and rejection of mismatched clients, roles, callbacks, endpoints, issuer, arbitrary secret paths, guessed admins and inline credentials. `internal/cli/credential_transfer_test.go` verifies secret-free resolved metadata, native stdin only, stage/import present-only ordering, check nonmutation, source-buffer clearing, unchanged saved owner metadata, source/destination capability gates and all private frame refusals before writes, safe import failure, mutating inspection refusal, post-import source drift and guided resume/review. These are protocol/CLI unit proofs, not real cross-target acceptance.
- Verification serial under the hard 2-GiB cgroup: initial full touched package race passed CLI 2.439s / stack 1.037s; final failure-path focused run passed CLI 1.062s / stack 1.025s. Final `make ci` passed tidy diff, formatting, ordinary plus e2e-tag vet and whole race suite (`/home/yoshi/.cache/kyquickstart-work/kyq-client-transfer-final-ci.log`, CLI 2.482s, offboard 28.357s, stack 1.061s). `make build` rebuilt workstation `kyquickstart` and resident `kyoffboard`. Diff checks clean across installer and both native worktrees. No verification process remains running.
- DOX: installer root, CLI child and guided runbook updated with selection/authority/connection scope and acceptance limits. Native Identity and Notes runbooks now acknowledge the implemented installer selection while keeping real transfer/sign-on acceptance open. Notes root wording updated likewise. Parent/index/backup/browser docs unchanged: no parent ownership/index, backup or browser contract changed.
- NEXT SAFE WORK: add actual disposable source/destination installer acceptance over SSH/Docker and restricted kind (use current static `/home/yoshi/.cache/kyquickstart-work/kyidentity-client-export` and `/home/yoshi/.cache/kyquickstart-work/kynotes-client-import`). Prove full CLI dispatch, owner metadata, authenticated source/destination transport, native protected import and resume/refusals, and ordinary native setup/check against a real local TLS issuer. Do not let mock receipts or the earlier offline two-binary composition stand in for actual SSO discovery/token/native authorization. Include a stack with an unrelated edge to qualify absence of NPM credential access. Each owned fixture must have hard resource limits and be cleaned individually; leave unrelated Synapse container untouched.
- Continue onward through every supported Ky product and admitted third-party adapter, native owner/everyday lifecycle/separation and necessary guided inputs, Vaultwarden native owner integration, MFA/mixed-use handover, independent bulk backup/restore, provenance and full-stack readiness. Vaultwarden-only password choice remains; old KyVault/data untouched. Production catalog stays gated; this checkpoint is not suite completion or deployment approval.


## Real SSH native credential-transfer acceptance — 2026-10-10

- Previous goal turn was progress (installer transfer wiring/CI/build); this continuation builds and verifies actual transport acceptance. Full all-product goal remains active. No live changes, commits or PR work occurred.
- New `test/e2e/credential_transfer_test.go`, invoked by `TestApply` only when BOTH native binary variables are set. It starts resource-limited, named native Identity/Notes containers on loopback, each from the previously rebuilt static binaries, with real local TLS reverse proxies and an owned CA. The certificate hostname `example.com` resolves only inside those containers to loopback via explicit Docker host entries. Native TLS verification, HTTPS rules, private credential readers and literal-loopback setup URL refusal remain unchanged. No system DNS or live service is changed. This Linux host-network fixture is not production edge or network-policy acceptance.
- Actual installer `integrate-stack` runs native Identity preparation/application first even when Notes is saved first, opens source/destination SSH connections, exports privately through native source authority, resolves the canonical owner, stages/imports/checks Notes, rechecks source and performs ordinary native Notes SSO/admin setup/check against real native TLS discovery. Logical source/destination target labels use separate SSH sessions against the owned Docker controller; this proves driver target selection and authenticated exec/private stdin between native containers, not distinct physical machines.
- Qualifies absent initial inspection refusal; apply/check/resume; destination owner subject exactly equals Identity's sanitized native user receipt; exact generated OIDC/signing credential copies; absence of those values from all installer reports and every readable workstation state file; modified destination credential refusal during BOTH check and apply, preserved conflicting bytes, and successful inspection after restoring original source bytes. An unrelated declared app/edge has an unusable hostname and no NPM token; the integration succeeds without connecting it or initializing edge credentials.
- Fixture development failures: first unrelated edge omitted its required route (fixed by adding a valid demo-only app/route declaration). Then literal loopback issuer/callback URLs were rejected by the existing native setup contract. Initially called that a bug in commentary, but inspected existing hostile-input tests and PRESERVED the intentional guard; use the named certificate/DNS fixture instead. No native validation change or security relaxation was made. Temporary mutating debug diagnostics were removed; proxy errors return fixed 502 without raw request logs.
- Final actual SSH acceptance passed: `/home/yoshi/.cache/kyquickstart-work/kyq-client-transfer-final-ssh-e2e.log`, `TestApply` 49.55s / package 49.565s, including existing Identity/Notes preparation checks and the new transfer acceptance. Earlier named-endpoint run passed 35.49s and strengthened owner/drift run 42.72s before final report/state checks; rely on FINAL source/result. All host Go runs serial under MemoryMax=2G, no swap, TasksMax=128, CPUQuota=200%, GOMAXPROCS=2/GOMEMLIMIT=1GiB/-p=1 and disk scratch. New native containers each memory/swap 512m, CPU 1, pids 128, heap384MiB.
- Final `make ci` passed (`/home/yoshi/.cache/kyquickstart-work/kyq-client-transfer-ssh-ci.log`): tidy diff, formatting, ordinary and e2e-tag vet, whole race tests. No production code changed this turn, so previous rebuilt installer remains current. Diff checks clean. All owned containers/data cleaned; only unrelated `kymatrix-accept-471818-synapse-1` remained running, untouched. Owned `kyq-e2e-sshd` image tag/cache removed after acceptance. No verification process remains running.
- DOX: installer root/CLI child/guided runbook and native Identity/Notes runbooks plus Notes root now distinguish QUALIFIED SSH transfer/native TLS configuration from OPEN Kubernetes transfer and browser sign-on. Parent indexes, shared suite contract, backup/browser docs unchanged because no ownership/index, shared contract, backup or browser behavior changed.
- NEXT: build actual restricted kind transfer (do not use mock receipts). Expose owned native HTTP backends through an authenticated/owned local test connection, serve TLS on an address reachable only from the disposable cluster, map the named CA hostname in pods, and preserve native URL/key protection. Qualify native exec private input/output, owner metadata, application/inspection/resume, drift preservation and cleanup. Existing individual Identity/Notes kind fixtures already qualify their preparation interfaces, but DO NOT qualify credential transfer between them. Then qualify real password/CSRF/PKCE/token/JWKS sign-on and administrator/everyday native API separation USING THE INSTALLER-COMPOSED credentials/owner, not a separate manually wired fixture. Earlier source/receiver tests and TLS discovery do not prove this.
- Continue through all other supported Ky and admitted third-party adapter interfaces/owner graph and lifecycle, Vaultwarden native administrator integration, fresh guided metadata, MFA/mixed-use migration, independent bulk backup/restore and full-stack readiness. Vaultwarden only for new password installs; old KyVault/data untouched. Catalog/production gates remain closed; no goal completion or live deployment authorization inferred.


## Restricted kind native credential-transfer acceptance — 2026-10-10

- Revalidated the memory-recovery turn: it inspected saved work and the board but made no implementation progress. This continuation makes concrete fixture/verification progress. The all-product goal remains active; no live changes, commits or PR work occurred.
- New `test/e2e/credential_transfer_kube_test.go`, invoked by `TestKubernetesApply` when BOTH native binary variables are set. Uses actual static Identity/Notes binaries and native commands, not mock receipts. Native pods and test-only TLS sidecars run nonroot with restricted Pod Security, read-only roots, dropped capabilities, disabled service-account token mounts and explicit resource limits. The owned CA names cluster-local Identity/Notes Services; only the sidecars mount the throwaway TLS private key. Native HTTPS validation and literal-loopback URL refusal remain unchanged.
- The static e2e test executable itself is the stdlib TLS proxy and private fixture inspection tool; it skips normally and is explicitly activated only in disposable containers. No helper or new dependency enters production products or installer authority. `make e2e` now sets `CGO_ENABLED=0` so this Linux fixture executable runs in pinned distroless. ELF validation rejects dynamic binaries before fixture deployment. Source/destination use separate native pods in one owned fixture namespace, not a production per-product namespace or production edge topology. Default-deny and fixture-only TLS ingress policies are declared; the test does NOT prove NetworkPolicy enforcement.
- Real `integrate-stack` applies prepared Identity first despite saved Notes-first order, transfers native private stdout/stdin through Kubernetes exec, resolves the canonical owner, stages/imports/inspects Notes and performs ordinary native SSO setup/check using CA-verified TLS discovery. Qualifies absent inspection refusal, apply/check/resume, exact source/destination credential bytes, canonical owner binding, no credential values in reports or any workstation state file, changed destination credential refusal during both check/apply, preservation of conflicting bytes and successful reinspection after the fixture restores original bytes. Test-only file inspection/mutation is confined to owned fixture data and never used by installer code.
- Actual `TestKubernetesApply` passed 115.69s / package 115.700s, including earlier native preparation fixtures, transfer acceptance, deployment drift and stale-lock checks. Log: `/home/yoshi/.cache/kyquickstart-work/kyq-client-transfer-kind.log`. The actual final fixture source matches the tested snapshot; a temporary extra source UUID assertion was removed before closeout rather than attributing an unrun edit to that result.
- Final `make ci` passed tidy diff, formatting, ordinary/e2e-tag vet and whole race suite. Log: `/home/yoshi/.cache/kyquickstart-work/kyq-client-transfer-kind-ci.log` (CLI 2.595s, offboard 28.016s, stack 1.042s). Diff checks clean. No production code changed, so the previously built installer binaries remain current. Host verification ran serial under MemoryMax=2G, MemorySwapMax=0, TasksMax=128, CPUQuota=200%, constrained Go and disk scratch. The actual kind node had 2GiB/no extra swap/2 CPUs/PID512; observed fixture load was 1.316GiB. All owned kind containers and fixture image tags were cleaned; the unrelated Synapse container was left untouched. No verification process remains running.
- DOX: root AGENTS.md, CLI child and guided runbook now record qualified SSH AND restricted kind transfer/TLS setup, the static executable requirement and the remaining downstream sign-on gate. Parent/shared rules, indexes and native product docs intentionally unchanged: no ownership boundary, native interface, backup, custody or browser behavior changed.
- NEXT: qualify actual password/CSRF/PKCE/code/token/JWKS sign-on and dedicated owner/everyday native API separation USING THIS INSTALLER-COMPOSED metadata and generated credentials. Reuse protocol mechanics from native Notes `internal/httpapi/identity_native_test.go` and Identity `internal/sync/notes_native_test.go`; prior manually composed fixtures do not prove the new installer composition. The SSH transfer fixture already exposes real named TLS backends and disposable source data, so extend it instead of adding another production interface. Read any test bootstrap password only from the owned source fixture, never export it through scoped client export or persist it in workstation installation state.
- Continue every other supported Ky and admitted third-party adapter, native owner graph/directory lifecycle, fresh guided metadata, Vaultwarden native administrator integration, MFA/mixed-use migration, independent bulk backup/restore, provenance and full-stack acceptance. Vaultwarden-only new password installs; old KyVault/data untouched. Production catalog remains gated. Neither browser sign-on nor whole-suite completion is inferred from this successful transfer fixture.


## Installer-composed native HTTP sign-on acceptance — 2026-10-10

- Previous goal turn was concrete restricted-kind transfer/CI progress. This continuation qualifies real downstream sign-on using the actual installer-created accounts/client/owner and transferred credentials, rather than the earlier manually composed source/receiver fixture. Full all-product objective remains active; no live changes, commits or PR work occurred.
- New `test/e2e/credential_signon_test.go`, invoked by the existing actual SSH transfer fixture. Reads bootstrap passwords only from the owned source fixture's retained `admin.password`/`everyday.password`. Separate cookie jars for owner/everyday, genuine native CSRF/password authentication, Notes discovery/nonce/state/PKCE S256, native authorization code and confidential token exchange, Notes claims verification and session creation. No source session, JWT, receiver account, directory event or grant is seeded. TLS hostname/certificate verification stays on; a test-only dialer routes only the two explicitly selected named HTTPS endpoints to their loopback fixtures, without changing system DNS or production transports.
- Verified owner `GET /api/v1/admin/users` succeeds and ordinary `/api/v1/containers` is refused; everyday identity gets the inverse. After both logins, actual native setup again inspects and resumes successfully, proving populated native accounts do not conflict with the retained installer owner graph. Browser UI rendering, owner MFA, broader action permissions and complete directory/offboarding lifecycle are separate gates; this is HTTP protocol acceptance, not a claim that every browser/deployment case is qualified.
- Extended `credential_transfer_test.go` with fixed-path atomic counters only (no query/body/header retention). Reset immediately before sign-on; actual native discovery, exactly two confidential token requests and JWKS traffic are observed. Existing transfer, unrelated-edge exclusion, native owner/exact credential checks and changed-credential preservation remain. Private-value checks now include BOTH generated account passwords alongside OIDC/directory keys, inspect all initial/post-login installer reports, and scan final workstation state after all resume operations. Private byte buffers are cleared when the fixture returns.
- Verification, serial and capped: first actual SSH `TestApply` passed 37.77s / package 37.779s; added post-login resume/password checks passed 43.29s / package 43.299s. Final source with endpoint counters and final-state scan passed 44.10s / package 44.110s. Rely on final `/home/yoshi/.cache/kyquickstart-work/kyq-composed-signon-proof-ssh.log`; earlier logs `kyq-composed-signon-ssh.log` and `kyq-composed-signon-final-ssh.log` are intermediate scope. No source changes occurred after the final actual SSH proof apart from documentation.
- Final `make ci` passed tidy diff, formatting, ordinary/e2e-tag vet and whole race suite: `/home/yoshi/.cache/kyquickstart-work/kyq-composed-signon-ci.log` (CLI 2.380s, offboard 31.249s, stack 1.058s). Diff checks clean. Host commands ran under MemoryMax=2G, no swap, TasksMax=128, CPUQuota=200%, constrained Go and disk scratch. New native transfer containers retain explicit 512m/512m/CPU1/PID128 limits. All owned containers were cleaned; unrelated Synapse remains untouched. No verification process remains running. No production code changed; previously rebuilt installer binaries remain current.
- DOX: root and CLI child plus guided runbook now distinguish qualified SSH native HTTP sign-on from still-open browser UI/MFA/Kubernetes sign-on/full lifecycle gates. Parent/shared rules, indexes and native product docs intentionally unchanged: no ownership boundary, native interface, backup or custody behavior changed.
- NEXT IMPLEMENTATION: build secret-free native preparation and scoped credential/owner composition for remaining Ky adapters; do not keep requiring manually prewritten bundles as the installer end state. Actual reusable boundary is `/home/yoshi/git/busnes.app/ky-primitives-installer-setup/applysetup/setup.go` (`Common`, `SSO`, `Identity`, private Decode/Secret and flags), NOT the main `ky-primitives` worktree which lacks this unpublished package. Reuse the existing shared package for Common-envelope products while products retain data-directory/role/callback/configuration authority; do not copy Notes implementation across every product. KyDrive worktree `/home/yoshi/git/busnes.app/KyDrive-installer-setup` uses `cmd/server/installer.go` and `docs/INSTALLER-SETUP.md`; it needs explicit native owner AND everyday identities plus receiver-owned SCIM token configuration. Notes' source-generated signed webhook secret is not a SCIM token and cannot substitute for that reverse credential flow. Existing prepared Identity SCIM connector tokens still reference protected target files; source export intentionally exposes no SCIM token or bootstrap password.
- Before running more optional product fixtures, bound their owned containers: inspection found the older `test/e2e/drive_test.go` native `docker run` still has no resource flags. Existing Docker fixture helper is `docker` in `test/e2e/e2e_test.go`; a fixture-only default cap for owned `run` calls may cover remaining older cases while preserving explicit larger limits. Never touch unrelated containers or weaken native path/ownership/security guards to make fixtures pass.
- Full supported/admitted-product preparation, owner/everyday graph and lifecycle, missing admitted third-party native adapters, Vaultwarden native KyIdentity administration, fresh guided metadata, MFA/mixed-use migration, independent bulk backup/restore, provenance and whole-stack acceptance remain open. Vaultwarden only for new password installs; old KyVault/data untouched. Production catalog remains gated. This is progress, not goal completion or deployment authorization.
