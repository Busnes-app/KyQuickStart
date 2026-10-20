# Purpose

Resident directory/offboarding bridge: app-scoped SCIM events, verified native account
bindings, durable directory intent, revocation work and audit delivery.

## Ownership

- This directory owns bridge state and scheduling. Product adapters own native access proof.
- `jobs.go` owns revocation/reactivation epochs, their attempts and immutable audits;
  `reactivation.go` owns the re-assignment PUT that starts a reactivation epoch.
- `directory.go` owns registered-account profile/role intent and immutable local audit proofs.
- `creation.go` owns native creation intents, verified receipts, initialization proofs and
  the created-binding marker; `scim_creation.go` owns its authenticated transport.
- `group_creation.go` owns protected native team setup intents, immutable verified creation
  receipts and group bindings; `scim_groups.go` owns the explicitly enabled group transport.
- `group_membership.go` owns group membership intent, immutable audit proofs and account
  authority serialization. Native adapters verify actual membership and access.
- `runtime.go` owns protected configuration loading, direct TLS and joined service lifecycle.
- `bookstack_directory.go` owns registered-account directory dispatch over separate native
  credentials and protected scopes, plus explicit ordinary creation and native receipt lookup.
- `audit.go` owns the single ordered kyPulse relay and its destination/source pin and receipts.
- The root owns deployment, target credentials, network policy and catalog admission.

## Local Contracts

- Persist one immutable binding and one epoch sequence of revocation/reactivation jobs per
  app/issuer/subject; names and emails never select an account. Bind only after the caller verifies native provisioning.
- `Target` dispatches BookStack, Forgejo and community-module Immich HTTPS adapters. `Register`
  verifies exact native identity before binding; `Deliver` uses only the retained binding.
  Protected configuration pins app, kind, origin, issuer and Forgejo source ID. Native source
  setup must verify that source against the issuer before constructing a Forgejo target.
  Tokens stay inside native clients in memory; foreign scope is refused before network I/O.
- Numeric BookStack/Forgejo bindings retain their existing JSON shape. Immich requires
  `native_uuid` and `client_id`, with `native_id:0`, no username and no numeric source ID.
  Reject mixed identity shapes and client/UUID drift; never rewrite a retained binding.
  Immich runtime configuration requires its exact `client_id` and installation UUID;
  registration verifies the native module fence/proof. Explicit directory mode additionally
  requires the owned current native profile/revision receipt. Explicit creation mode uses
  verified ordinary-account creation receipts; groups remain unsupported for Immich.
  Complete identity lifecycle admission remains gated.
- Forgejo/Immich/BookStack directory mode is explicitly configured with administrator/everyday role names.
  Setup must verify that mapping against the owned product provider before constructing the
  target. Default targets retain revocation-only authority.
- BookStack directory mode additionally requires `directory.token_file` distinct from every
  runtime credential and `directory.bookstack` with client ID, canonical ordinary role IDs
  and per-subject native ID/account setup UUID. Each account may pin its own canonical
  `ordinary_role_ids`; absent/null uses the app policy, while explicit `[]` preserves no
  ordinary roles. Copy each policy before serving; SCIM cannot select or alter it.
  Account setup UUIDs are independent of the
  bridge installation UUID. Register verifies initialized native receipts before binding;
  existing numeric bindings stay unchanged. Groups remain refused. Ambiguous
  writes or post-write proof failures hold the durable directory job across restart;
  native rejection can retry. Native resident service delivery is qualified with a controlled
  SCIM sender and actual identity worker for registered profiles/roles, assignment loss and
  single-product global disablement; cross-product and complete lifecycle remain gates.
- BookStack `directory.create_accounts:true` additionally requires distinct protected
  `directory.creation_token_file` and the product's matching fixed creation policy. New
  account setup UUIDs derive as version-8 UUIDs from installation/app/issuer/client/subject;
  native creation receipts, not the derivation, authorize ownership. Birth always starts with
  non-management ordinary roles; directory/admin projection uses its separately audited lane.
  Existing protected account maps stay immutable. Resolve unmapped accounts through exact
  native source/setup/creation/directory proofs on every call, and compare the returned native
  ID to the retained binding. No shared mutable map or binding schema change is introduced.
  Keep the credential for read-only scope resolution when disabling new birth; false/omission
  never enables creation/absence dispatch. All credentials/files remain distinct and private.
  Native unknown birth maps to the existing durable creation hold; revocation stays available.
