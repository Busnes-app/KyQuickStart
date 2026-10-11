# Third-party app catalog

Date: 2026-10-06. Status: approved; release admission remains evidence-dependent.

KyQuickStart installs the Ky suite plus a small catalog of third-party apps. This spec decides
which third-party apps are in the first catalog, the rules any future app must pass, and the
per-app integration work the installer owns. Installer architecture is a separate spec.

## Decisions

- One app per category. Alternatives may be added later as separate entries.
- Every app signs in through KyIdentity with native OIDC. No auth-proxy gate is built.
- Monitoring is not a catalog category: kyPulse covers health, alerts and logs. The installer
  registers every installed app as a kyPulse target instead.
- Third-party apps run on Docker hosts only, from Compose close to what upstream publishes. An
  app that leaves the catalog is handed to its owner with `kyquickstart unmanage` (installer spec).
- Third-party backups are app-aware: a restic timer runs each app's own dump command and backs up
  its volumes into an encrypted restic repository on an append-only rest-server off the NAS. The
  restic password and app secrets travel in KyRecovery sealed capsules.
- Mail hosting belongs to KyPost, not this catalog.
- Notes: no third-party OneNote replacement passes the rules. KyNotes is retained and gains
  OneNote-style notebooks (notebooks, sections and pages, freeform canvas pages, ink) as its own
  sub-project with its own spec.
- Forms and surveys are out of scope.
- Video calls are out of scope. Video streaming is deferred.

## Admission rules

A third-party app enters the catalog only if all hold:

1. **Open source.** OSI license. SSO and every feature the suite relies on are in the free
   edition; no proprietary add-on is required.
2. **Native OIDC.** Discovery plus authorization code flow against KyIdentity. It must not
   require refresh tokens, SAML or LDAP (KyIdentity provides none).
3. **Access from KyIdentity.** The app connection uses *Assigned users only*; unassigned users
   get no token. Roles come from the app's `roles` claim. Where the app cannot derive admin from
   a claim, the installer grants admin to the dedicated administrator identity through the app's
   CLI or API. The everyday identity never holds an admin grant.
4. **Offboarding.** The app accepts back-channel logout, or exposes a CLI/API to disable a user
   and revoke that user's sessions and tokens. The installer ships a deprovision adapter.
5. **Unattended configuration** through env vars, config file, CLI or API.
6. **Native clients work with OIDC.** An app usable only behind an auth proxy is rejected.
7. **Maintained.** A release within the last 6 months and a `linux/amd64` image pinned by
   digest. Community add-ons are labeled as such in the installer.
8. **Observable.** kyPulse can probe an HTTP health endpoint.

## Catalog

Versions are the latest releases checked on 2026-10-06; the release set pins exact digests.

