# Third-party app catalog

Date: 2026-10-06. Status: proposed, awaiting review.

KyQuickStart installs the Ky suite plus a small catalog of third-party apps. This spec decides
which third-party apps are in the first catalog, the rules any future app must pass, and the
per-app integration work the installer owns. Installer architecture is a separate spec.

## Decisions

- One app per category. Alternatives may be added later as separate entries.
- Every app signs in through KyIdentity with native OIDC. No auth-proxy gate is built.
- Monitoring is not a catalog category: kyPulse covers health, alerts and logs. The installer
  registers every installed app as a kyPulse target instead.
- Third-party backups are app-aware: K8up runs each app's own dump command and backs up its
  volumes into an encrypted restic repository on an append-only rest-server off the NAS. The
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
| Forgejo (v16 / LTS v15) | Git hosting, Actions CI | GPL-3.0-or-later | `forgejo admin auth add-oauth --group-claim-name roles --admin-group … --required-claim-*` | Disable user, delete access tokens via API | No back-channel logout; Git over HTTPS uses personal tokens |
| BookStack (v26.09) | Wiki | MIT | `OIDC_USER_TO_GROUPS`, `OIDC_GROUPS_CLAIM=roles`, Admin role External Authentication ID `admin` | Disable user via API | No official image: build or pin one |
| Vikunja (v2.7) | Kanban, tasks | AGPL-3.0 | Installer runs `vikunja user set-admin` after first admin login | Disable user, revoke tokens via CLI/API | Team sync needs a `vikunja_groups` object claim KyIdentity lacks; teams managed in-app. Desktop client needs a `127.0.0.1` redirect |
| Immich (v3.2) | Photos | AGPL-3.0 | `roleClaim` → `admin`/`user` | Back-channel logout; delete API keys | Config via `IMMICH_CONFIG_FILE`. Mobile redirect `app.immich:///oauth-callback` or HTTPS override |
| Home Assistant + hass-oidc-auth (2026.9 / v1.2.1) | Home automation | Apache-2.0 / MIT | `roles.admin` from claim | Revoke refresh and long-lived tokens via API | Add-on is community-maintained: label it |
| Dolibarr (v24.0) | CRM, invoicing, ERP | GPL-3.0 | Installer sets admin flag via API | Disable user, revoke API key | Admin not claim-driven |
| LibreChat (v0.8.8) | AI chat | MIT | `OPENID_ADMIN_ROLE`, `OPENID_REQUIRED_ROLE` | Disable user, revoke API keys | Keep `OPENID_REUSE_TOKENS` off (needs refresh tokens) |

## Installer-owned integration work