- Bridge state is target-local, private and owned by the service user. Hold an exclusive
  directory-inode lease; serialize request/worker changes within the same account job.
  State inspection remains available while native delivery runs; a second delivery for
  that account returns pending. Other apps/accounts retain independent progress.
  The worker reopens the directory at each new scan so Btrfs sees late-arriving jobs.
- Persist revocation intent and each attempt's audit before calling a product. Preserve
  pending work across restart. Retry through the adapter's inspect/revoke/verify path.
- Started and result audits are separate immutable files, published before the final job
  marker. Completion requires both audit proofs. Retry backoff starts at attempt completion,
  caps at one hour and survives restart. Unsafe storage permissions fail closed at I/O.
- State JSON rejects unknown and duplicate fields. An optional runtime `audit` object pins
  one bare HTTPS kyPulse origin and a distinct private paired-source token basename. Check
  loading performs no capability requests or state writes. Operational admission requires
  this destination, its native receipt protocol and the complete failure-alert path.
- The audit relay authenticates non-browser OPTIONS on `/api/ingest/logs` before any POST;
  require `ky.log-ingest/1` and the source UUID. Persist origin, source and installation pins.
  Operational `audit.require_alerts:true` additionally requires one exact
  `Ky-Log-Alerts: ky.offboarding-alerts/1` before mutation; false/omission supports isolated
  ingestion-only fixtures. kyPulse grants this capability only through target-bound admin
  pairing. Native failure events and webhook delivery use kyPulse's own store/monitor.
  One relay owns `pulse-state.json` and immutable `<audit filename>.sent` acknowledgments;
  revocation workers retain their independent audit/job files and do not write relay state.
- Validate immutable audit/target scope and publish the exact NDJSON body/sequence before
  sending. Persist verified sequence/digest receipt before advancing. Lost replies retry
  identical bytes after durable backoff; an existing matching receipt repairs an interrupted
  head advance without I/O. Started delivery precedes its result. Scan at most 1024 entries
  per 1.1-second tick; count/verify retained receipts before assigning the next sequence so
  missing historical acknowledgment files or a rewound head fail closed before mutation.
  Reopen the directory at each new scan and after receipt publication; rewinding an open
  directory can retain a stale entry view on Btrfs. Qualify late-arriving audit work on disk.
- Native body/errors and credentials stay out of audit projections. Imported Activity uses
  fixed actions/outcomes, immutable audit hashes and app/binding digests; it is not kyPulse's
  keyed audit chain. Honor bounded Retry-After, cap retry backoff at one hour and retain pending
  work on transient delivery failure. Old protocol, changed source, bad receipt, unsafe state,
  authentication and sequence conflicts stop the service with generic errors.
- Relay HTTPS verifies standard trust, refuses redirects/proxies and bounds requests to 15s
  and response headers to 8 KiB. Runtime cancels/joins the relay before releasing state.
  Restore configuration and all relay/audit/receipt state together; missing or stale receipt
  proofs require explicit reconciliation. Never infer permanent archival from acceptance.
- Revocation is not permanent. Epoch 1 is `<id>.job` with `<id>.audit-*` (an older store reads
  unchanged as epoch 1); even epochs reactivate and odd epochs revoke, each in `<id>.epoch-<n>`
  with immutable `-audit-<attempt>-started|result` files avoiding the relay's `.audit-` names.
  Epochs are monotonic, at most 1001; a completed epoch is never rewritten and an earlier
  revocation must be complete. An active PUT during pending/attempting revocation returns 429
  and changes nothing. After completed revocation it starts a reactivation epoch (durable job,
  started/result audits, native call through `Run`/the worker); its profile/roles are the
  projection, and native administration must follow them before sign-in is re-enabled.
  Revocation-only targets refuse a native administrator (409, `administrator_refused`).
  A kind without native reactivation returns 409, audited `reactivation_unsupported`; another
  PUT is another attempt, the worker never retries a refusal. A later PATCH starts the next
  revocation epoch, also over an unfinished reactivation. A published result settles an
  interrupted attempt without another native call. GET `active` follows the latest completed
  epoch. Bindings, group retirement and the creation refusal for retained subjects stay
  permanent; reactivated accounts stay out of group membership.
