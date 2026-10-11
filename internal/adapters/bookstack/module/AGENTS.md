# BookStack native setup and offboarding module

## Purpose

Supply receipt-owned setup, ongoing native profile/role projection and retained-account offboarding inside BookStack.

## Ownership

- This directory owns module metadata, PHP hooks, container-local commands and the
  separate scoped HTTPS offboarding, directory and creation endpoints in `http.php`.
- `profiles.php` owns explicit current-profile initialization, native CAS projection/audits
  and the shared native profile/identity writer guard.
- `roles.php` owns receipt-pinned ordinary-role policy, native administrator projection,
  role revision/audit proofs and signed-login authority checks.
- `creation.php` owns the native ordinary-account birth transaction and its immutable
  installation/account-scoped receipt/audit and read-only absence proof. `http.php` owns
  its separate HTTPS creation authority and pinned private policy.
- `directory.php` composes the native profile and role commands in one user-row transaction.
- The parent owns disposable runtime tests and installer qualification.

## Local Contracts

- Only an exact native user ID, configured HTTPS issuer and external subject authorize revocation.
- Read bounded, private request files owned by the running application user.
- Publish immutable per-user and per-subject fences before removing native API tokens.
- The receipt reports the pinned module version, native ID, issuer/subject digest, disabled
  status, whether both fences are present and the remaining native token count. Partial
  publication must remain distinguishable when no tokens existed.
- Preserve account identity, roles and content. No reactivation command is provided.
- Web, API, new-login and self-registration boundaries enforce persistent revocation.
- Persist fences outside the web root and include them in backups. Missing module protection
  or altered permissions must fail qualification before any installation is offered.
- `KYQ_BOOKSTACK_REVOCATION_DIR` names a canonical persistent directory, provisioned with
  mode 0700 and owned by the application user. CLI requests are bounded 0600 files in
  private directories. The CLI and HTTP workers run as the same application user.
- Canonical paths have protected root/application-owned ancestors. Public roots and their
  native upload targets cannot hold revocation state.
- Errors and audit messages contain no credentials or request bodies.
- `POST /api/kyquickstart/offboard` requires verified HTTPS and its separate bearer token
  from `KYQ_BOOKSTACK_BRIDGE_TOKEN_FILE`: canonical application-owned 0600 JSON in a
  protected 0700 directory outside public/upload roots. Include this target credential in
  backups. Browser cookies and native API tokens grant no endpoint authority.
- The endpoint permits only inspection/revocation of receipt-owned accounts, matching both
  native ID and exact issuer/subject. It verifies the primary account and assignment receipts.
  Its token cannot grant roles, create/reactivate accounts or access product content.
- Bound the raw HTTP body before JSON decoding and preserve byte-exact bindings by skipping
  native string trimming on this route. Return generic failures and audit locally.
- Scoped HTTP endpoints return 503 for database exceptions and failures while reporting a
  completed native operation. Clients retain an uncertain outcome and inspect native receipts
  before retry; validation and proven native business refusals retain their refusal status.
- `kyquickstart:account` probes the configured OIDC source before account creation; required
  config is exact issuer/client, `sub`, groups from `roles`, removal enabled, avatar fetching
  disabled and token debugging disabled. Source activation can remain pending during setup.
- One `kyquickstart-account:<setup UUID>` native setting holds each account's immutable
  intent/native ID receipt, committed with the user in the same database transaction.
  New accounts get a target-generated password, no role grant and no invitation; retained
  accounts keep passwords, profile, roles and revocation. Collisions refuse adoption.
- Module 0.15.0 account results use `ky.bookstack-account-report/2` with current native
  name/email. Request and immutable ownership receipts remain `ky.bookstack-account/1`;
  upgrading this readback does not rewrite provisioning intent or adopt existing accounts.
  Resume preserves operator profile changes and reports their current values.
- `kyquickstart:profile` uses `ky.bookstack-profile/1` and the same protected input/source
  boundaries. Initialization requires an owned, active account and the exact current name/email;
  never infer ownership from a profile. One native setting pins setup/user/source/subject,
  current profile and revision; a separate immutable setting per revision proves its audit.
  Updates use native UserRepo in a user-row transaction with receipt/audit publication;
  stale revisions, collisions, foreign drift, missing proof and revoked writes refuse.
  Check is read-only and can inspect retained revoked profiles. No role changes occur.
- Native UserRepo edits share that user-row transaction before testing the profile receipt.
  Managed name/email/subject edits and deletion return 403; unrelated native fields and
  uninitialized accounts retain native behavior. Direct database/alternate writers and
  restore and production runtime deployment remain qualification gates.
