# BookStack native adapter groundwork

## Purpose

Build and verify native setup, directory and offboarding interfaces before qualifying BookStack for installation.
The catalog BookStack never uses this groundwork or `module/`: it runs stock BookStack with the
receiver in `internal/stockbookstack` (Yoshi, 2026-10-20: stock only).

## Ownership

- This directory owns disposable native-interface qualification.
- `client.go` and `accounts.go` own native-command invocation, receipt-owned account
  provisioning, grant verification and resumable offboarding. The caller independently
  verifies the owned container/user before binding execution.
- `creation.go` owns installation/account-scoped ordinary creation, strict joined native
  receipts and read-only recovery of unknown results over the native command.
- `creation_http.go` owns scoped HTTPS absence/creation/receipt inspection and distinguishes
  ambiguous writes; its authority is separate from directory and revocation.
- `profiles.go` owns explicit native profile initialization, revisioned name/email projection
  and verified current-profile/audit readback. It does not project roles or enable directory runtime.
- `roles.go` owns explicit native role initialization, administrator projection and
  verified policy/revision/audit readback over fixed ordinary roles.
- `directory.go` owns combined native profile/role initialization, atomic projection
  and inspection with both source-bound receipts.
- `http.go` owns HTTPS inspection/revocation using the product's separate scoped token;
  its credential has no creation/content authority. The parent owns bridge orchestration.
- `directory_http.go` owns HTTPS inspection and atomic profile/role projection with
  separately provisioned directory authority; initialization stays container-local.
- `module/` owns account/profile/role receipts, atomic directory projection,
  retained-account revocation and native auth hooks.
- The parent owns admission, manifests, deployment and target-owned receipts.

## Local Contracts

- Verify retained-account disable, old-session denial and token revocation independently.
  User deletion or a successful update response alone does not satisfy offboarding.
- Test only newly created containers, networks and data. Pin fixture image digests and
  preserve existing installations, account identity and content.
- Missing native interfaces remain explicit gates; a placeholder adapter is not completion.
- Each client call owns a separate protected target request directory; input and native
  error bodies are never written to workstation state or returned as error messages.
- Verify the pinned module version, native ID, issuer/subject digest, both fences and zero
  tokens before claiming completed revocation. One fence alone is partial state.
- HTTPS calls use bounded requests/responses, verified TLS, timeouts, no environment proxy
  and no redirects. Unknown mutation outcomes require inspection before retry.
- Account setup probes the exact configured issuer/client and supported module before
  creation. One immutable receipt key per account commits with its native user in the
  product database. Email or subject collisions never authorize adoption.
- Account inspection requires module 0.15.0 and `ky.bookstack-account-report/2`, including
  validated live name/email. Retained profiles are observed without resetting original intent;
  new-account profiles must match creation input. Older incomplete reports are refused.
- Module 0.15.0 `kyquickstart:create` atomically creates an ordinary account, assigns only
  validated non-management roles and initializes owned profile/role/creation receipts and
  audits. Installation UUID and account setup UUID are separate pins. Prior setup accounts
  without creation receipts cannot be adopted. Repeats preserve projected or revoked state;
  creation checks may observe separately projected administration only with matching native
  directory proof, without granting it. Probe the exact source/module before mutation.
  Unknown results require `CheckCreatedAccount` with the identical immutable spec.
  HTTPS creation requires separate protected token and fixed installation/source/ordinary-role
  policy. Absence is read-only; non-absent subjects require matching immutable creation receipts,
  never adoption. `ErrCreationUncertain` requires inspection before retry; direct SDK retry
  probes and checks ownership before another write. Module 0.15.0 also resolves an explicitly
  pinned source/subject/account setup UUID through joined native account/assignment/creation
  and current directory proofs. Subject absence refuses orphan ownership/audit keys as well
  as native subject/fence collisions. No name/email or native ID selects resolution.
  The parent bridge owns explicit protected creation opt-in and stable native setup derivation;
  production lifecycle, restore and administrator workflow separation remain gates.