- No kind reactivates natively yet. BookStack/Immich modules have no route (their app jobs add
  one). Forgejo 16.0.5 cannot revoke web sessions (session auth loads only the user ID; no admin
  API; codeberg forgejo#796), so re-enabling sign-in revives pre-revocation sessions; it stays
  unsupported until a Forgejo release with session revocation.
- Keep native errors, credentials and request bodies out of durable diagnostics.
- SCIM endpoints are `/scim/<app>/Users` and explicitly enabled `/scim/<app>/Groups`, with one separate 32-byte bearer
  credential per app. Accept direct TLS 1.2+ and one exact Authorization header; forwarded
  TLS claims, cookies and browser origins confer no authority. Filter only exact externalId
  with the sender's bounded pagination; every resource must match protected target scope.
- PATCH accepts only a single `replace active=false`. Bound and narrow JSON before intent;
  reject duplicate keys, activation and content deletion. Creation is a separate explicit lane.
  Unknown/missing bindings are conflicts unless explicit creation mode proves native absence;
  absence never implies completed offboarding.
- Explicit Forgejo/Immich/BookStack directory mode accepts an 8 KiB full active User PUT on registered bindings.
  Require exact subject, one primary email, a bounded display name
  and explicit known roles. Empty roles remove administrator authority; unknown/duplicate
  roles are refused. Revocation-only targets accept bounded roles that confer no authority,
  and only for reactivation; otherwise their PUT stays 405. Forgejo requires its pinned native
  username; Immich has no native username, so its bounded userName is informational and
  never selects or rebinds an account.
  Immich reads retain the opaque subject as userName. Accept SCIM role `primary` selection metadata without
  granting authority from it. Project administrator access from the protected mapping;
  ordinary-role aliases correspond to the product's non-administrator account state. Only a
  reactivation epoch activates a disabled native account; no directory update does.
- BookStack userName is bounded informational metadata, like Immich; native identity comes
  only from the retained subject/ID and protected account receipt map. Reads use the subject.
- Protected Forgejo/Immich/BookStack `directory.create_accounts:true` enables new active User POSTs only.
  Require the configured installation UUID, source/issuer and role mapping. The exact native
  source/login-name lookup must return an empty array and one zero `X-Total-Count` header;
  username or subject collisions never authorize adoption. Missing/corrupt private metadata,
  unsupported native reads and held creation/initialization return failure, never absence.
  Immich uses its installation/provider/subject-scoped native absence and immutable creation
  proof instead of a numeric source/login name. Require ordinary native authority at creation,
  then publish the verified UUID before the separately audited directory/administrator step.
- Each account owns `.creation` and separate immutable `.creation-audit-*` proofs. Persist
  intent/start before native POST and its verified ordinary-account receipt before binding.
  A created binding carries `creation:true` and requires those proofs even after restart.
  Initial directory/administrator projection follows receipt publication; acknowledge 201 only
  after directory success and a creation marker referencing its immutable audit attempt.
  Later profile changes retain that initial proof; repeated identical creation never rewrites
  the account or rolls back its profile/roles. Known rejection can retry; unknown/interrupted
  creation holds all later profiles across restart. Never repeat or adopt an uncertain create.
  A retained verified receipt repairs failed binding publication without another native POST.
  GET/PUT refuse unfinished initialization; revocation can still disable a retained binding.
- Internal creation receipts support numeric Forgejo/BookStack IDs and UUID Immich IDs. An uncreated
  scope contains neither native ID; only a verified product receipt supplies one. UUID receipts
  retain the exact client pin and have no native username; informational SCIM userName never
  enters that binding. Validation probes are neither persisted nor sent to a product.
  Protected Immich creation dispatch maps native unknown writes to creation uncertainty and
  known refusals to conflicts. Stock/unreceipted native interfaces cannot create a binding.
  Complete lifecycle admission requires native worker qualification beyond storage tests.
- Native Forgejo `CreateGroup` is a protected setup operation: require the exact installation,
  app/issuer/source and retained organization scope, non-administrative team permissions and
  native absence. Each group owns `.group-creation`, `.group-creation-audit-*` proofs and
  immutable `.group-binding`. Persist intent/start before native POST and its verified native
  receipt before binding publication; every binding read requires those proofs. Known native
  rejection may retry the identical scope. Unknown/interrupted creation holds later scopes
  across restart; never replay or adopt by name. A verified receipt repairs failed publication
  without another create, after native inspection. One group delivery lock protects its
  creation; other groups/accounts retain independent files and progress.
- Protected `ApplyGroupMembership` accepts only verified bridge account IDs in a bounded
  owned scope, with desired members a unique subset. Each group owns `.group-membership`
  and immutable `.group-membership-audit-*` proofs, separate from creation/account state.
  Persist intent/start before native I/O and result proof before completion. Require exact
  group creation receipts and native authority; foreign native members stop replacement.
- Membership takes its group delivery lock and, in sorted order, the delivery locks of every
  supplied owned account (at most 100). Roles, account creation and native revocation cannot
  race this operation; other accounts retain independent progress. Revocation intent may
  still publish immediately. Refuse desired revoked, uninitialized, unresolved-directory or
  administrator accounts before native I/O; recheck durable desired authority afterward.
  New revocation intent during native work leaves an unknown membership hold, not success.
- Unknown/interrupted membership holds all later memberships across restart. Known refusal
  permits another audited attempt; missing result proofs fail closed. Native errors and
  credentials never enter these records. Account revocation remains usable after group locks
  release, including during a persisted group hold. Automatic reconciliation remains a gate;
  administrator promotion after membership still
  needs the suite ordinary-workflow separation policy.
- Forgejo's optional protected `groups` policy pins native organization ID/name and exact
  non-administrative unit permissions. Require directory mode; validate before credentials
  and clone policy maps. Groups events provide only external ID, display label and verified
  bridge account IDs. Native team names derive from the stable group key; rename changes
  only native description. Create/PUT require the Group schema, explicit unique member array
  (at most 100), a nonempty label (at most 100 bytes) and no extra authority fields.
- Group lookup proves native absence before returning an empty collection. Existing bindings
  require creation proofs, actual native membership/label and protected scope. Held group
  creation/membership never becomes absence or success through GET. Omission disables group
  routes. Use the same direct-TLS app bearer and four-call/five-second bounds as Users.
- Directory membership derives withdrawal ownership from the latest verified state under
  the group lock; competing replacements cannot overwrite each other's current-member scope.
  Logical DELETE verifies empty membership, retains repositories/native team/receipts and
  records permanent retirement. Repeated deletion re-verifies; PUT/POST cannot reactivate it.
  Group reads after proved retirement return 404 (filtered reads return an empty page).
- Directory writes publish narrowed intent and a started audit before native I/O; publish the
  result audit before completion. Each account owns its `.directory` state and separate
  `.directory-audit-*` files. Profile and revocation lanes share only the native account's
  delivery lock, because they mutate the same account. Known refusals permit another audited
  attempt. Interrupted/uncertain native writes hold later directory changes across restart;
  return 503, never a pending 429/202 that would release KyIdentity's uncertainty fence.
  Read-back is observation and cannot release this hold; native completion/reconciliation
  support is an outstanding admission gate. Revocation remains available during a hold.
- Directory-mode reads inspect the actual native profile/role through verified HTTPS. Default
  targets retain the opaque-subject revocation projection. Active remains conservatively true
  until durable revocation completion proof; native inactivity alone does not prove token removal.
  Four synchronous calls per app and a five-second native deadline bound request work.
  Pending attempts return 429 with Retry-After; only proved completion returns 204. Storage
  failures return generic 503. Lost/server-error responses retain KyIdentity's normal fence.
- `ServeWorker` is the single resident scheduler per store. Stream at most 1024 directory
  names per 250ms scan, with one cursor and at most four background deliveries total. Preserve
  due times across restart; incomplete/attempting jobs resume only when due. Completed jobs
  require audit proof and never replay. Each delivery uses the existing native account lock.
  Missing/conflicting target scope or unsafe/corrupt state stops the worker; jobs remain durable.
- Cancel and join the worker before closing the store. It cancels and joins every in-flight
  native call before returning. HTTP request deliveries retain their separate per-app bound.
  Flat-directory full-cycle latency grows with retained audit files; split a jobs directory
  if measured growth exceeds the deployment's scheduling budget.
- Runtime JSON has schema `ky.offboarding-runtime/1`, one installation UUID, explicit IP/port,
  separate private state/config directories, server certificate/key basenames and 1–32 app
  entries. Each app pins product/issuer/source and distinct private product/SCIM token files.
  Validate all public fields before credentials; forbid inline secrets, duplicate apps/origins,
  reused file/credential authority, symlinks and unsafe ownership/permissions. Config is bounded
  to 128 KiB, credential/TLS files to 16 KiB. No files or keys are generated during loading.
- An optional Forgejo/Immich/BookStack app `directory` object names distinct
  `administrator_role` and `everyday_role` values (lowercase role identifiers, at most 64 bytes).
  BookStack additionally pins its protected account policy and separate credentials.
  `create_accounts:true` is an explicit per-app opt-in; omission preserves registered-account directory scope, and omitting directory
  preserves the revocation-only protocol.
- Config/credential/TLS files are service-owned 0600 under a canonical 0700 target directory
  outside state. Server keys match an unexpired server-auth leaf certificate; CA private keys
  are refused. Native outbound clients retain verified standard trust, no redirects/proxy.
  Runtime targets require matching installation UUID as well as app/source scope.
- `Runtime.Serve` owns the state lease, TLS 1.2+ listener, native worker and optional audit relay.
  Cancel/join HTTP handlers and both workers before closing state; failed startup/shutdown diagnostics stay generic.
  Config check and JSON/ordinary formatted reports reveal only validity and app count, make
  no native requests and perform no writes. Credential/key/config/state artifacts require
  suite backup/restore qualification before production; no deployment is authorized by load.
- Full connector/lifecycle composition, audit/failure-alert deployment and restore are required
  before claiming an operational bridge. Native user rename,
  creation/directory-hold reconciliation and creation/directory audit relay remain unfinished; the current
  kyPulse relay transports epoch-1 revocation audits only; later epochs' audits stay local.

## Work Guidance

Reuse native adapters and the identity worker's protocol; do not add a sign-in proxy.
KyIdentity fences lost/async/server-error mutation outcomes. Queued local work is not a
completed SCIM deactivation: transport integration must preserve truthful completion and
those fences. Source `internal/sync/delivery.go` and `scim.go` own that protocol behavior.

## Verification

- Immich binding/runtime fixtures reject mixed numeric/UUID identities, client/receipt drift,
  unsupported modes and foreign client pins before I/O; configuration loading is read-only.
- With the parent Immich native module fixture and `KYQ_TEST_IMMICH_BRIDGE=1`, compile this
  package's test helper and pass owned deployment/CA/binding/credentials on bounded stdin.
  Verify native registration, restarted UUID receipt, authenticated TLS SCIM intent,
  unavailable-write backoff, actual worker retry and restarted completion/audit proofs.
  The parent proves old key/session/socket and fresh-login denial and account/photo retention.
  Adding `KYQ_TEST_IMMICH_SETUP=1` enables protected directory roles and additionally proves
  native active profile/repeat/grant/demotion/readback, unchanged subject/UUID despite username
  metadata changes, restarted directory audit proofs and refusal after revocation. Optional
  actual KyIdentity sign-in does not change this controlled sender into its event producer;
  actual KyIdentity lifecycle delivery and full admission remain gated.

- With `KYQ_TEST_IMMICH_PRODUCER=1`, the parent's real identity fixture sends configuration
  and bounded shutdown control to `TestNativeImmichProducerBridge` on stdin. Protected
  runtime registers both owned accounts before serving TLS; only the actual identity worker
  writes SCIM. After joined shutdown, require unchanged UUID/client bindings and independently
  verified directory/revocation audit proofs. The parent proves acknowledgment/revision,
  actual role/profile changes, scoped access denial, retained photos and identity authorization
  refusal. A pinned owned SSH fixture carries raw TLS TCP from its private Docker network to
  the loopback runtime; no HTTP/authentication proxy or host firewall change is involved.
  New-account/global multi-connector lifecycle, audit/restore and production remain gates.

- Adding `KYQ_TEST_IMMICH_CREATE=1` to actual producer mode enables protected creation and
  the parent's new-assignment fixture. Verify an initially absent owned subject, real identity
  assignment/delivery acknowledgment, native ordinary UUID receipt, subject-bound PKCE,
  usable session/key followed by global-disablement denial and retained native profile. Reopen
  and require creation, initialization and revocation audit/completion proofs plus actual
  native UUID/provider ownership. This does not qualify global multi-connector disablement,
  creation-hold reconciliation, audit relay, native-writer coordination or production restore.
- Add `KYQ_TEST_IMMICH_MULTI=1` to that producer/create fixture for one protected runtime
  with distinct Immich/Forgejo targets and credentials. Reopen exact UUID/numeric native
  bindings and creation/initialization/global-revocation proofs; require no Forgejo revocation
  job for Immich-only assignment loss. The parent verifies actual native session/key denial,
  retained profiles/repository and independent existing-session access. This pair does not
  establish complete suite lifecycle, audit/restore or production acceptance.

- `go test -race ./internal/offboard`: private storage, binding conflicts, restart/resume,
  concurrent requests, durable attempts, bounded retry scheduling and both native SDK wire
  paths through verified TLS fixtures. These HTTP fixtures do not prove product access denial.
- Directory tests additionally prove strict role/profile boundaries, intent/audit ordering,
  partial-publication refusal, uncertainty across restart, no replay/supersession from a held
  write, known-refusal retry and native-account serialization with revocation.
  Runtime loading verifies explicit mappings without native I/O. Forgejo SDK tests distinguish
  known rejection from uncertain write/verification outcomes and refuse disabled grants.
- UUID creation tests prove restart/partial-publication repair without another native create,
  initialization before acknowledgment, opaque-subject SCIM readback, required audit proofs,
  mixed/supplied-ID refusal and foreign-client/unknown-outcome holds across restart. They use
  controlled adapter callbacks and do not qualify native Immich account creation.
- Add `KYQ_TEST_BOOKSTACK_QUEUE=1` to the BookStack OIDC fixture command in its owning DOX
  to verify real native queue registration, interrupted partial-publication repair across
  store restart, completed retries and retained web/API/OIDC access denial. Add its identity
  binary variable for actual KyIdentity. SCIM event transport and resident deployment remain
  separate gates.
- Add `KYQ_TEST_BOOKSTACK_SCIM=1` or `KYQ_TEST_FORGEJO_SCIM=1` alongside the corresponding
  queue flag to resume real native work over the authenticated TLS SCIM receiver. Tests
  preserve real backoff, require pending 429/active=true, and verify 204/active=false only
  after native repair. This is an HTTP-client fixture, not KyIdentity worker composition.
- Add `KYQ_TEST_FORGEJO_QUEUE=1` to the Forgejo native fixture command in its owning DOX
  for real native binding registration, interrupted-result restart, inspected revocation
  completion and retained account/token/session/content checks through the same queue.
- Reactivation tests (`reactivation_test.go`) use KyIdentity 0.4.3's exact PUT/PATCH bodies:
  revoke/reactivate/revoke with every epoch's job and audits byte-identical afterwards, 429
  during pending/attempting revocation, an origin/master store loading as epoch 1, retired
  groups and user DELETE staying permanent, POST refusal for a retained subject,
  `reactivation_unsupported` for BookStack/Immich/Forgejo with no native write, administrator
  refusal and role projection on the generic path, restart without a repeated native call,
  revocation superseding a failed reactivation and safe diagnostics.
- With the Forgejo native fixture's `KYQ_TEST_FORGEJO_REACTIVATE=1` (beside the queue and SCIM
  flags), `TestNativeForgejoReactivation` reopens the revoked store twice over TLS SCIM: the
  re-assignment PUT is refused and audited twice, then a PATCH revokes again in epoch 3.