- `kyquickstart:roles` / `ky.bookstack-roles/1` requires exact account/assignment ownership
  and explicit current-role initialization. Initial administrator capture also requires native
  verified OIDC authority. Pin sorted ordinary role IDs (no management permissions), their
  native claim/default mapping, definition/permission fingerprints and source binding.
  An explicit empty ordinary-role list captures no ordinary membership; null/missing lists
  refuse. Administrator-only capture still requires signed native authority.
  Native administrator projection preserves those ordinary roles; user-row transactions
  commit membership, receipt and immutable revision audit together. Missing proof, foreign
  policy/identity, native drift, stale revisions and revoked writes refuse.
- Managed native UserRepo role and subject edits/deletion return 403. OIDC role checks run
  after native token verification, under a transaction joined with login; signed claims
  must agree with owned administration. Native login-time group sync is skipped for these
  accounts. Missing/empty roles are ordinary-only; unknown/malformed/duplicate/stale roles
  refuse. Authenticated native requests verify current role/policy/audit state; failure denies.
  Global role-definition and alternate membership writers still require coordination.
- `kyquickstart:directory` / `ky.bookstack-directory/1` takes validated profile and role
  requests with identical action, setup/user and source binding. Both updates and audit
  receipts commit together or roll back together. Inspection observes one transaction;
  initialization captures current state without adoption. The revocation HTTP token has
  no directory authority. `POST /api/kyquickstart/directory` authenticates only a separate
  protected `KYQ_BOOKSTACK_DIRECTORY_TOKEN_FILE` with the same private-file/token format.
  Shared tokens disable directory access. It permits check/apply over already initialized
  owned receipts; initialization stays native. It has no creation, revocation or content
  authority. Registered directory delivery is qualified by the parent actual-worker fixture;
  single-product global disablement is qualified too; full lifecycle/deployment remain gated.
- `kyquickstart:create` / `ky.bookstack-creation/1` accepts private bounded input with an
  installation UUID, immutable ordinary account spec, everyday alias and non-empty canonical
  ordinary-role IDs. Both action fields agree. Native role definitions/permissions must
  pass the existing ordinary policy before assignment. Account, assignment, ordinary roles,
  initialized profile/role receipts and `kyquickstart-creation:<account setup UUID>` plus its
  `:audit` commit together; any refusal rolls back all of them. Prior setup accounts without
  that creation receipt refuse adoption. Store immutable creation intent; check/repeat never
  changes retained profile, roles or revocation. Read separately projected authority only
  behind the matching creation audit and native directory proofs. Include all these settings
  in consistent database backups. Existing account commands keep their original ordinary
  authority refusal; offboarding/directory tokens gain no creation power.
- `POST /api/kyquickstart/creation` accepts the same ordinary spec for create/check and
  read-only probe (both actions agree). `KYQ_BOOKSTACK_CREATION_TOKEN_FILE` must contain a
  separate credential distinct from directory/offboarding; `KYQ_BOOKSTACK_CREATION_POLICY_FILE`
  is a protected canonical application-owned 0600 JSON outside public/upload roots. Its exact
  installation UUID, issuer/client, everyday alias and ordinary-role IDs constrain every call.
  Probe reports source/account/install/role-bound absence without account IDs or content;
  revocation fences, subject/email collisions or any existing ownership refuse absence.
  Creation rechecks native ownership inside its transaction. Native role policy still applies.
  Cookies/native/directory/offboarding tokens confer no authority, and the creation token
  has no profile/role projection, revocation or content authority. Back up both private files.
- Module 0.15.0 `ky.bookstack-creation-scope/1` accepts read-only `subject-probe` and `resolve`
  at the creation endpoint, constrained by the same protected policy. Pin installation and
  account setup UUIDs, exact issuer/client/subject and ordinary roles. Subject probe checks
  assignment/native user/revocation and every expected primary/creation/audit key; orphan or
  malformed ownership never becomes absence. Resolve requires matching assignment and account
  intent, creation receipt/audit joined to installation/ordinary policy, and current native
  directory proof in one transaction. Return only verified native ID/setup UUID and source/
  subject/intent digests; preserve profile/roles and retained revocation. No prior setup account
  can resolve through this lane without the creation receipt.
- Dedicated owner grants require native administrator authority plus a source/subject-bound
  record from the post-verification OIDC login hook. Everyday inspection refuses management
  authority. Back up account/login receipts in the product database along with fence files.
- A separate native setting per issuer/subject binds the exact source, setup UUID and user ID,
  committed with account creation. OIDC registration resolves only these assignments before
  native group synchronization; unknown, rebound or revoked subjects cannot create users or
  receive roles. Resume requires both ownership and assignment receipts.
- After the native controller checks and consumes callback state, an absent authorization
  code clears pending PKCE and returns a generic OIDC refusal. Provider denial text is not
  rendered and no token exchange runs without a code.

## Work Guidance

Use the logical theme hooks and native framework services; qualify internal service usage
against each pinned BookStack release. Keep the module separately labeled as a community extension.

## Verification

- Parent disposable native fixture covers loaded-module behavior; PHP lint runs in its image.

## Child DOX Index

None.