| App | Purpose | License | Admin grant | Offboarding adapter | Notes |
|---|---|---|---|---|---|
| Holm (v0.3.1) | Home dashboard | MIT | `auth.admin_groups` from claim; drop `admin_usernames` | **Blocked**: signed 30-day session, never rechecked, no logout | Needs back-channel logout or session recheck upstream (or fork) before release |
| Vaultwarden (selected password service; release unpinned) | Passwords and passkeys | AGPL-3.0 | **Gated:** instance administration uses `ADMIN_TOKEN`, not a verified KyIdentity grant | Native disable/deauth APIs exist; adapter session/token proof remains open | Sole password choice; dedicated administrator separation and native owner administration must pass before admission |
| Forgejo (v16 / LTS v15; 16.0.5 fixture qualified) | Git hosting, Actions CI | GPL-3.0-or-later | Native OIDC app-scoped `roles`; required `aud` equals the KyIdentity client ID; explicit SCIM directory projection with new-account and group-policy opt-ins | **Gated:** native disable/token removal, actual KyIdentity worker creation/profile/role/revocation delivery, sign-in and actual KyIdentity group worker lifecycle verified; complete connector/deployment/restore pending | No back-channel logout; Git over HTTPS uses personal tokens; authenticated directory role replacement denies old-session administration before fresh OIDC; group receiver creates/renames/retires owned teams with retained repositories; production organization setup, group audit/restore and creation/write uncertainty reconciliation remain open |
| BookStack (v26.09.1 checked) | Wiki | MIT | `OIDC_USER_TO_GROUPS`, `OIDC_GROUPS_CLAIM=roles`, Admin role External Authentication ID `admin` | **Gated:** community module 0.15.0 verifies native setup/OIDC and actual identity-worker birth/profile/role/revocation delivery; complete lifecycle/deployment/restore/release pending | Upstream disable flags are ignored; community module retains accounts/content and denies sessions/tokens; no official image: build or pin one |
| Immich (v3.2.0 native fixture qualified) | Photos | AGPL-3.0 | `roleClaim` → `admin`/`user`; actual KyIdentity PKCE sign-in, assigned-only access and native administrator authorization verified | **Gated:** stock logout retains API keys; a pinned community module verifies retained-account session/key/socket denial and actual KyIdentity creation/profile/role/revocation delivery, with protected creation opt-in; complete lifecycle and deployment remain unfinished | Config via `IMMICH_CONFIG_FILE` or native API; account DELETE schedules content removal and must not implement offboarding. Mobile redirect `app.immich:///oauth-callback` or HTTPS override; native-writer coordination, complete identity lifecycle and release/deployment proof pending |
| Home Assistant + hass-oidc-auth (2026.9 / v1.2.1) | Home automation | Apache-2.0 / MIT | `roles.admin` from claim | Revoke refresh and long-lived tokens via API | Add-on is community-maintained: label it |
| Dolibarr (v24.0) | CRM, invoicing, ERP | GPL-3.0 | Installer sets admin flag via API | Disable user, revoke API key | Admin not claim-driven |
| LibreChat (v0.8.8 checked) | AI chat | MIT | `OPENID_ADMIN_ROLE`, `OPENID_REQUIRED_ROLE` | **Gated:** native ban expires and is optional; no verified retained-disable/token-revocation interface | Community native inspection module 0.1.0 and Go SDK qualified against real native models/MongoDB; persistent revocation and full setup/login remain gates; keep `OPENID_REUSE_TOKENS` off |
| Zammad (added 2026-10-10; release unpinned) | Helpdesk, ticketing | AGPL-3.0 | **Gated:** OIDC has no role claim; installer grants Admin to the dedicated administrator bound by subject, not email | Back-channel logout (`/auth/openid_connect/backchannel_logout`); disable user and revoke API tokens via API | Discovery, S256 PKCE, default scopes `openid email profile`; client-secret field absent (public client); keep automatic email account linking off and `No user creation on logon` on; admin password-reset fallback stays active when password login is off |
| Paperless-ngx (approved candidate 2026-10-10; release unpinned) | Documents, OCR | GPL-3.0 | `PAPERLESS_SOCIAL_ACCOUNT_SYNC_SUPERUSER_GROUP` from a claim (new; verify release) else installer grant | Django `is_active=false` (unverified: ends sessions and API tokens) | OIDC via django-allauth `PAPERLESS_SOCIALACCOUNT_PROVIDERS`; `PAPERLESS_DISABLE_REGULAR_LOGIN`; group sync applies only from second login (issue #9386) |
| Mealie (approved candidate 2026-10-10; release unpinned) | Recipes, meal planning | AGPL-3.0 | `OIDC_ADMIN_GROUP` from `OIDC_GROUPS_CLAIM=roles`; `OIDC_USER_GROUP` for ordinary access | No back-channel logout; native user disable/token revocation unverified | v2 requires a client secret; `ALLOW_PASSWORD_LOGIN=false`, `OIDC_AUTO_REDIRECT` |
| Linkding (approved candidate 2026-10-10; release unpinned) | Bookmarks (replaces KyMark) | MIT | Installer creates the owner superuser before first OIDC login | Django `is_active=false` (unverified: ends sessions and API tokens) | **OIDC links accounts by email**, conflicting with the no-email-adoption rule; KyIdentity must own emails and assigned-only access must hold. `LD_SUPERUSER_NAME` breaks OIDC (issue #988) |
| Actual Budget (approved candidate 2026-10-10, replaces FlyBudget; release unpinned) | Personal finance | MIT | **Gated:** first OIDC login becomes the unchangeable server owner and admin; installer must ensure the dedicated administrator logs in first (or pre-creates users with `ACTUAL_USER_CREATION_MODE=manual`) | Native user disable in the User Directory; session/token revocation unverified | OIDC multi-user since 25.1.0 (marked experimental): `ACTUAL_OPENID_DISCOVERY_URL`, client secret, `ACTUAL_OPENID_ENFORCE=true`; roles Basic/Admin are not claim-driven |

Vaultwarden admin page, ACCEPTED RISK (Yoshi, 2026-10-10): instance administration stays
`ADMIN_TOKEN`-only. The installer generates the token on the target, configures it as an
Argon2 PHC hash, and hands the plaintext once to the dedicated administrator with the rest
of the administrator credential set; it is never kept in workstation state. Users sign in
through upstream OIDC SSO (`SSO_ENABLED`, `SSO_ONLY`, `SSO_SIGNUPS_MATCH_EMAIL=false`).

Admission evidence update, 2026-10-09: Vikunja v2.7.0 resolves to commit
`a16be96aa454671fdf213b0fbe411dd38a098418`; its
[native administrator command](https://github.com/go-vikunja/vikunja/blob/a16be96aa454671fdf213b0fbe411dd38a098418/pkg/cmd/user.go)
checks the admin-panel entitlement before any grant. The
[upstream Pro documentation](https://vikunja.io/docs/pro/#admin-panel) also confirms the
administrator CLI and API require that entitlement. Rejection follows rule 1, not a
change to the task-management requirement; no replacement is selected yet.

Immich's partial adapter pins v3.2.0 commit `1b6098c9dbfffe978bec2d414606ed7a4c8e019a`.
[Native user administration](https://github.com/immich-app/immich/blob/1b6098c9dbfffe978bec2d414606ed7a4c8e019a/server/src/services/user-admin.service.ts)
supports administrator updates, but DELETE marks accounts/content for removal. The
[deletion worker](https://github.com/immich-app/immich/blob/1b6098c9dbfffe978bec2d414606ed7a4c8e019a/server/src/services/user.service.ts)
later removes files; it must not serve as content-preserving offboarding. Boundary fixtures
for provider/subject/authority inspection do not qualify native sign-in, account lifecycle,
API-key/session revocation or deployment.

The opt-in native fixture now qualifies owner/everyday signed OIDC and exact native
administrator grant/repeat/demotion against the official amd64 image
`ghcr.io/immich-app/immich-server@sha256:949bbf9ff32543539f471bc275437e6d1a1a4fee43c55bb9e4c06b946909bf1c`.
Its OCI revision matches the pinned source. Verified RSA back-channel logout denies the old
everyday session and preserves the unrelated owner, but the everyday personal API key still
returns successful account access. Stock logout is insufficient. The AGPL-3.0 community
extension in `internal/adapters/immich/module` adds installation-bound native revocation:
a write-once metadata fence and session/key removal commit together, subject denial prevents
new sign-in, and subscribed sockets disconnect. Native qualification verifies direct API
scope/authority refusals, retained account/profile, photo records and original bytes, unrelated
owner access and permanent repeat. The client refuses incomplete/foreign proofs and holds
unknown writes. An optional real-provider fixture now qualifies actual KyIdentity owned
setup/client/role receipts, Assigned users only, signed S256 code exchange and native owner
administrator API access while denying everyday administration. Unassigned users and global
administrators receive no app code. The installer creates the exact native accounts over
SSH/Docker and suppresses actual provider subjects; after module revocation a fresh genuine
authorization code cannot restore app access. This does not admit Immich: complete identity
lifecycle, restore, dedicated administrator ordinary-workflow separation
and production release/deployment remain gated. The overlay is a local fixture, not an upstream
feature or production provenance claim.
The resident bridge now pins UUID/client receipts, verifies registration and drives this module
through authenticated SCIM intent and actual worker retry. The disposable native fixture proves
unavailable-write backoff and retained completion/audit proofs across restart. The bridge
sender remains a controlled SCIM helper even in real-identity OIDC mode; actual KyIdentity
worker/event delivery and active account lifecycle remain unqualified.

The provider consumer now uses native system configuration with a protected preflight
fingerprint. It sets verified HTTPS OIDC, signed ID tokens and the roles claim, retains
unrelated configuration exactly and independently checks full readback. Native qualification
proves configuration/repeat and stale/foreign refusal before signed login. Its upstream
full-document PUT lacks conditional updates; production requires control over every
configuration writer, protected target-local intent and installation ownership. This consumer
is separate from the community product-owned setup command and does not provide ongoing provisioning.

The community module now includes container-local `immich-admin apply-setup` with a protected
`immich.setup/1` bundle. A serializable transaction creates absent owner/everyday accounts and
cluster groups, writes native OIDC configuration and pins installation/intent/native UUID
receipts. Check is read-only; repeats verify exact ownership/profile/role and refuse migration,
foreign accounts/provider/installation and unsafe bundle paths. Signed native login uses the
created UUIDs. Saved integrate-stack now dispatches initial setup over SSH/Docker under a target
lock, verifies native readback and suppresses raw subjects/secrets. This is initial setup only:
actual identity event delivery,
ongoing lifecycle, administrator ordinary-workflow separation and release/deployment remain gated.

The module now provides native owned directory projection for its two initial accounts.
Each has a revisioned `ky.immich-directory/1` receipt; exact initial setup/provider/subject
and current native state are verified before atomic name/email/administrator changes.
Native qualification proves repeat without revision growth, original setup inspection after
legitimate profile updates, stale/foreign/duplicate-email rollback and refusal to adopt an
unreceipted edit. Role demotion denies existing-session administration, and a stale signed
OIDC role cannot overwrite the current owned grant. Profile inspection remains possible after
revocation, but updates cannot reactivate the account. The Go consumer verifies native readback
and holds uncertain writes. Protected resident directory mode uses this consumer; controlled
native SCIM qualification covers profile/repeat/role updates, immutable identity, durable
proofs and permanent revocation. A separate actual KyIdentity worker fixture additionally
proves initial owned profiles, name/email changes, old-session administrator grant/removal
and assignment-loss revocation, with durable identity acknowledgment and restarted runtime
proofs. With a separate protected creation opt-in, actual new assignment creates an owned
ordinary UUID account, signs in through subject-bound PKCE and revokes sessions/keys on
global account disablement while retaining its profile. Reopened creation/initialization/revocation
proofs pass. Global multi-connector lifecycle, native-writer coordination and full connector
audit/restore remain gates. Its private pinned SSH fixture forwards raw TLS TCP; it creates no sign-in proxy or
production dependency and does not change host firewall rules.

Installer-owned OAuth requires a provisioned account and owned directory receipt. Native
first-login registration and email adoption are refused, and link/unlink routes cannot change
managed identity. This closes the normal login bypass of protected creation; external writers,
operations already in flight at initial setup and database uniqueness still require qualification.

## Installer-owned integration work

1. **OIDC client per app** in KyIdentity: exact redirect and logout URLs, back-channel logout
   URL where supported, app role names (`admin`, `user`, plus app-specific names such as
   Immich's), *Assigned users only*, the admin identity mapped to `admin`, the everyday identity
   to `user`.
2. **Admin grant step** for Dolibarr, run once and verified after first login.
3. **Deprovision adapter** per app, triggered when KyIdentity ends a user's access; verified by
   disabling a test user and confirming sessions and tokens fail.
4. **kyPulse registration** of each app's health endpoint and log source.
5. **Backup declaration** per app: dump command, volumes to include and
   exclude, secrets for the sealed capsule, and restore order (see Backups).

## Backups

Two paths, split by size and sensitivity.

**Sealed capsules (small, secret).** App configuration, OIDC client secrets, app encryption keys
and a copy of each restic repository password are sealed with `ky-primitives/recoveryclient` and
delivered to KyRecovery and the local backup directory under the suite KyRecovery contract.

restic encryption is symmetric: the backup client needs the repository password to write, so the
password also lives on the app's target. Custodians therefore protect recovery after the target
is lost; whoever controls a live target can already read that app's data and its backup history.
The rest-server host never holds a password and sees ciphertext only.

**restic (bulk, app-aware).** Capsule caps (384 MiB per container) rule out bulk data,
so:

- **Engine:** restic (BSD-2-Clause) with client-side encryption; the target sees ciphertext only.
- **Kubernetes (Ky products):** K8up (Apache-2.0, restic-based operator; 4.10.0, 2026-07-17). The installer
  writes a `Schedule` per app and a `k8up.io/backupcommand` annotation on the app pod; K8up runs
  the command in the pod and streams its output into restic, then backs up the app's volumes.
  Scheduled `check` runs verify the repository.
- **Docker hosts** (every third-party app, and KyDrive on the NAS): restic on a timer runs the
  app's dump command with `docker compose exec`, streams it into restic, then backs up the app's
  volumes.
- **One repository and password per app**, so a compromised target exposes only its own apps'
  backups, and `--private-repos` keeps each client inside its repository.
- **Target:** rest-server (BSD-2-Clause) as a separate container on the KyRecovery host, with
  `--append-only --private-repos` and TLS. KyRecovery is already the independent store, so bulk
  and sealed backups share one off-NAS host. Backup clients get append-only credentials;
  `forget`/`prune` runs from a separate admin credential with time-based retention
  (`--keep-within`).
- **Independence:** the KyRecovery host and its storage must be off the NAS, on a host the NAS
  holds no credentials for. Preflight refuses a rest-server data path on NAS storage; a NAS-local
  copy must not be described as independent.
- **Status:** KyRecovery mounts the repository directory read-only and shows, per app, the last
  snapshot time, snapshot count and size, all readable from the repository layout without a
  password. Backup and `check` results also reach kyPulse, which alerts on failed or stale
  backups.

| App | Backup command (streamed to restic) | Volumes | Skip |
|---|---|---|---|
| Immich | built-in daily dump in `UPLOAD_LOCATION/backups` (verified) | `library`, `upload`, `profile`, `backups` | `thumbs`, `encoded-video` |
| Forgejo | `pg_dump` | repositories, LFS | caches |
| BookStack | `mariadb-dump` | uploads | |
| Vikunja (existing owner-managed installations only) | `vikunja dump` | attachments | |
| Home Assistant | built-in backups | `/config` | |
| Dolibarr | `mariadb-dump` | `documents` | |
| LibreChat | `mongodump` | uploads | |
| Zammad | `pg_dump` (unverified: native backup script) | attachments/storage | search index (rebuildable) |
| Paperless-ngx | `document_exporter` (unverified) | media, data | |
| Mealie | built-in backup (unverified) | data | |
| Linkding | SQLite `.backup` | data | favicons, previews |
| Actual Budget | SQLite `.backup` of server and budget files (unverified) | data | |
| Holm | SQLite `.backup` | `config.yml` | |

Restore order per app: database first, then files (Immich's documented order).

### Restore

- **Start from either place.** KyRecovery's UI picks a product and a point in time and shows the
  matching capsule and the restic snapshot nearest it, then gives the exact command; the CLI can
  start the same restore directly.
- **One executor:** `kyquickstart restore <app>` on the operator's workstation, which holds the
  SSH and cluster access KyRecovery deliberately lacks.
- **Decryption never happens in KyRecovery**, server or browser. Combining k shares rebuilds the
  suite recovery private key; doing that in pages KyRecovery serves would let a compromised
  KyRecovery capture it on any restore. Shares are typed into the product's `restore` command
  or `kyquickstart restore` on stdin, never argv.
- **Bulk data, target alive:** restic restores on the target with the password already there;
  no password moves.
- **Bulk data, target lost:** the password comes from the app's capsule, opened on the
  operator's workstation with k custodians present, and is used for that restore only.

Rejected engines: Kopia (repository server holds the key), Velero (one shared static key),
Duplicati (proprietary parts, paid SSO), Borg 2 (beta, drops append-only), MinIO (archived).
Garage lacks Object Lock; SeaweedFS is the only checked S3 target with Object Lock if an S3 tier
is added later.

## Edge and reverse proxy

Infrastructure, not catalog apps; the admission rules do not apply, but these do:

- **Reverse proxy: Nginx Proxy Manager** (MIT). If one exists, the installer adds and enables
  only its own hosts through NPM's API (create disabled, verify, enable) and never edits other
  hosts or NPM's database. Otherwise it installs one NPM per target (installer spec, Edge
  placement). TLS terminates at NPM on the LAN.
- **NPM admin (port 81):** LAN-only, never published at the edge. No SSO; generated admin
  credentials go in the printed handover.
- **Forwarded headers:** each installer-created host overrides NPM's broad real-IP trust and
  sanitizes client-supplied forwarding headers, as in the ky-kubernetes KyPost route.
- **Edge, operator's choice:**
  - *Bring your own Cloudflare.* Two zone-scoped tokens: tunnel (Tunnel Edit, DNS Edit, Zone
    Read) and certificates (DNS Edit). The installer states that Cloudflare decrypts all public
    traffic, caps uploads at 100 MB on Free/Pro, and may act on video-heavy hostnames.
  - *Self-hosted (default open-source).* frp (Apache-2.0) on a public VPS in `https` SNI mode:
    it forwards encrypted streams, so the VPS never sees plaintext. Public DNS stays at the
    operator's registrar.
- **Certificates:** DNS-01 through NPM. acme-dns (MIT) by default, after one CNAME per name, so
  the credential can change only challenge records; the Cloudflare plugin with the BYO option.

Rejected: Traefik (a second proxy adapter for no hard gain), Pangolin (decrypts on the VPS;
commercial-licensed features), NetBird reverse proxy (beta), rathole (unmaintained).

## Rejected or deferred

| App | Reason |
|---|---|
| Grafana, Kener, Uptime Kuma, Gatus, Beszel | Monitoring covered by kyPulse; Uptime Kuma also has no OIDC |
| Gitea | Passes, but open-core under Gitea Ltd (SAML, audit log, enforced 2FA are Enterprise); Forgejo preferred |
| Woodpecker CI | Redundant with Forgejo Actions |
| Outline | BUSL-1.1, not open source |
| Docmost, Plane, Planka v2, OpenProject, Mattermost Boards | SSO behind paid tier or non-OSI license |
| Vikunja v2.7.0 | Required instance administrator grant and API require a Pro entitlement; fails free-edition administration. No new installs; preserve existing services/data. Task-management category closed 2026-10-10: covered by KyNotes tasks, KyCalendar VTODO, Forgejo issues/boards and Zammad |
| WeKan, Kanboard, Leantime | Not admitted; no replacement needed after the task-management category closed (2026-10-10) |
| Wiki.js | BookStack selected; settings live in DB, 3.0 still beta |
| PhotoPrism | User roles and user management are paid; no official mobile app |
| Jellyfin | Deferred: no native OIDC; single-maintainer alpha SSO plugin; revisit when core SSO ships |
| Plex | Proprietary, plex.tv accounts only |
| Frigate, Firefly III | Proxy header auth only |
| Open WebUI | Non-OSI license |
| Twenty | SSO code enterprise-licensed |
| Invoice Ninja, Akaunting | Non-OSI licenses |
| EspoCRM | Passes; Dolibarr covers CRM and invoicing in one app |
| Audiobookshelf | Second app in a category |
| Video calls (Jitsi, Element Call) | Out of scope |
| Joplin Server | Personal Use License forbids business use; SAML/LDAP only |
| LimeSurvey | No OIDC in core; plugins give one fixed role, no user-disable API. Forms are out of scope |
| Formbricks, OpnForm | OIDC in enterprise-licensed code |
| salt.md | Google/Microsoft sign-in only: no generic OIDC, no role sync, no deactivation |
| OneNote replacements: AFFiNE, AppFlowy, Anytype | Non-OSI server/client license or seat-capped free tier |
| OneNote replacements: SiYuan, Trilium | Single-user; OIDC only gates the instance |
| OneNote replacements: Logseq | Self-hosted sync is beta and accepts Cognito tokens only; revisit when stable |

## Unverified at plan time

Zammad candidate, 2026-10-10 (user request). Sources:
[OpenID Connect](https://admin-docs.zammad.org/en/latest/settings/security/third-party/openid-connect.html),
[third-party login](https://admin-docs.zammad.org/en/latest/settings/security/third-party.html),
[security base](https://admin-docs.zammad.org/en/latest/settings/security/base.html).
Documented: discovery is required; PKCE supports S256 only; back-channel logout with
session required; HTTPS only, no self-signed certificates; email-based automatic account
linking is off by default; `No user creation on logon` refuses unknown users; password login
can be disabled once a provider is enabled, but "Zammad automatically offers a password
request for administrator users". Open before admission:
1. KyIdentity must issue a public PKCE client; Zammad's documented setup has no client secret.
2. Unattended configuration and an admin grant bound to the OIDC subject (not email linking)
   through Zammad's console or API; pinned version and digest.
3. ACCEPTED RISK (Yoshi, 2026-10-10): the administrator password-reset fallback makes
   owner access depend on mailbox control.
4. Offboarding proof: deactivation plus API-token revocation denies existing sessions.
5. kyPulse health probe: the monitoring endpoint requires a token.
6. Resource cost of the required search, cache and database services on a homelab host.

BookStack qualification, 2026-10-09: disposable v26.09.1 tests reproduce upstream's
ignored disable flags and continued session/token access. The MIT community module in
`internal/adapters/bookstack/module` uses [logical theme hooks](https://www.bookstackapp.com/docs/admin/hacking-bookstack/#logical-theme-system)
and a [registered native command](https://github.com/BookStackApp/BookStack/blob/31d94dd8b98e3334dc01b66bed6d514719a2f85d/app/Theming/ThemeService.php).
Native standard-auth tests now prove exact-binding/private-file refusal, retained identity,
roles and content, old web/API session denial, API-token removal, new password-login denial,
concurrent retries, restart persistence and continued denial after token recreation or account
rebinding. A new native ID with the same revoked subject is also denied. Internal framework
and model usage is release-sensitive. The Go client now verifies the native command's pinned
module contract and issuer/subject digest, stages independent private target files, and
confirms both revocation fences plus zero tokens. A real-container test repairs deliberately
incomplete fence publication with zero remaining tokens; unit/race tests reject malformed
authority and unsupported modules before mutation. Native receipt-owned account creation
and lost-response resume also pass; owner grants remain pending until actual verified OIDC.
Module 0.4.0 also commits an exact-source/subject assignment receipt with account creation.
Native OIDC resolves this assignment before registration and role synchronization; unknown,
rebound or revoked subjects cannot receive roles. A disposable signed IdP with verified TLS
now proves native discovery/PKCE, owner grant and Go verification, everyday management
denial, invalid-signature/unassigned-subject refusal, old OIDC session/token denial and new
login refusal while retaining identity, roles and content. This qualifies the native interface
against the fixture provider. Module 0.5.0 adds a clean missing-code refusal after the native
controller consumes and verifies callback state, covering actual KyIdentity `access_denied`
without reflecting provider error text or running token exchange.
The fixture's native-provider mode now creates/checks/resumes actual KyIdentity accounts,
confidential client and app-role assignments through `apply-setup`, then proves native
discovery, HTTPS/PKCE, exact native subject bindings, owner administration and Go verification,
everyday management denial and unassigned-user refusal. KyIdentity's audited, operation-bound
step-up removes the everyday assignment and blocks fresh issuance. Existing BookStack sessions
and native API tokens remain until explicit product adapter revocation, which is also verified
with retained identity/roles/content. Source assignment removal alone does not satisfy
downstream revocation; the resident service and full connector composition remain gated.
Module 0.15.0 preserves an explicit empty ordinary-role policy for administrator-only
native accounts; each resident account can pin its own policy without adding membership.
Missing/null native policy arrays refuse. It includes protected native administrator projection over pinned ordinary-role
policy, with revision/audit/readback and signed-login consistency. Native managed role edits
are refused, and old sessions lose administration after demotion. This does not enable the
directory receiver, administrator purpose separation or deployment admission.
The module also provides protected container-local name/email projection with explicit owned-profile
initialization, revision checks and atomic native receipt/audit publication. Native managed
profile edits/deletion are refused, and fresh signed OIDC preserves projected profiles. This
does not enable the directory receiver or deployment admission.
`kyquickstart:directory` combines explicit initialization, inspection and profile/role
projection in one native transaction with separate revision audits. A failed role write
or audit rolls back profile changes. Module 0.15.0 exposes check/apply through a separate
protected HTTPS directory credential; it refuses native initialization, unowned/uninitialized
accounts and revocation/content authority. Resident registered-account wiring now has explicit
protected directory authority and per-subject native receipt scopes. The actual resident TLS
service passes controlled SCIM profile/grant/demotion/repeat against pinned native BookStack,
joined shutdown and reopened binding/audit proofs; the parent verifies native profile and
old/fresh ordinary access. Separate actual-producer qualification now passes receipt-owned
connector setup, initial/profile delivery, old-session administrator grant/demotion and
assignment-loss revocation with newer native acknowledgments. Private raw-TCP SSH forwarding
retains verified TLS and distinct credentials; reopened bindings and every directory audit
remain intact. Single-product global disablement also passes through a separate surviving
identity administrator, with old identity/product session and token/fresh-login denial,
retained owner receipts/content and both reopened revocation/audit chains. Cross-product
global and full lifecycle qualification remain required before
deployment admission. Groups remain unsupported; new accounts require the protected
creation opt-in below.
Module 0.15.0 adds native `kyquickstart:create` and strict joined SDK readback: ordinary
account, assignment, verified non-management roles, directory initialization and immutable
installation/account creation/audit receipts are atomic. Native policy/audit failure rollback,
unknown-result inspection, no prior-account/collision adoption, projected-profile-preserving
resume, signed ordinary use and retained revocation pass. A separate scoped HTTPS creation
endpoint fixes installation/source/ordinary-role policy in a protected target file and
provides absence/create/receipt inspection; direct SDK retry inspects ownership before
repeating ambiguous writes. Module 0.15.0 additionally resolves exact native created scopes
through joined account/creation/audit/directory proof; the explicitly enabled resident
creation lane keeps its original protected account map immutable. Actual worker birth,
profile projection, signed ordinary use and global retained-account disablement are covered
by the combined producer/create fixture. Cross-product lifecycle, audit, restore,
administrator purpose and release provenance remain admission gates.
The module retains versioned live name/email readback to account setup inspection. Original
provisioning intent stays immutable; check/resume preserves operator profile edits. This
does not enable resident new-account delivery or open production admission.
The module retains the 0.6.0 scoped HTTPS inspection/revocation endpoint with a separate protected
target bearer credential. It verifies both account and assignment receipts; cookies and
native API credentials confer no authority. The Go HTTPS adapter passes actual KyIdentity
and signed-provider fixtures, including partial-publication repair, retained content and
old-session/token denial. Wrong credentials, unowned/binding-invalid accounts, oversized
input and insecure credential permissions are refused; the scoped token cannot access
content APIs. Target-local durable queue and authenticated app-scoped SCIM receiver components
now exercise this native adapter after actual retry backoff: pending remains 429/active until
native repair proves 204/inactive. The actual KyIdentity provider fixture still verifies
retained web/API/fresh-login denial and content. Default fixture mode does not deliver that revocation through
KyIdentity's SCIM engine; the separate producer mode above qualifies assignment loss. The runtime service now loads protected private files and joins TLS/
worker shutdown; its real-product fixture preserves backoff and native completion without a
further mutation request. This does not qualify installer deployment or connector composition.
The protected runtime now includes an ordered kyPulse audit relay. Its native receiver
fixture proves lost-response/restart delivery through the actual executable without
duplicate log/activity rows. Native failure Alerts and restartable webhook delivery have
isolated SQLite/PostgreSQL
qualification; the operational runtime requires a target-bound admin source grant. Active
profile provisioning, resident service/audit deployment and full connector qualification
remain open.
Central offboarding and ordinary-workflow separation composition,
restore, CLI deployment orchestration and release provenance remain open; the module does
not admit BookStack yet. Native assignment/account/login settings and the persistent private
fence directory and scoped bridge credential are required backup artifacts.

LibreChat qualification, 2026-10-10: v0.8.8 commit
`e8f3be08623663d4ad7f7241e693c94469b63bb0` provides native OIDC issuer/subject storage and
claim-based administrator assignment. Its [admin user routes](https://github.com/danny-avila/LibreChat/blob/e8f3be08623663d4ad7f7241e693c94469b63bb0/api/server/routes/admin/users.js)
register list/search only; the [list response](https://github.com/danny-avila/LibreChat/blob/e8f3be08623663d4ad7f7241e693c94469b63bb0/packages/api/src/admin/users.ts)
omits the OIDC issuer/subject. Email or a listed provider is insufficient ownership proof.
The [native ban operation](https://github.com/danny-avila/LibreChat/blob/e8f3be08623663d4ad7f7241e693c94469b63bb0/api/cache/banViolation.js)
removes refresh sessions, but [ban middleware](https://github.com/danny-avila/LibreChat/blob/e8f3be08623663d4ad7f7241e693c94469b63bb0/api/server/middleware/checkBan.js)
admits requests after expiry or when enforcement is disabled. Its
[JWT strategy](https://github.com/danny-avila/LibreChat/blob/e8f3be08623663d4ad7f7241e693c94469b63bb0/api/strategies/jwtStrategy.js)
still resolves a retained native user independently of deleted refresh sessions.
The pinned runnable prerequisite fixture executes these modules unchanged with substituted
storage/dependency boundaries and an injected clock. It proves this gap, not full HTTP,
MongoDB, signature validation or every token path. Do not implement offboarding by temporary
ban or destructive account deletion. Build product-owned source-bound inspection and persistent
retained revocation with native authorization hooks, then qualify actual setup/OIDC/session/API
denial and content retention before admitting LibreChat. Dedicated administrator workflow
separation, lifecycle, backup/restore and deployment remain additional gates.

Community module 0.1.0 now supplies a read-only container-local account inspection command
and a bounded Go SDK. It pins native ObjectId/provider/configured issuer/stored subject and
reports only identity digest and native ADMIN/USER role. Private input files, nonmutating
capability discovery, strict report pins, unknown/foreign refusals and shell staging/cleanup
have runnable unit checks. Those checks substitute the native model boundary; they do not
qualify installed native packages or MongoDB/container execution. Native role readback alone
does not prove owner authorization or administrator workflow separation. No initial setup,
revocation, resident connector or deployable catalog entry is enabled by this interface.

Native inspection qualification, 2026-10-10: the disposable fixture now runs the pinned built
native data-provider/data-schemas and module CLI against MongoDB 8.0.20, then invokes the Go SDK
through actual container exec. Ordinary/admin identity inspection, foreign/local/missing refusal,
repeat and private request cleanup pass. Full collection/index/document snapshots remain equal.
The CLI registers schemas explicitly and uses native shared methods without the broad API
loader's cache initialization; native logs use an isolated fixture directory. This supersedes
the earlier missing MongoDB transport evidence, while signed OIDC, actual administrator
authorization/separation, initial setup, retained revocation and production gates remain open.

An internal native revocation primitive now publishes an account/subject-bound record and
local publication audit atomically on the retained User, then removes native sessions and
agent API keys. Real MongoDB checks prove lost-reply inspection/repeat, duplicate/corrupt
identity refusal and preserved user/profile/role/chat content. Its proof reports physical
record/credential state only. Candidate source hooks now cover JWT acceptance, OIDC
subject/account handling, token issuance, agent user resolution and optional cookie file
authentication. The patched native Passport JWT strategy is qualified with actual native
models, MongoDB and cryptography: valid tokens work before publication, invalid signatures
and old/fresh valid tokens after publication refuse, and another account keeps access.
Native session/JWT issuance and refresh-cookie middleware are also qualified against native
models and isolated Express HTTP: publication denies cookie identity before cleanup and
new issuance without changing sessions, while another account retains access. Native
ObjectId/string IDs work before publication. This is strategy/middleware qualification,
not the full LibreChat/OIDC server, actual share ACL or complete authorization coverage.
Remote-agent API-key authentication is also qualified through the patched native factory and
actual generated native keys/hashes over isolated HTTP: publication denies a retained valid
key before cleanup and afterward while a control account keeps access. Explicit fixture
configuration enables API keys and disables OIDC; this does not qualify agent authorization,
configuration delivery or strict multi-tenant operation.
Signed remote OIDC/M2M middleware now uses actual native HTTPS JWKS retrieval and RS256
verification with seeded native tenant-bound users and explicit client configuration.
Publication denies old/fresh signed user/machine tokens while control accounts keep access;
signature, expiry, issuer/audience/scope and client/subject mismatches refuse. The isolated
worker trusts only its owned fixture CA; TLS checks are preserved and fixture keys removed.
This does not prove KyIdentity browser login, production config delivery or full authorization.
An optional actual KyIdentity fixture now qualifies HTTPS discovery, assigned authorization,
PKCE and the native LibreChat browser callback. Native ordinary registration and signed app
ADMIN role projection work; fresh revoked-subject login refuses without User/session changes
and control login remains accepted. This uses explicit fixture configuration, native Keyv
memory cache and unused refusing avatar/email boundaries; it does not prove all native
routes, administrator capabilities/purpose, initial setup ownership or release provenance.
Candidate source hooks now also cover common Passport success boundaries (including RUM)
and secure image session lookup without hiding native owner records. Secure image cookie
and common bearer HTTP denial before cleanup are qualified with control account access.
RUM/provider reuse, legacy image reuse, full shared-image ACL, other alternate browser paths,
complete native writer coordination and restart/restore remain unqualified, so the
public CLI stays inspection-only and offboarding admission remains gated.

Candidate User model hooks now guard native query mutations, replacement/deletion, document
saves and bulk operations with an atomic record-absence condition; native writers cannot
supply/rename the publication field. Publication refuses an existing deletion fence and
removes expiry. Actual native methods retain the exact recorded User under these attempts,
including a document loaded before publication, while active control writes/browser creation
work. Raw maintenance/restore, concurrent new-account/source races, credential creation and
full deletion-controller orchestration remain unqualified; admission stays gated.

Native session/key writers now claim bounded private per-write admission on the retained
User; publication and User mutations require no claims. This keeps source/fence/admission
atomic on standalone MongoDB. Actual inserts held after MongoDB insertion block publication
until release; new native creation after publication refuses before insertion. Failed
insertion releases, unknown claim/release replies stay inspectable and corrupt state refuses.
Physical cleanup also requires zero claims. Internal confirmed-offline recovery now uses
exact claim-map/source pins and one atomic claim-clear/audit receipt. Owned killed/restarted
native Node writer fixtures cover stale state, lost replies, newer claims, bounded/corrupt
history and data retention. No public maintenance command or replica-stop authority is
qualified. Native provider session upsert/repeat, refresh rotation/expiry and API-key last-used writes
now additionally pass real MongoDB qualification. Exact document/query owner pins prevent
late-match cross-owner mutation and existing-row writes cannot recreate a credential deleted
by concurrent native logout. Multi-row/batch writes refuse. Complete native controllers,
restore, privileged raw writers and new-account/source races remain unqualified;
LibreChat admission stays gated.

Actual native local-session AuthController refresh is also qualified over owned HTTP/MongoDB
with patched native issuance. Follow rotated cookies and verify the retained session exists
before publication-denial checks. Published revocation yields 403 without cookies or native
User/session mutation before cleanup; afterward access stays denied and control refresh
works. Explicit fixture tenant context and refusing unused provider/Graph boundaries limit
this proof: provider-token reuse/bridges, full server routes, setup ownership and administrator
purpose remain gates.

Vaultwarden qualification, 2026-10-09: inspected upstream commit
`df2cd3c8693782eeedfd1f70318f6f14033213a8`. [Native configuration](https://github.com/dani-garcia/vaultwarden/blob/df2cd3c8693782eeedfd1f70318f6f14033213a8/src/config.rs)
supports OIDC, SSO-only login and PKCE. Disable email-based account association for new
suite setups. [Instance administration](https://github.com/dani-garcia/vaultwarden/blob/df2cd3c8693782eeedfd1f70318f6f14033213a8/src/api/admin.rs)
checks its own admin cookie/token; disable resets the user's security stamp, marks the
account inactive and removes devices. These supported APIs are adapter inputs, but do not
prove dedicated KyIdentity administration or denial of personal vault use. Keep admission
gated until a native integration and actual owner/everyday/offboarding tests prove those
contracts. Do not substitute an auth proxy or the excluded KeePass KyVault.

- Unattended OIDC configuration for Dolibarr (`llx_const`) and BookStack role mapping by
  External Authentication ID, end to end.
- Each app's behavior on KyIdentity back-channel logout, and which client sessions survive.
- Official `linux/amd64` manifests for Forgejo (codeberg.org registry) and BookStack.
- Runtime dependencies per app (database engine, cache, search) and Home Assistant's network
  needs for device discovery on a Docker host.
- Holm upstream's willingness to accept a logout/session-recheck change.
- Backup commands for every app except Immich, and each app's documented restore steps.
- K8up restore flow for streamed dumps (Ky products), and rest-server append-only behavior under K8up.
- One frp client carrying many hostnames on a shared 443, and real client IP reaching NPM
  (PROXY protocol).

Research sources: official docs, repositories, LICENSE files and release APIs, read 2026-10-06.
Per-app URLs were not persisted; re-cite them when each adapter is specified.