- Worker race tests also cover single-worker exclusion, bounded native calls, joined
  cancellation, retained unattempted backlog, future backoff across restart, completed
  suppression, unsafe state/configuration and streaming beyond 1024 directory entries.
- Add the corresponding `KYQ_TEST_BOOKSTACK_WORKER=1` or `KYQ_TEST_FORGEJO_WORKER=1` queue
  fixture flag to resume real native work after actual backoff without another SCIM mutation.
  Authenticated TLS GET observes pending active and completed inactive; the original native
  access-denial/content checks remain. This does not qualify KyIdentity event production,
  protected runnable-service configuration or deployment.
- Runtime tests prove read-only command check, protected file/config refusal, UUID separation,
  safe reports, verified TLS worker dispatch against native wire fixtures, exclusive state
  ownership, listener release and HTTP/native cancellation joined before lease release.
  A Linux process fixture builds/runs the actual command, uses a public fixture certificate
  through standard TLS trust, and proves native wire completion plus clean SIGTERM/lease
  release. Other-platform command execution/trust distribution is not qualified by it.
  These service tests do not replace real-product, KyIdentity connector or deployment gates.
- Audit relay tests cover lost response/restart, durable backoff and bounded Retry-After,
  crash after acknowledgment before head advance, ordered phases, retained payload/proof,
  missing historical receipt refusal, unsupported protocol, changed source, redirects,
  invalid receipt and safe diagnostics. Runtime tests prove protected audit credentials,
  worker dispatch and joined in-flight cancellation. The native kyPulse API harness invokes
  `TestNativePulseAuditRelay`: commit then lose the socket reply, restart through the actual
  command with public fixture trust, verify two native log/activity rows from three POSTs,
  and SIGTERM/lease release. This proves native ingestion, not KyIdentity event production,
  failure-alert creation, product access denial or deployed archival/recovery.
