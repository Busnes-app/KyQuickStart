# Forgejo adapter

## Purpose

Set up native OIDC sources/accounts, verify owner administration and revoke user access.

## Ownership

- `source.go`: native administrator sessions, source creation and read-only inspection.
- `creation.go`: exact source/subject absence and ordinary-account creation with explicit
  unknown-outcome classification.
- `team.go`: exact retained organization/team authority, missing-team creation and owned
  ordinary-account membership replacement, complete native reads and description-only labels.
- `client.go` and `profile.go`: native REST account provisioning, pinned profile updates,
  authenticated directory role projection, administrator verification and offboarding.
- `native_identity_test.go`: actual disposable KyIdentity provider and audited native
  assignment/role/profile changes for the pinned-image fixture.
- `native_producer_test.go`: actual native worker acknowledgment and the separate resident
  bridge fixture, scoped to registered accounts.
- `native_group_producer_test.go`: actual KyIdentity worker group lifecycle, native repository
  access and retirement qualification using owned disposable receipts.
- Native-container and test-IdP fixtures verify these interfaces; the parent owns admission,
  release manifests, deployment orchestration and protected target-owned receipts.

## Local Contracts

- Source forms require qualified Forgejo 16.0.5. Its REST API omits authentication sources;
  API tokens cannot authenticate administrator forms. Use a native administrator session.
- Source/account IDs come from protected target-owned receipts. Names never authorize
  adoption. Repeat setup checks exact retained IDs and refuses authority/configuration drift.
- Source credentials stay in memory and are compared without returning their values.
  Read protected target credentials before calling the adapter; source settings contain no secret.
- Source inspection checks issuer, client, scopes, assignment/admin claims and disabled
  username changes/custom endpoints/extra authority mappings. It does not repair drift.
- `CreateDirectoryAccount` refuses an existing native source/login-name identity and username
  collisions. Absence requires a successful bounded exact-filter response with an empty array
  and one zero total-count header. New accounts include their requested display name/email
  and verify complete profile, source/subject, ordinary authority and active state.
- A POST failure other than a known 4xx rejection (408 excluded), malformed successful reply
  or failed post-write verification returns `ErrCreateUncertain`; callers persist a hold and
  never issue another create or adopt an account discovered afterward without owned proof.