- Profile management is an explicit native-command opt-in over an already owned account.
  Initialization pins the exact current profile; updates compare the native revision and
  commit profile, receipt and audit together. Native profile/subject edits and deletion are
  refused after initialization. Retain revoked profiles; missing audit or native drift refuses
  inspection/mutation. Registered-account worker delivery is qualified by producer mode; full lifecycle and
  production composition remain gates.
- Role initialization captures exact current native roles; initial administrator capture
  requires a verified source-bound OIDC grant. An explicit empty ordinary-role array is valid for an administrator-only native account;
  missing/null role arrays refuse. Capture each account separately without adding roles.
  Pin ordinary role IDs/alias and native role
  definition/permission fingerprints. Projection changes only the native administrator role;
  updates, receipts and audits share a transaction. Repeats retain revision and unknown results
  require inspection. Native managed role/subject edits and deletion are refused.
- Combined directory updates lock the owned native user and join profile/role writes and
  their audit publication in one transaction. Either refusal rolls back both; lost replies
  inspect both receipts before retry. The HTTPS directory endpoint requires its own protected
  credential, distinct from offboarding; it permits only check/apply over already initialized
  owned receipts. Cookie/native/offboarding tokens grant no directory authority. Native
  initialization, creation and content access are outside its scope. Registered-account profile/role, assignment-loss and global-disablement worker delivery is qualified;
  cross-product lifecycle, groups and production remain gates.
- HTTPS projection distinguishes known native refusal from ambiguous writes and post-write
  proof failure with `ErrDirectoryUncertain`. Direct SDK retry inspects first; the resident
  store holds those outcomes for reconciliation rather than replaying another profile.
- Managed OIDC compares validated signed roles with the owned projection and skips native
  login-time group synchronization. Unknown/malformed/duplicate roles and stale grants refuse
  login. Missing/empty roles permit only owned ordinary access. Authenticated native requests
  inspect current roles, policy and audit proof before proceeding. Alternative/global native
  writers, administrator purpose separation and restore remain qualification gates.
- Create accounts without administrator roles and let native verified OIDC establish them.
  Report activation and grant prerequisites as pending; a role alone is insufficient proof.
- `native_producer.py` owns the disposable pinned SSH raw-TCP relay and private resident
  test control; the actual identity worker is the only SCIM writer in producer mode.
- `native_identity.py` provisions only its owned disposable KyIdentity through `apply-setup`
  and native APIs. Native subject receipts bind product accounts; no existing provider is used.
- Bind each OIDC subject to a separate native assignment receipt. Reject unknown or revoked
  subjects before native registration or role synchronization, rather than after login.

## Work Guidance

Use upstream native services/interfaces; any required extension must be open source,
release-qualified, included in backups and tested through actual authentication boundaries.

## Verification

- `KYQ_TEST_BOOKSTACK_IMAGE=<digest-pinned-image> KYQ_TEST_BOOKSTACK_DB_IMAGE=<digest-pinned-mariadb> python3 -u internal/adapters/bookstack/qualification.py`:
  disposable native API counterexample. Standard-auth sessions and API tokens remain usable
  after an update containing disable flags. Success reproduces the missing interface;
  it does not qualify offboarding or prove OIDC integration. Fixture credentials stay in
  memory; containers, networks and volumes are removed on exit. Serial native qualification
  caps BookStack at 1 GiB and MariaDB at 768 MiB, each one CPU with no swap growth;
  compiler scratch and logs use the root disk-backed TMPDIR.
- Add `KYQ_TEST_BOOKSTACK_MODULE=1` to test the community module: private input/binding
  rejection, old web/API session denial, token removal, new password-login denial,
  retained accounts/roles/content, concurrent retries and restart persistence.
  This fixture uses standard authentication with a configured issuer and explicit subject;
  signed OIDC, restore and installer orchestration remain separate acceptance gates.
- `go test -race ./internal/adapters/bookstack`: wire/binding rejection, error redaction,
  refusal to mutate through an unsupported module and partial-publication resume.
