**Repo:** KyQuickStart and Ky suite server repositories
**Worktree:** /home/yoshi/git/busnes.app/KyQuickStart (branch feat/phase-4-edge)

# Deployment plan: every product into the catalog — 2026-10-10

> Roadmap-level plan. Each milestone gets its own detailed plan before execution. Gates are
> defined in `2026-10-06-kyquickstart-roadmap.md` (G1–G11) and the catalog spec's admission
> rules; a gate opens only when the capability ships, with tests, in a **tagged release**.
> No duration estimate has been established.

## Where we are

- Production catalog (`internal/catalog/apps/`) is empty. Only the disposable demo `hello`
  installs. `integrate-product` connects 13 running products; only Identity → Notes has
  composed acceptance.
- Every product's installer work is **uncommitted** on `*-installer-setup` worktrees, and the
  shared `applysetup` package is unpublished (`ky-primitives` still v0.9.0). No product can
  build from released dependencies, so no gate can open.
- Catalog manifest code (`catalog.go`) lacks the spec's OIDC, backup, upgrade, ports/env and
  release-set fields; strict decoding would reject them.

## Milestones (critical path first)

### M0 — Land the foundations (blocks everything)

1. Commit, PR and land KyQuickStart `feat/phase-4-edge` (edge, offboard, adapters, review fixes).
2. Land `ky-primitives` `applysetup` + keyfile changes; tag a release (v0.10.0).
3. Bump each product's `go.mod` to that tag; land each `*-installer-setup` branch one product
   at a time, CI green, tagged release. Order: Identity, Notes, Recovery, Pulse, Drive, then
   the rest.
4. Extend the catalog manifest schema (OIDC client, backup declaration, upgrade rules, ports,
   env, release-set ID) with strict validation and tests.
5. Rerun deferred proofs on the landed code: `make e2e` (Docker), restricted kind suite,
   shared-module race/macOS/vulnerability jobs.

Exit: all repos on released tags; installer CI and e2e green from landed code.

### M1 — Shared infrastructure on the pilot

Ordering from the installer spec: edge → KyIdentity → KyRecovery → kyPulse → KyYard.

1. **Edge:** Kubernetes connector deployment (decision 4); fix review items (connector IP vs IPAM `IPRange`, per-installation NPM
   ownership ID, disable removed routes); G11 forwarded-header self-check per product.
2. **KyIdentity (G1, G2):** catalog entry; `bootstrap-admin` + staged `apply-setup`; prove G2
   SCIM `active:false` on disable/end date/lost assignment. Fix owner lookup by verified ID.
3. **KyRecovery (G3, G10):** catalog entry; human custodian ceremony as a parked gate;
   unattended `pair generate --service`; backup status/restore-start UI.
4. **kyPulse (G4):** catalog entry; preparation that preserves and validates the `monitoring`
   extension; target/log-source registration.
5. **rest-server** on the KyRecovery host (append-only, private repos, TLS, separate prune
   credential; preflight refuses NAS storage).

Exit: `verify` passes for each: SSO as admin and everyday identity, admin refused to
everyday, health in kyPulse, backup to KyRecovery and restic, restore drill.

### M2 — First user-facing products: Notes, then Drive

1. **KyNotes:** catalog entry; browser UI + owner MFA sign-on; Kubernetes sign-on; make SSO
   save and audit one transaction.