1. **OIDC client per app** in KyIdentity: exact redirect and logout URLs, back-channel logout
   URL where supported, app role names (`admin`, `user`, plus app-specific names such as
   Immich's), *Assigned users only*, the admin identity mapped to `admin`, the everyday identity
   to `user`.
2. **Admin grant step** for Vikunja and Dolibarr, run once and verified after first login.
3. **Deprovision adapter** per app, triggered when KyIdentity ends a user's access; verified by
   disabling a test user and confirming sessions and tokens fail.
4. **kyPulse registration** of each app's health endpoint and log source.
5. **Backup declaration** per app: K8up backup command annotation, volumes to include and
   exclude, secrets for the sealed capsule, and restore order (see Backups).

## Backups

Two paths, split by size and sensitivity.

**Sealed capsules (small, secret).** App configuration, OIDC client secrets, app encryption keys
and the restic repository password are sealed with `ky-primitives/recoveryclient` and delivered
to KyRecovery and the local backup directory under the suite KyRecovery contract. Restoring
anything, bulk included, therefore needs k-of-n custodians.

**restic via K8up (bulk, app-aware).** Capsule caps (384 MiB per container) rule out bulk data,
so:

- **Engine:** restic (BSD-2-Clause) with client-side encryption; the target sees ciphertext only.
- **Kubernetes:** K8up (Apache-2.0, restic-based operator; 4.10.0, 2026-07-17). The installer
  writes a `Schedule` per app and a `k8up.io/backupcommand` annotation on the app pod; K8up runs
  the command in the pod and streams its output into restic, then backs up the app's volumes.
  Scheduled `check` runs verify the repository.
- **Outside Kubernetes** (KyDrive on the NAS): plain restic on a timer against the same
  repository.
- **Target:** rest-server (BSD-2-Clause) with `--append-only --private-repos` and TLS, on a host
  the NAS holds no credentials for. Backup clients get append-only credentials; `forget`/`prune`
  runs from a separate admin credential with time-based retention (`--keep-within`). A
  rest-server on the NAS is a NAS-local copy and must not be described as independent.
- **Installer input:** the off-NAS backup host is required when any app with bulk data is
  selected.
- **Status:** backup and check results are reported to kyPulse.

| App | Backup command (streamed to restic) | Volumes | Skip |
|---|---|---|---|
| Immich | built-in daily dump in `UPLOAD_LOCATION/backups` (verified) | `library`, `upload`, `profile`, `backups` | `thumbs`, `encoded-video` |
| Forgejo | `pg_dump` | repositories, LFS | caches |
| BookStack | `mariadb-dump` | uploads | |
| Vikunja | `vikunja dump` | attachments | |
| Home Assistant | built-in backups | `/config` | |
| Dolibarr | `mariadb-dump` | `documents` | |
| LibreChat | `mongodump` | uploads | |
| Holm | SQLite `.backup` | `config.yml` | |

Restore order per app: database first, then files (Immich's documented order).

Rejected engines: Kopia (repository server holds the key), Velero (one shared static key),
Duplicati (proprietary parts, paid SSO), Borg 2 (beta, drops append-only), MinIO (archived).
Garage lacks Object Lock; SeaweedFS is the only checked S3 target with Object Lock if an S3 tier
is added later.

## Edge and reverse proxy

Infrastructure, not catalog apps; the admission rules do not apply, but these do:

- **Reverse proxy: Nginx Proxy Manager** (MIT). If one exists, the installer adds and enables
  only its own hosts through NPM's API (create disabled, verify, enable) and never edits other
  hosts or NPM's database. Otherwise it installs NPM. TLS terminates at NPM on the LAN.
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
| WeKan, Kanboard, Leantime | Vikunja selected; Kanboard/Leantime cannot map admin |
| Wiki.js | BookStack selected; settings live in DB, 3.0 still beta |
| PhotoPrism | User roles and user management are paid; no official mobile app |
| Jellyfin | Deferred: no native OIDC; single-maintainer alpha SSO plugin; revisit when core SSO ships |
| Plex | Proprietary, plex.tv accounts only |
| Frigate, Firefly III | Proxy header auth only |
| Open WebUI | Non-OSI license |
| Twenty | SSO code enterprise-licensed |
| Invoice Ninja, Akaunting | Non-OSI licenses |
| EspoCRM | Passes; Dolibarr covers CRM and invoicing in one app |
| Actual Budget, Audiobookshelf | Second app in a category |
| Video calls (Jitsi, Element Call) | Out of scope |
| Joplin Server | Personal Use License forbids business use; SAML/LDAP only |
| LimeSurvey | No OIDC in core; plugins give one fixed role, no user-disable API. Forms are out of scope |
| Formbricks, OpnForm | OIDC in enterprise-licensed code |
| salt.md | Google/Microsoft sign-in only: no generic OIDC, no role sync, no deactivation |
| OneNote replacements: AFFiNE, AppFlowy, Anytype | Non-OSI server/client license or seat-capped free tier |
| OneNote replacements: SiYuan, Trilium | Single-user; OIDC only gates the instance |
| OneNote replacements: Logseq | Self-hosted sync is beta and accepts Cognito tokens only; revisit when stable |

## Unverified at plan time

- Unattended OIDC configuration for Dolibarr (`llx_const`) and BookStack role mapping by
  External Authentication ID, end to end.
- Each app's behavior on KyIdentity back-channel logout, and which client sessions survive.
- Official `linux/amd64` manifests for Forgejo (codeberg.org registry) and BookStack.
- Runtime dependencies per app (database engine, cache, search) and Home Assistant's network
  needs for device discovery inside Kubernetes.
- Holm upstream's willingness to accept a logout/session-recheck change.
- Backup commands for every app except Immich, and each app's documented restore steps.
- K8up restore flow for streamed dumps, and rest-server append-only behavior under K8up.
- One frp client carrying many hostnames on a shared 443, and real client IP reaching NPM
  (PROXY protocol).

Research sources: official docs, repositories, LICENSE files and release APIs, read 2026-10-06.
Per-app URLs were not persisted; re-cite them when each adapter is specified.