- Add `KYQ_TEST_BOOKSTACK_ADAPTER=1` to the Python command for native Go-client verification.
  Only its nonce-labeled, digest-pinned fixture container is accepted. The test exercises
  inspect/revoke/resume, repairs a deliberately missing subject fence with zero tokens,
  and checks staging cleanup before the Python fixture verifies actual access denial.
  It also covers account creation/resume, source and intent conflicts, no adoption,
  Unicode intent digests, lost-create-response recovery, pending owner grants and resume
  after offboarding. Native operator name/email edits are read back through check/create
  repeats without resetting profiles or replacing ownership. A second helper explicitly
  initializes and projects the owned profile, checks stale/foreign/collision refusal, repeats,
  lost-result resume, audit-failure rollback, missing-proof refusal and retained revocation.
  Native edit/delete APIs return 403 for the initialized account.
- With the same pinned-image variables, run `python3 -u internal/adapters/bookstack/oidc_qualification.py`:
  a disposable TLS gateway and signed fixture IdP exercise native discovery, RSA validation,
  one-use PKCE exchanges, owner grant/proof and Go `VerifyAdmin`, everyday management denial,
  invalid signatures, unassigned-subject refusal and retained-account OIDC offboarding.
  Private keys stay in memory; only the public CA and trust configuration are mounted.
  The same fixture verifies the real HTTPS Go adapter, partial-fence repair and completed
  resume. Cookie-only/wrong-token, unowned/binding-invalid, oversized and insecure-credential
  requests are refused; the scoped bearer has no content API authority.
  Role phases explicitly initialize/project/read back native authority, verify lost-result
  resume and audit-failure rollback, old-session administration/demotion, signed-role
  mismatch refusal, protected native edits and retained revocation. Signed fixtures also
  exercise missing/empty, unknown, duplicate, mixed-type and scalar role claims; exact
  PKCE exchange counts follow each attempted authorization.
  Combined directory initialization refuses mismatched bindings and rolls back its first
  receipt when role initialization fails. Combined demotion/profile projection checks
  stale-role and audit-failure rollback of both effects over actual HTTPS, then lost-reply
  resume and repeat. Native initialization remains on the CLI. Directory authority tests
  refuse offboarding/native/cookie credentials, shared credentials, insecure files,
  uninitialized/unowned accounts, oversized/mismatched requests and initialization attempts.
  Neither scoped token grants native content access; directory credentials cannot revoke.
  Initial administrator projection also uses HTTPS; old sessions gain and lose native
  administration while fresh signed claims must agree with the projected grant.
  Administrator workflow separation, restore, guided dispatch,
  release provenance and browser UI acceptance remain separate gates.
- Add `KYQ_TEST_BOOKSTACK_IDENTITY_BINARY=<absolute static fixture binary>` to use actual
  KyIdentity. Its owned data and credentials live in a separate private temporary target;
  only the TLS/public fixture artifacts are shared with the gateway and PHP consumer.
  Native setup creates/checks/resumes exact owner/everyday accounts, client and roles.
  The identity fixture is capped at 512 MiB and its TLS gateway at 256 MiB, each one CPU
  with no swap growth; database/application retain the qualification limits above.
  Actual native login/discovery/PKCE, subject-bound administrator proof and Go verification
  pass. Projected native name/email survives a fresh actual OIDC login with everyday
  authority unchanged. Audited, operation-bound step-up removes the everyday app assignment: fresh issuance
  is denied, while old product sessions/tokens require explicit adapter revocation. This
  fixture verifies that revocation without claiming a resident SCIM bridge exists.
  Assigned-only denial without a code returns a clean login refusal after native state checks.
- Add `KYQ_TEST_BOOKSTACK_QUEUE=1` to either OIDC fixture mode to deliver through the durable
  bridge store and native target. Registration verifies the native binding; interrupted
  partial publication survives store restart and repairs before completion. The fixture
  retains the same actual access-denial/content checks. This does not qualify SCIM delivery,
  a runnable resident service or deployment composition.
- Add `KYQ_TEST_BOOKSTACK_SCIM=1` with the queue flag to resume its real partial native state
  through app-scoped authenticated TLS SCIM. Preserve actual backoff, require pending 429
  and conservative active status, then require native repair before 204/inactive. This client
  fixture does not test delivery by the KyIdentity SCIM engine or active profile provisioning.