- `TestNativePulseFailureRelay` is invoked by the native kyPulse failure harness with a
  protected synthetic failed attempt. It proves relay projection, required alert capability,
  native failure creation, fresh-monitor webhook delivery and receipt replay suppression;
  it does not qualify deployed product failures or KyIdentity lifecycle composition.
- Add the corresponding `KYQ_TEST_BOOKSTACK_RUNTIME=1` or `KYQ_TEST_FORGEJO_RUNTIME=1` queue
  fixture flag to verify real-product repair through protected loading and TLS service/worker,
  actual retry backoff, observer-only GET and joined shutdown before state proof inspection.
  The caller fixture supplies verified public trust; deployed trust distribution remains gated.
- Add `KYQ_TEST_FORGEJO_DIRECTORY=1` with its pinned image and actual identity binary to run
  `TestNativeForgejoDirectory`: protected runtime/TLS, subject-bound profile/role replacement,
  repeat, native read-back, joined shutdown and durable audit proofs. The parent removes the
  actual KyIdentity app role first and proves that the old Forgejo session loses administration
  before another OIDC login. This is authenticated SCIM-client delivery, not the KyIdentity
  worker's event production, new-account/group provisioning or complete admission.
- Add `KYQ_TEST_BOOKSTACK_DIRECTORY=1` to its owning disposable OIDC fixture to run
  `TestNativeBookStackDirectory`: separate protected credentials/account scopes, real resident
  TLS SCIM profile/grant/demotion/repeat, native readback and joined shutdown followed by
  reopened unchanged bindings and every directory audit. The parent checks actual native
  profile and old/fresh ordinary access, then retained-account revocation. This is a controlled
  authenticated SCIM sender, not actual KyIdentity worker delivery, creation/groups or admission.

