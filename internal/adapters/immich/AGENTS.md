# Immich adapter

## Purpose

Verify exact native OIDC ownership, administrator projection and content-preserving revocation and reactivation.

## Ownership

- `client.go`: bounded native HTTPS transport, version/provider/account inspection and verified
  grants. `New` requires a verified HTTPS origin; `NewInternal` takes a Compose service origin
  (`InternalOrigin`: one lowercase DNS label, optional port, no IP/localhost/path), plain HTTP
  allowed, for the bridge in Immich's own project.
- `reactivate.go`: `Reactivate`, the bridge's native reactivation of a retained account.
- `provider.go`: preflight fingerprints, native OIDC configuration and verified readback; protected intent stays on target.
- `access.go`: installation-bound module proof and permanent revocation with uncertain-outcome refusal.
- `directory.go`: owned current profile/revision inspection and verified atomic native name/email/administrator projection.
- `creation.go`: protected native subject-absence proof and ordinary creation with verified immutable UUID/profile receipts.
- `module/`: pinned native apply-setup/owned account and service-administrator receipts,
  revocation/reactivation source extension and the catalog server overlay `Dockerfile`.
- `native_test.go`: owned digest-pinned service/database/cache, native signed login/grant/logout verification.
- `native_setup_test.go`: protected bundle, blank-installation refusal, read-only check, phone redirect override,
  owner/everyday/service-administrator creation, private service key reuse/no second key and repeat.
- `native_reactivation_test.go`: module reactivation refusals, epoch idempotence, dead old credentials, fresh sign-in and re-revocation.
- `native_directory_test.go`: profile/role transactions, setup compatibility, stale/foreign refusal and existing-session authorization.
- `native_creation_test.go`: atomic native creation, collision rollback, repeat, independent role projection and retained revocation.
- `native_installer_test.go`: actual saved integrate-stack over SSH/Docker with target locking and host-key pinning.
- `native_provider_test.go`: synthetic RSA/PKCE provider or scoped TLS proxy on the fixture Docker network.
- `native_identity_test.go`: actual KyIdentity binary, owned setup/subject receipts and TLS/PKCE authorization.
- `native_producer_test.go`: real identity mutation/acknowledgment checks and an owned pinned SSH TCP relay for isolated resident TLS qualification.
- `native_multi_test.go`: optional pinned native Forgejo fixture, separate identity connector and cross-product access/content probes.
- `native_producer_creation_test.go`: actual identity new assignment, owned native UUID receipt, PKCE sign-in and retained-account revocation.
- The parent owns admission and the root owns deployment, credentials and durable receipts.

## Local Contracts

- Source/API shapes are pinned to Immich v3.2.0, commit `1b6098c9dbfffe978bec2d414606ed7a4c8e019a`.
- Require target-owned UUID, issuer, client ID and OIDC subject receipts. Email never authorizes adoption.
- Recheck enabled native OAuth configuration with insecure requests disabled and exact identity before mutation; standalone grants change only `isAdmin`.
- Missing identity/activity/administrator fields, provider drift and deleted accounts refuse mutation,
  except revocation: it requires only the exact native UUID/subject and module fence binding, so a
  disabled/rotated provider or inactive/soft-deleted account can still be fenced. The module binds
  that revocation to its stored fence or directory receipt; the creation fixture qualifies it.
- Unknown writes or failed verification return a reconciliation error; never claim successful access from a PUT alone.
- Native DELETE schedules content deletion and is not offboarding. No deletion or restore call is implemented.
- Native signed back-channel logout revokes OAuth sessions but preserves personal API keys;
  it does not satisfy complete offboarding. Never substitute account deletion for revocation.
- The community module adds transactional session/key removal, a metadata fence (write-once
  per epoch, cleared only by its epoch-bound reactivation route), subject denial and socket disconnection. Require `ky.immich-access/1`, exact installation
  ownership, explicit nonnegative counts and valid revocation time; failed writes/readback
  require reconciliation. Stock servers cannot use this revocation path.