- No reactivation method exists. Forgejo 16.0.5 session auth loads only the session's user
  ID and nothing revokes a session (codeberg forgejo#796), so `Deprovision` denies old sessions
  only through `prohibit_login`. Re-enabling sign-in revives them; the bridge refuses
  reactivation until a Forgejo release with session revocation.
- `GrantAdmin` verifies an active exact-subject native administrator grant; it never promotes.
  Initial setup verifies native OIDC, while explicit directory mode may project app-scoped roles.
  Account repeat setup preserves existing authority/passwords and disabled status.
- Native CLI source creation requires provider-cache activation before first PKCE login.
  Native administrator-form creation activates inside the serving process without a restart.
- `UpdateProfile` changes only display name and primary email on an exact pinned binding.
  Require complete native profile fields, inspect before mutation and verify afterward.
  Preserve source/subject, administrator grants and active/prohibited state, including disabled
  accounts. Lost replies remain errors; resume inspects the desired values before writing.
- `ApplyDirectory` additionally projects the authenticated app-scoped administrator boolean.
  It refuses disabled accounts and verifies the pinned source/subject, desired profile/role
  and unchanged activity/prohibited state. Unknown native write or failed post-write
  verification returns `ErrDirectoryUncertain`; directory callers fence newer writes rather
  than treating a later read as settled completion. `InspectDirectory` requires profile fields.
- Actual KyIdentity source qualification uses required claim `aud` equal to the pinned client
  ID and app-scoped `roles`; the client's native assigned-only policy controls issuance.
  The signed fixture's `kyq_assigned` claim is synthetic and is not a KyIdentity requirement.
- Team receipts pin organization/team/source IDs, names and exact repository-unit permissions.
  Only read/write teams without all-repository, repository-creation or repository-admin team
  control are supported. Create requires complete bounded native absence; collisions refuse
  adoption. Membership requires every current member to be in the supplied verified owned
  scope and every desired member to be active and non-administrative. Withdraw non-desired
  members before validating or adding desired ones; an unfit desired member is a definite
  refusal only after withdrawals verify natively, otherwise `ErrTeamUncertain`. Verify native
  membership and authority afterward. Keep repositories and permissions intact.
- Forgejo clamps `limit` to `[api] MAX_RESPONSE_ITEMS` (16.0.5 default 50). Team and member
  listings page at 50 until `X-Total-Count`, refusing a changed total, short/oversized page,
  duplicate ID, more than 1000 teams or more members than the owned scope.
- Team callers own durable intent, receipts and serialization. Unknown creation/write or
  failed post-write verification returns `ErrTeamUncertain`; even a known refusal after an
  earlier successful membership write requires a hold. Label updates PATCH description
  only and verify the label and all pinned authority. Native reads require complete bounded
  membership. The bridge owns explicit SCIM opt-in and durable group receipts/intents;
  automatic outcome reconciliation remains unqualified.

## Work Guidance

Qualify changed native forms against a pinned real image before allowing another release.
Full KyIdentity connector lifecycle and native ordinary-workflow policy remain separate
acceptance requirements; provider sign-in alone does not satisfy them.

## Verification

- `go test -race ./internal/adapters/forgejo`: trust-boundary checks, including ambiguous forms
  and refused credential-forwarding redirects; profile identity/field refusal, native drift,
  authority preservation and lost-write reply resume without another mutation; team/member
  paging against a server that clamps to 50 and withdrawal before unfit-member refusal.
- `KYQ_TEST_FORGEJO_IMAGE=<digest-pinned-rootless-image> go test -race -v ./internal/adapters/forgejo`:
  real source/account creation and inspection, repeat/conflict refusal, zero-restart activation,
  verified TLS/RSA/PKCE owner/everyday/unassigned sign-in, native owner administration/everyday
  denial, 101 token revocations, old/new session denial, repository retention and disabled repeat
  provisioning, plus owner/everyday/disabled profile updates and repeats. The fixture uses
  disposable SQLite and a test IdP, binds ephemeral loopback
  endpoints on Linux Docker host networking and removes its own container/volumes.
- Suspended-account pages use HTTP 200: assert their actual denial content. This proves access
  denial, not physical deletion of native session records.
- Add `KYQ_TEST_FORGEJO_QUEUE=1` to the native fixture command to verify bridge registration,
  interrupted completion, store restart and native revocation verification through the durable
  queue. Its subprocess accepts only the fixture's labeled, pinned container and loopback TLS
  endpoint; credentials stay in process memory. The parent fixture still verifies all 101 token
  removals, old/new session denial, retained repositories and unrelated owner authority.
  This does not qualify KyIdentity SCIM delivery or resident deployment.
- Add `KYQ_TEST_FORGEJO_SCIM=1` with the queue flag to resume its real retained native account
  through authenticated TLS SCIM after actual backoff. Require 429 before due and native
  verification before 204/inactive; existing denial/token/content checks remain. This client
  fixture does not qualify KyIdentity SCIM engine delivery or active profile provisioning.
- Add `KYQ_TEST_FORGEJO_REACTIVATE=1` with the queue and SCIM flags (no identity binary) to
  reopen the revoked bridge store and send KyIdentity 0.4.3's re-assignment PUT, then its
  PATCH, over the TLS receiver. The PUT is refused and audited `reactivation_unsupported`
  twice; the same native account stays disabled, the pre-revocation session, all tokens and a
  fresh sign-in stay denied and the repository is retained; the PATCH revokes again in epoch 3
  with the same checks. `make e2e` runs this mode with the pinned 16.0.5-rootless image.
- Add `KYQ_TEST_FORGEJO_WORKER=1` with the queue flag to complete retained native work after
  actual backoff via the resident worker, without another SCIM mutation. TLS reads observe
  status and the original fixture retains session/token/repository checks. Runnable-service
  configuration, actual KyIdentity event delivery and deployment remain unqualified.
- Add `KYQ_TEST_FORGEJO_RUNTIME=1` with the queue flag to load protected runtime files and
  start the real TLS service/worker against its pinned native fixture. Native work finishes
  after actual backoff, TLS reads observe it, and joined shutdown releases state for proof
  inspection. Private fixture key/credential files are removed with their temporary target.
  Actual KyIdentity event delivery, deployment and restore remain separate gates.
- Add `KYQ_TEST_FORGEJO_IDENTITY_BINARY=<absolute static fixture binary>` to use actual
  KyIdentity in its own labeled, ephemeral container and private temporary target. Its proxy
  trust is the exact Docker gateway /32 and its published port is loopback-only. Native setup
  creates/checks/resumes exact owner/everyday accounts, assigned-only client and roles.
  Actual discovery, signatures, PKCE and subject-bound native owner/everyday authorization
  pass. Operation-bound step-up removes the everyday app assignment: fresh issuance fails
  while retained product tokens still require the native offboarding path. Removing the
  owner app role removes Forgejo administration after fresh OIDC, including the prior product
  session; it does not prove immediate revocation before that login. Combining the queue and
  runtime flags proves native identity bindings survive resident revocation/retry, not that
  KyIdentity's SCIM worker produced the event or that profile delivery is wired.
- Add `KYQ_TEST_FORGEJO_DIRECTORY=1` with the actual identity binary to verify protected
  runtime SCIM profile/role replacement and repeated delivery against real Forgejo. Native
  identity app-role removal followed by authenticated PUT removes old-session administration
  before fresh OIDC. This does not qualify the identity worker as event producer, new accounts,
  group/rename delivery, uncertainty reconciliation or dedicated-admin workflow separation.
- Add `KYQ_TEST_FORGEJO_PRODUCER=1` with a fresh actual identity binary to qualify its
  setup-owned SCIM connector through the real worker: initial profiles/app roles, native
  profile edits, unassigned exclusion, assignment-loss offboarding and role removal before
  another sign-in. Poll actual producer acknowledgments/revisions and verify product access,
  retained content and private durable bridge proofs after joined shutdown. The optional
  queue/runtime flags additionally exercise the existing interrupted-delivery/backoff path.
  This mode requires `KYIDENTITY_LISTEN_HOST` support, verified first by an isolated no-network
  malformed-bind probe. Identity and the bridge use ephemeral loopback endpoints on host
  networking, exact loopback /32 proxy trust and standard public fixture certificate trust.
  The native binary/data and bridge credentials belong to private disposable targets only.
  This mode alone does not qualify group delivery; native user rename, held-write
  reconciliation, deployment and restore remain gates.
- Add `KYQ_TEST_FORGEJO_CREATE=1` with producer mode to add a separate owned identity pair
  and assign its everyday user through audited operation-bound step-up. The actual worker
  creates the previously uninstalled native account; real SSO, token/session revocation and
  repository retention pass. The bridge verifies reopened creation, directory and revocation
  proofs. This does not qualify group delivery, unknown-create resolution, deployment or restore.
- Add `KYQ_TEST_FORGEJO_TEAMS=1` for native missing-team creation/collision refusal, owned
  membership grant/repeat/withdrawal, private repository access/denial, administrator and
  foreign-member refusal, and content retention. The fixture creates its own organization
  through the native API. Its separate resident-bridge test executable proves durable native
  group creation/membership intent and proofs, restart/repeat, administrator/foreign-member/
  foreign-name refusal, native access withdrawal, revocation fencing, content retention and
  missing-proof refusal, plus authenticated TLS SCIM group create/rename/read/retirement and
  reactivation refusal. Production organization provisioning and dedicated-administrator
  workflow policy remain separate gates.
- Add `KYQ_TEST_FORGEJO_GROUPS=1` with producer and teams modes to create/check/resume an
  owned KyIdentity group assigned to the fixture app and enable protected bridge group policy.
  Native audited step-up mutations drive worker creation, rename, member withdrawal/regrant
  and deletion. Verified TLS reads, ordinary native repository access and reopened bridge
  creation/membership/retirement proofs verify completion and content retention. This does not
  qualify production organization setup, audit relay, unknown-outcome resolution or restore.
- The fixture does not qualify the full central lifecycle, SSH/other application tokens, target receipt
  durability, mixed-use migration, dedicated-admin ordinary-workflow separation, PostgreSQL,
  backup/restore, browser UI acceptance or complete deployment/admission.

## Child DOX Index

None.