- Add `KYQ_TEST_FORGEJO_PRODUCER=1` with the fresh actual identity binary to invoke
  `TestNativeIdentityProducerBridge` as a separately compiled race-enabled test process.
  Protected runtime/TLS owns both verified native bindings; only the actual KyIdentity worker
  sends mutations. The parent checks producer acknowledgment/revision, profile changes,
  assignment-loss token/session denial and old-session role removal; shutdown verifies both
  directory and revocation audit/completion proofs. Endpoints are ephemeral loopback, and
  bounded stdin control cancels/joins the runtime. Group delivery, uncertainty
  reconciliation, deployment and restore remain unqualified.
- Add `KYQ_TEST_FORGEJO_CREATE=1` with producer mode to qualify native worker creation of
  a newly assigned user, subject-bound SSO, token/session revocation, repository retention
  and reopened creation/directory/revocation proofs. Boundary tests also cover explicit opt-in,
  source absence, administrator-grant ordering, known-rejection retry, unknown-create holds,
  no replay/supersession, partial binding publication repair and missing required proofs.
  Creation hold resolution, audit relay and deployment/restore remain gates; groups require
  the separate producer/teams/groups fixture.
- `KYQ_TEST_FORGEJO_TEAMS=1` also invokes `TestNativeForgejoGroupOwnership` against the
  parent's labeled, digest-pinned disposable native fixture using bounded stdin credentials
  and verified public TLS trust. It proves native team creation receipts, restart/repeat,
  foreign-name refusal and missing-proof refusal. Boundary tests cover durable intent/start,
  known-refusal retry, unknown/interrupted/foreign-receipt holds, partial binding repair,
  scope drift, missing receipts and same-group serialization with independent-group progress.
  The same native fixture proves bridge membership intent/audits, restart/repeat, scoped
  repository access/withdrawal, native administrator/foreign-member refusal, revocation
  intent fencing and repository retention. Membership race tests additionally cover held
  outcomes across restart, missing-result refusal, invalid/unowned member scopes, unsettled
  directory authority and serialization with roles/native revocation while intent publishes.
  The fixture additionally proves authenticated TLS SCIM group creation, metadata rename,
  native reads, retirement, reactivation refusal and content retention. Boundary tests cover
  explicit runtime policy, unique/full member input, policy injection refusal, lost label/member
  replies, held-read refusal and withdrawal derivation from the latest proof. Production
  organization setup, audit relay, reconciliation and restore
  remain unfinished; native refusal alone does not prove suite administrator workflow separation.