- Add `KYQ_TEST_BOOKSTACK_WORKER=1` with the queue flag to finish real native partial state
  through the resident retry worker after actual backoff. No further SCIM mutation triggers
  completion; authenticated TLS reads only observe it. The fixture still proves retained
  content and web/API/new-login denial. Runnable service/deployment remain separate gates.
- Add `KYQ_TEST_BOOKSTACK_RUNTIME=1` with the queue flag to load separate protected runtime
  files and start the real TLS service/worker. Native partial state resumes after actual
  backoff; TLS reads observe completion, and shutdown joins before state is reopened. Fixture
  credentials/key files are temporary private target artifacts. Deployment, actual KyIdentity
  SCIM engine delivery and restore remain separate gates.
- Add `KYQ_TEST_BOOKSTACK_DIRECTORY=1` to either OIDC mode to invoke the actual protected
  resident TLS service against this owned BookStack. A controlled authenticated SCIM sender
  projects profile/administrator authority, removes it and repeats; GET verifies native
  readback. Joined shutdown precedes reopening unchanged numeric bindings and all directory
  audit proofs. The parent verifies native profile and old/fresh ordinary access; subsequent
  retained revocation preserves role revision four. Actual worker delivery requires the separate
  producer mode below.
  Creation/groups, dedicated administrator purpose, audit relay and restore remain gates.
- Add `KYQ_TEST_BOOKSTACK_PRODUCER=1` instead of the controlled directory flag, with the
  actual identity binary and `KYQ_TEST_BOOKSTACK_SSH_IMAGE=<immutable sha256 image ID>`,
  to configure a receipt-owned native SCIM connector and a pinned raw-TCP SSH relay.
  Initial delivery, native owner/everyday profile changes, administrator grant/demotion and
  assignment-loss revocation require newer acknowledged identity revisions. Verify old
  sessions/tokens and fresh OIDC denial, retained content and joined shutdown/reopened
  unchanged native bindings plus every directory audit. Credentials travel on bounded stdin
  or private fixture files, never environment JSON/output; trust uses the public server
  certificate with explicit private callback opt-in only in this owned identity fixture.
  Global disablement also uses a separate surviving identity administrator: require newer
  inactive acknowledgment, denied old identity/product sessions and token/fresh OIDC,
  unchanged owner directory receipts and a read-only native retained-content proof. Reopen
  both completed revocations and all their audit attempts. Cross-product global lifecycle,
  Without the create flag this exercises registered accounts only. Groups, uncertainty
  reconciliation, audit relay, restore,
  administrator workflow separation and deployment remain gates.

- Add `KYQ_TEST_BOOKSTACK_CREATE=1` with an actual identity binary (without producer mode)
  for standalone native ordinary creation and signed use. It exercises policy/audit failure rollback,
  lost-result inspection, no collision/prior-account adoption, profile-preserving repeat,
  newly assigned subject OIDC with usable ordinary session/token, retained revocation and
  old/fresh access denial. Standalone mode does not qualify identity-worker delivery.
- Add `KYQ_TEST_BOOKSTACK_CREATE_HTTP=1` to that creation fixture to run birth/check/resume
  through verified HTTPS, including an actually committed lost transport reply. It also checks
  protected token/policy permissions, fixed source/install/role pins, separate credentials,
  bounded byte-exact requests and no directory/revocation/content authority. The native
  CLI still supplies explicit projection and revocation.
- Combine `KYQ_TEST_BOOKSTACK_CREATE=1` and producer mode for actual identity-worker birth:
  the assigned-only producer creates the previously absent subject, projects a later profile,
  verifies signed ordinary use and global-disablement old/fresh denial with retained content.
  Joined shutdown reopens creation/initialization/directory/revocation proofs and resolves
  the unchanged native account scope from receipts. Full lifecycle and deployment stay gated.

## Child DOX Index

- `module/AGENTS.md`: owned account/profile/role/directory commands, scoped revocation,
  immutable fences and native hooks.
- `testgateway/AGENTS.md`: disposable signed test IdP and TLS transport fixture.