2. **KyDrive:** wire typed preparation into the installer; receiver-owned SCIM token
   generation and reverse transfer to Identity's connector; owner/everyday composition;
   rebuild `web/dist`; S3/R2 bulk data backup (decision 3); recovery pairing (replacing today's refusal); keep refusal reasons;
   dedicated-admin check against the current role.
3. Pilot origins `kydrive.urlxl.us`, `office.urlxl.us`; Euro-Office source/build provenance.

Exit: per-app `verify` on the pilot, including the bulk-backup gate (the pilot deferral
must be closed or explicitly waived).

### M3 — Remaining Ky products (one at a time, same pattern as Drive)

KyForge and KyMark are canceled (2026-10-10), dropped from the plan, and deleted locally.

For each: secret-free preparation, credential composition from native records, recovery
pairing (G7), kyPulse registration, edge G11, catalog entry, `verify`.

| Product | Unique work |
|---|---|
| KyPost | Runs as `kypost` (Docker only); external mail and IMAP admin separation |
| KyDNS | Backup enrollment |
| KyMessage | Matrix provisioning; MAS assignment exclusion |
| KyCalendar | Real-device interop; release provenance; VTODO task sync (iOS Reminders, Android) moved into scope |
| KyYard (G5, G6) | Enrollment honoring locks; shared upgrade module; tenant/endpoint composition |

### M4 — Third-party apps (Docker hosts; one detailed plan each)

Each needs: admission evidence resolved, pinned digest, installer setup dispatch, admin grant,
deprovision adapter, backup declaration, kyPulse probe, adapter harness pass.

| App | Status / next |
|---|---|
| Immich | Closest: registered adapter. Move test-only exports into tests; writer coordination. Pre-existing admin is an accepted risk |
| Forgejo | Production org setup, group audit/restore, uncertain-write reconciliation |
| BookStack | Retire default `admin@admin.com`; role allowlist; non-locking middleware; build/pin image |
| LibreChat | Product-owned setup; persistent revocation; keep `OPENID_REUSE_TOKENS` off |
| Home Assistant | No adapter yet; community add-on labelled |
| Dolibarr | No adapter yet; verify `llx_const` OIDC config; admin grant via API |
| Zammad | Candidate: public PKCE client in KyIdentity; subject-bound admin grant; admin reset fallback |
| Vaultwarden | Selected. OIDC SSO with `SSO_ONLY`, `SSO_SIGNUPS_MATCH_EMAIL=false`; hashed admin token in the administrator handover (accepted risk); prove offboarding |
| Holm | Blocked: G8 back-channel logout or session recheck upstream/fork |
| Paperless-ngx | Approved candidate: verify superuser group sync release; prove disable ends sessions/tokens |
| Mealie | Approved candidate: admin from `roles` claim; prove offboarding |
| Linkding | Approved candidate (bookmarks, replaces KyMark): OIDC links by email; installer pre-creates owner superuser |
| Actual Budget | Approved candidate (personal finance; FlyBudget not adopted): owner-first login race; experimental OIDC multi-user; prove offboarding |
| Task management | Closed (Yoshi, 2026-10-10): KyNotes checkbox tasks, KyCalendar VTODO, Forgejo issues/boards, Zammad tickets |

Contacts are covered by KyPost (CardDAV with Android and iOS sync); no separate app.

### M5 — Cross-cutting completion

1. Offboarding bridge (`kyoffboard`) resident deployment, network policy, kyPulse alerts;
   close offboard review lows.
2. Fresh guided inputs, owner MFA, printable handover, `uninstall`/`unmanage`.
3. Upgrades (Phase 10): shared module, pre-upgrade capsule + snapshot, KyYard action.
4. Full-stack pilot acceptance on `unraid.urlxl.us` / `hluswcdata01` /
   `kyrecovery.urlxl.us`, preserving existing services, credentials, KyVault and Vikunja data.

## Decisions (Yoshi, 2026-10-10)

1. **Who configures products:** delegated. Catalog-installed products are configured by
   `apply` as ordinary Inspect/Apply/Verify steps (one command, per the installer spec).
   `integrate-stack` stays for existing products installed outside the catalog.
2. **Installer spec:** approved.
3. **KyDrive backups:** Drive needs bulk data backup outside KyRecovery to S3-compatible
   object storage (AWS S3 or Cloudflare R2). The rest-server path remains for other apps.
   Open: restic's S3 backend has no append-only mode, so ransomware protection must come
   from object lock/retention or a write-only credential; verify R2 support before M2.
4. **Edge on Kubernetes:** required. Kubernetes connector deployment (cloudflared, NPM, frp
   client) moves into M1.
5. **Password app:** Vaultwarden. Its token-only admin page is an accepted risk. The admin
   token is generated on the target, stored in Vaultwarden as an Argon2 PHC hash, and
   delivered once as part of the dedicated administrator's credential set at handover;
   it never enters workstation state. See "Password app" below.
6. **Zammad administrator password reset:** accepted risk.
7. **KyVault** is being replaced; dropped from G11.

## Password app (Vaultwarden selected)

Vaultwarden has upstream OIDC SSO (`SSO_ENABLED`, `SSO_ONLY`, PKCE; first release believed
1.35.0, unverified). Users still need a master password: SSO replaces login, not vault
encryption. Remaining lift: set `SSO_SIGNUPS_MATCH_EMAIL=false` (default `true` links by
email); the admin portal is still `ADMIN_TOKEN`-only, so the dedicated administrator needs
either an accepted token-held-by-installer design or an upstream change; offboarding via
native disable/deauth must be proven. Alternative: Psono (Apache-2.0) offers OIDC free up
to 10 users in its Enterprise Edition, which fails admission rule 1 (SSO outside the free
open-source edition). Passbolt CE has no SSO.

## Safety

Live pilot work needs per-deployment authorization; the 2026-10-04 pilot authorization
covers the KyDrive pilot only. Preserve foreign containers and data; never adopt foreign
accounts; build/test serially under the memory limits in the adapters handoff.