- `KYQ_TEST_FORGEJO_GROUPS=1` with producer and teams flags additionally drives the actual
  KyIdentity worker through owned group creation, rename, member withdrawal/regrant and
  deletion. The resident target pins the fixture organization and read-only repository unit;
  reopened private proofs require at least five completed membership attempts and permanent
  empty retirement with the retained label. Native repository access and content retention
  are checked by the parent. Production group audit relay and restore remain gates.

- BookStack producer mode invokes the shared `TestNativeIdentityProducerBridge` with
  bounded stdin configuration/control instead of environment JSON. It verifies nonce-owned
  pinned native deployment, separate credentials and both initialized native account scopes
  before Serve. Only the actual KyIdentity worker writes SCIM; reopen both bindings, every
  profile/role audit and completed revocation after joined shutdown. The parent requires
  newer producer acknowledgments and native access/content effects. Its raw-TCP pinned SSH
  fixture remains network-private; no authentication proxy or host firewall changes occur.
  The same parent globally disables the owner using a surviving identity administrator,
  verifies native/identity denial and retained data, then reopens both revocation jobs and
  every audit attempt. Cross-product global lifecycle and production/restore remain gates.

- BookStack producer plus `KYQ_TEST_BOOKSTACK_CREATE=1` qualifies actual assigned-only
  worker birth through separate protected creation/directory/revocation credentials. Verify
  native ordinary sign-in and profile projection through dynamically resolved receipt scopes,
  global disablement, retained native content and reopened numeric creation/initialization/
  directory/revocation proofs. The initial fixed scope map is unchanged. Storage tests and
  native source/intent wire checks cover protected opt-in, distinct/unsafe credentials,
  source/setup/receipt drift and no adoption; uncertainty reconciliation, group delivery,
  audit relay, restore, cross-product lifecycle and production remain separate gates.

## Child DOX Index

None.