- Provider configuration requires a protected full-configuration preflight fingerprint that
  excludes `oauth.clientSecret` and `notifications.smtp.transport.password`;
  configure only a blank provider or repeat an exact match. Refuse migration and preflight
  drift; preserve every unrelated field and hold unknown writes/readback as uncertain.
  Upstream PUT has no conditional update: callers must serialize every configuration writer.
  Fingerprints prove configuration state, not installation ownership. Setup, from
  `integrate-product` or apply's `immich.setup`, runs under the target lock.
- `Reactivate` takes an even epoch (≥2) and the PUT's profile. It posts the module route only
  over a fence; without one it accepts only this epoch's earlier receipt. Success needs no
  fence, this epoch's receipt, zero sessions/keys after its own post and the directory profile
  read back (a later PUT's profile is applied by directory update). A 409 is `ErrRefused`;
  every other failure is `ErrUncertain`.
- Native directory projection requires `ky.immich-directory/1`, exact initial setup/native UUID/provider/subject
  and explicit current profile, revision and revocation fields. Compare-and-set updates touch only
  name/email/administrator plus the owned receipt in one native transaction. Refuse missing receipts,
  foreign/stale writes, unreceipted edits and revoked accounts. Repeats preserve revisions;
  uncertain results require reconciliation. Stock and older unreceipted modules are refused.
- Admission gates (offboarding, account lifecycle, restore) close only in `make e2e`
  (`TestKubernetesImmich`); these fixtures do not close them. Administrator separation is a
  documented residual (H2): Immich lets its administrator keep photos, and the handover points
  photos to the everyday account.
- Installer-owned mode requires a provisioned account and directory receipt before native
  OAuth login; it refuses first-login creation, email adoption and OAuth link/unlink changes.
  Other native configuration/account writers still require deployment coordination.
- Native account creation requires exact installation/provider/subject and explicit ordinary
  profile. Never create from an email/username match or accept administrator authority in the
  initial creation. Require product-owned absence, immutable creation receipt, revision-zero
  profile readback and access inspection before returning a UUID. Any unverified POST/write
  outcome requires reconciliation. Definite native 400/403/409 refusals remain conflicts;
  transport/server failures hold uncertainty. Protected bridge creation is a separate per-app
  opt-in and retains its own creation/initialization audit proofs before acknowledgment.

## Work Guidance

Use native APIs; preserve existing accounts and content. Never return server bodies or credentials in errors.

Keep source checkouts, compiler scratch and fixture logs on disk; this workstation's `/tmp`
is RAM-backed. Run native qualification serially with `GOMAXPROCS=2`, `GOMEMLIMIT=1GiB`,
`GOFLAGS=-p=1` and a disk-backed `TMPDIR`. The Go memory limit is a soft runtime target;
the fixture separately caps container memory (server 2 GiB, database 768 MiB, provider
256 MiB, cache 128 MiB) and disables container swap growth.

## Verification

- `go test -race ./internal/adapters/immich`: verified TLS transport and identity/authority refusal before writes.
  `reactivate_test.go`: fence/epoch/profile verification and `InternalOrigin`;
  `workflow_test.go`: `source.patch.sha256`, the overlay `Dockerfile` base pin and
  `immich-image.yml`'s trigger, permissions, pins, push and attestation identity.
- HTTP fixtures qualify boundaries, not native Immich behavior or session/token denial.
- `KYQ_TEST_IMMICH_NATIVE=1 go test -race -count=1 -run '^TestNativeProviderAccountAndAdministrator$' -v ./internal/adapters/immich`:
  pinned v3.2.0 amd64 service, official pinned PostgreSQL/Valkey dependencies, fresh storage,
  private labeled Docker network and loopback-only host port. Compile a static test provider
  into its own unexposed container; TLS material stays in protected disposable files.
  Verify signed native owner/everyday login, exact provider/account receipts, grant/repeat/
  demotion, foreign-subject refusal, old-session denial after signed back-channel logout,
  retained personal API-key access and unrelated owner access. Cleanup errors fail the test.
  With `KYQ_TEST_IMMICH_MODULE_IMAGE` set to an immutable local overlay digest, verify its
  source/patch labels, upload an owned PNG, establish a subscribed socket, and verify scoped
  session/key/socket denial, new signed login refusal, permanent repeat, retained profile,
  native photo counts and original bytes. Native endpoints refuse foreign subjects/installations,
  self-revocation and session-only authority. Wait for database/cache readiness before startup.
  The default provider is synthetic; this mode does not qualify actual KyIdentity or ordinary-admin workflow
  denial, restore or deployment. No account DELETE is called.
  `KYQ_TEST_IMMICH_BRIDGE=1` requires the module image (which itself requires `KYQ_TEST_IMMICH_SETUP=1`) and drives the actual resident
  target/SCIM/worker through one unavailable write and durable backoff using a test helper
  from `internal/offboard`, with bounded stdin credentials. Reopened UUID/client receipts
  and completion/audit proofs must survive restart. Maintain socket heartbeat during retry
  so observed disconnection cannot be an idle timeout. This controlled sender does not qualify actual KyIdentity event delivery.
- `KYQ_TEST_IMMICH_SETUP=1` additionally requires the module image: invoke native apply-setup
  with a target-local bundle on a fresh module server (no bootstrap: its first-run sign-up and
  pre-setup restore must answer 4xx); verify missing-resume refusal, refusal (plan included)
  while a `psql`-planted administrator exists, then delete it so setup owns a blank installation. Verify read-only check, invalid phone-URI/key-path refusal, the stored
  mobile override, owner/everyday/service creation with exactly three native users, reuse of
  the refused run's pre-commit 0600 key file, the service key's bounded permissions, service
  password-login refusal, refused service-account updates and owner password/PIN, one key across repeats, missing/changed-file refusal, UUID/role
  receipt stability, foreign installation/provider refusal and private-file/symlink/parent checks.
  The owner's own API key then drives the fixture and bridges get the service key.
  After revocation, setup resume must fail and retain the exact fence/time/zero credentials.
  Without producer mode, reactivation then deletes a session and API key planted in the
  database while fenced (both 401, counted in the receipt), refuses bad epochs/fields/bindings, session-only
  authority, self, a never-revoked account and email collisions; reactivates at epoch 2 with
  administrator from the profile (synthetic `daily-admin` code, or KyIdentity's roles); repeats
  idempotently; keeps the old session, key and socket subscription dead; admits a fresh
  sign-in on the same UUID; and re-revokes with a new fence before epoch 4.
  Signed native login must use those created IDs. This is initial setup, not an ongoing
  directory connector or dedicated-administrator ordinary-workflow qualification.
- `KYQ_TEST_IMMICH_INSTALLER=1` additionally requires setup qualification and an immutable
  `KYQ_TEST_IMMICH_SSH_IMAGE` built from the repository's existing e2e SSH fixture. A capped,
  owned loopback-only sidecar exposes the Docker socket to the test SSH target; private
  keys/agent stay outside workstation state. Actual integrate-stack creates resources,
  refuses unconfigured inspection and a contended target lock, checks/repeats with pinned
  host keys, releases its target lock and renders only sanitized
  reports. Native login/revocation/photo checks continue afterward. No live target is used.
- `KYQ_TEST_IMMICH_IDENTITY_BINARY=<absolute-static-binary>` requires native setup and
  substitutes an actual disposable KyIdentity instance for the synthetic signer. It runs
  on the owned network with 512 MiB memory/no swap growth, one CPU and an exact trusted
  TLS-proxy /32. Only the TLS proxy is published on loopback; identity has no host port.
  Verify owned user/client/role setup and check/repeat, Assigned users only, real S256
  code exchange, native administrator API access and everyday denial, unassigned ordinary
  and global-administrator refusal, stable app UUIDs and post-revocation fresh-code denial.
  With installer qualification, provider UUID subjects stay out of reports and state.
  Native link/unlink refusal preserves managed account subjects in both provider modes.
  The fixture uses the selected local binary/unreleased dependencies; it does not attest a
  production release. Synthetic back-channel logout stays in synthetic-provider mode;
  complete identity lifecycle, admin workflow separation and restore
  remain gated. Without producer mode, bridge intents are driven by the controlled SCIM helper.
- `KYQ_TEST_IMMICH_PRODUCER=1` additionally requires bridge/setup flags, the actual identity
  binary and the immutable SSH fixture image. Only actual KyIdentity mutations produce SCIM
  writes; inspect its durable revision/acknowledgment before checking native profile/roles.
  Verify initial owned profiles, name/email updates, old-session administrator grant/removal,
  assignment-loss native session/key/socket denial through unavailable-write retry and retained
  account/photo bytes. Protected runtime registers both native bindings before listening;
  shutdown joins service/workers and reopens directory/revocation audit/completion proofs.
  A capped 64 MiB SSH fixture on the owned Docker network forwards raw TCP to host loopback;
  end-to-end TLS retains standard trust and exact DNS/IP SANs. Host keys are generated/pinned,
  SSH publishes only on loopback, and no Docker socket is mounted in this relay. No host
  firewall/network policy changes are made. Retained-code exchange after assignment loss may
  fail at identity before Immich sees claims; separately verify new authorization refusal.
  This proves registered-account delivery, not new-account creation, global multi-connector
  disablement, complete audit/restore, administrator purpose separation or production admission.
  Adding `KYQ_TEST_IMMICH_CREATE=1` also enables protected creation and qualifies actual
  new-assignment worker delivery, ordinary native UUID/profile receipt, exact subject-bound
  PKCE sign-in, usable native session/key and global-disablement revocation with retained
  profile. Joined runtime shutdown reopens creation/initialization/revocation proofs and native
  ownership. Full-suite multi-connector coverage and admission remain separate gates.
- `KYQ_TEST_IMMICH_MULTI=1` requires producer and creation modes. Add a digest-pinned
  Forgejo 16.0.5 fixture capped at 512 MiB/one CPU, a distinct OIDC client/source and SCIM
  token, and a second target in the same protected runtime. Verify actual worker creation
  in both native products, one global disablement denying their old sessions/keys while
  retaining profiles and the exact Forgejo repository ID, and Immich-only assignment loss
  preserving the other native account/session. Joined shutdown reopens both numeric and UUID
  creation/directory/revocation proofs. This covers this product pair; full-suite coverage,
  administrator purpose separation, audit/restore and release remain gates.
- `directory_test.go`: native proof/identity/profile validation, no writes on missing/foreign/revoked
  ownership, exact revision readback and reconciliation after a lost response.
- `KYQ_TEST_IMMICH_CREATE=1` requires setup/module mode and qualifies native absence,
  session-only administrator refusal, installation/provider/subject/email/role conflicts,
  atomic ordinary creation, exact receipt repeat, later profile/administrator projection
  without repeat rollback, existing-account retention, revocation while soft-deleted with OIDC
  disabled/rotated (rotated binding refused, restore stays fenced) and permanent revoked-account refusal.
  Alone this is controlled native API qualification; with producer mode it also drives
  actual new-account assignment delivery through protected runtime creation.
- `creation_test.go` refuses incomplete/foreign/contradictory absence and holds lost writes
  or unverified UUID/profile/digest receipts without returning a binding.
- Native setup qualification also tests directory name/email updates and repeats, original setup
  inspection after legitimate projection, stale/foreign/duplicate-email rollback, refusal to adopt
  an unreceipted native edit, native role grants and old-session demotion. A signed OAuth role
  conflicting with the owned directory receipt cannot overwrite authority. Profile projection
  is wired into protected resident directory mode; with setup and bridge flags, the controlled
  SCIM helper additionally checks profile/repeat/grant/demotion/readback, retained identity,
  reopened directory audit proofs and post-revocation refusal. This does not qualify actual
  KyIdentity event production, account creation or a complete resident lifecycle connector.
- `provider_test.go`: preflight/foreign-provider refusal, exact numeric/field preservation,
  repeat without writes and uncertain write/readback. The native fixture uses this adapter
  before signed login and verifies exact repeat plus stale/foreign intent refusal.
- `access_test.go`: refuse missing/negative counts and foreign ownership/subject before mutation;
  revoke with disabled/rotated provider or soft-deleted account; hold lost writes, incomplete
  readback and post-write identity/profile drift.

## Child DOX Index

- `module/AGENTS.md`: native owned setup/fence/API/authentication contracts, upstream build and fixture provenance.
