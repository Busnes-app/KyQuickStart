# Native BookStack setup and offboarding extension

MIT community extension, tested against the disposable BookStack v26.09.1 fixture.
It is not admitted to the production release set. Production resident deployment, guided setup,
restore and release provenance still require qualification. Internal BookStack service usage must be
retested for every release; this module does not replace native authentication.

Load this directory as a module beneath the explicitly configured logical theme.
Preserve existing themes; saved installation references do not authorize changing them.
Provision a persistent directory outside the web root, mode 0700, owned by the application
user, and set `KYQ_BOOKSTACK_REVOCATION_DIR` to its canonical absolute path. Include that
directory and the module in backups. HTTP workers and CLI commands use the same Linux user.
Path ancestors must belong to root or that user and have no group/other write permission.

For atomic profile and role updates, run `php artisan kyquickstart:directory
--request=/private/directory.json`. The protected JSON contains exactly `contract`
(`ky.bookstack-directory/1`), `profile` and `roles`. The latter two are complete requests
for the profile and role contracts documented below. Their action, setup/user ID and
issuer/client/subject must match; each carries its own expected revision. Native writes,
current receipts and both immutable revision audits commit in one transaction. A role
revision/audit failure rolls back a preceding profile write. The result contains the
directory contract/module version and both verified native reports. `check` reads both
together; `initialize` captures exact current state. Back up the existing profile/role receipts
and audits; no extra shared directory receipt is introduced.

Module 0.15.0 offers the same check/apply protocol at `POST /api/kyquickstart/directory`
over verified HTTPS. Explicit initialization stays container-local. Provision
`KYQ_BOOKSTACK_DIRECTORY_TOKEN_FILE` separately with the private path, ownership, modes and
exact token JSON format documented for the offboarding credential below. Generate a distinct
32-byte random token on the target; a shared token disables directory access. Include both
credentials in sealed product backups. The directory credential can inspect/project only
already initialized owned profiles and roles; it cannot create, adopt, initialize, revoke,
reactivate or access content. The offboarding token cannot use the directory endpoint.
Cookie sessions and native API tokens authenticate neither endpoint. The Go directory client
uses the same source/policy/revision checks and lost-result inspection as the native CLI.
The resident bridge is qualified by the parent actual-worker fixture for registered-account
profiles, roles, assignment loss and single-product global disablement. Creation/groups and production
deployment remain unfinished.

The container-local interface is:

```sh
php artisan kyquickstart:offboard --request=/private/request.json
```

The canonical request file must have mode 0600, belong to that application user, reside in
a private 0700 directory and contain at most 4096 bytes:

```json
{"contract":"ky.bookstack-offboard/1","action":"deprovision","user_id":3,"issuer":"https://identity.example.test","subject":"exact-native-external-auth-id"}
```

`check` inspects without revocation. Both actions require the configured HTTPS OIDC issuer,
`OIDC_EXTERNAL_ID_CLAIM=sub`, an existing non-guest native ID and its exact external subject.
The JSON receipt reports contract, module version (`0.15.0`), native ID, SHA-256 of
`issuer + NUL + subject`, disabled status, `revocation_complete` (both fences present)
and remaining token count. Rejected requests return a generic error and exit 1.
An account can be disabled by a single fence while publication remains incomplete.
Retry revocation to complete both fences even if its token count was already zero.

For a resident bridge, the same receipt protocol is available at
`POST /api/kyquickstart/offboard` over verified HTTPS. Provision
`KYQ_BOOKSTACK_BRIDGE_TOKEN_FILE` as a canonical absolute path outside public/upload roots,
with the same application ownership, protected ancestors and private directory/file modes
as CLI requests. Its JSON contains exactly `{"token":"<64 lowercase hexadecimal characters>"}`.
Generate the 32 random bytes once on the product target, preserve them on resume and deliver
them through protected files or stdin; credentials never enter command arguments or
workstation installation state. Back up the credential and persist the bridge's copy on
its own protected target. Only its `Authorization: Bearer <token>` authenticates this route.

The HTTPS route checks both immutable account and assignment receipts before inspecting or
revoking the exact native ID/issuer/subject. Browser cookies and native API credentials do
not authorize it. Its token cannot access content, grant roles, create accounts or reactivate
users. Raw request bodies are bounded at 4096 bytes before decoding; failures are generic
and locally audited. The Go HTTPS client shares the native command's receipt verification
and inspect/revoke/verify sequence, refuses redirects and repairs partial publication.
Actual KyIdentity and signed fixture-provider tests exercise this network interface;
automatic SCIM dispatch and protected deployment wiring remain unfinished.

Revocation retains accounts, roles and content. Immutable native-ID and issuer/subject
fences deny subsequent web/API requests, login and self-registration; native API tokens
are removed after fence publication. Retries finish partial publication without replacing
bindings. Token recreation and a changed external subject do not clear the native-ID fence.
No reactivation command is provided. Requests already executing at publication are not aborted.

Account setup uses the same protected-file boundary:

```sh
php artisan kyquickstart:account --request=/private/account.json
```

```json
{"contract":"ky.bookstack-account/1","action":"create","setup_id":"2c676879-9c71-4913-b8a7-995521d9ed20","issuer":"https://identity.example.test","client_id":"bookstack-client","subject":"exact-subject","name":"Installation owner","email":"owner@example.test","administrator":true}
```

Each account gets its own stable setup UUID. `probe` verifies source configuration and
module version; `check` reads its existing receipt. Source configuration requires exact
issuer/client, external identity from `sub`, groups from `roles`, group removal enabled,
token debugging off and both OIDC avatar fetching off and `AVATAR_URL=false`.
The installer client probes before invoking `create`.

The native user, immutable intent/native-ID receipt and issuer/subject assignment commit together in BookStack's
database. A lost reply resumes by the same UUID. An existing email or subject without
that receipt is refused. Passwords are generated inside BookStack and never returned.
Creation grants no administrator role; native OIDC assigns it at sign-in. The receipt
reports activation, `oidc_verified` and administrator-grant prerequisites. Module 0.15.0
returns `ky.bookstack-account-report/2` with current native `name` and `email`. Requests
and stored immutable provisioning receipts retain `ky.bookstack-account/1`. Inspection
and repeat report operator profile edits without resetting them; new-account reports
must match the requested profile. The Go adapter refuses older incomplete reports. A configured
OIDC auth mode, active native administrator role and a source/subject-bound record from
the verified OIDC login event are required before owner access can be declared ready.
The disposable signed-OIDC fixture verifies this proof through native login and the Go
client's `VerifyAdmin`. Its native-provider mode also provisions actual KyIdentity accounts,
client and app roles through `apply-setup` and proves them through real OIDC endpoints.
Production installation ownership and central offboarding orchestration remain gates.

After native callback-state verification, a response without an authorization code clears
pending PKCE and returns a generic login refusal instead of reaching token exchange. Provider
error text is not rendered. This covers assigned-only refusal from KyIdentity.

Native OIDC registration is bound to the assignment receipt before group synchronization.
Unknown, rebound or revoked subjects are refused without registration or a role change.
Each assignment is a separate native setting and contains the exact source, setup UUID
and native user ID. Existing receipts without this assignment are not silently adopted.

Resume never changes retained profiles, passwords, roles or revocation. Everyday account
inspection refuses user, role or settings management authority. Account receipts and
OIDC assignment/login records are native database settings and must be included in database backups.


Module 0.15.0 provides explicit product-owned profile management through
`php artisan kyquickstart:profile --request=/private/profile.json`:

```json
{"contract":"ky.bookstack-profile/1","action":"initialize","setup_id":"2c676879-9c71-4913-b8a7-995521d9ed20","user_id":3,"issuer":"https://identity.example.test","client_id":"bookstack-client","subject":"exact-subject","name":"Current name","email":"current@example.test","expected_revision":0}
```

Initialize only an already receipt-owned, active account with its exact current profile.
This explicit opt-in creates a revision-zero profile receipt and native audit proof without
changing the profile. `check` reads current state and proof without writing; it ignores the
request's profile/revision values. `apply` compares `expected_revision`, updates only native
name/email and publishes its next receipt and immutable audit in the same database transaction.
Exact repeats retain the revision. Check/apply require initialization; the Go consumer
inspects before updating and after commit. After a lost result, inspect and repeat the desired
profile; never assume rollback. Missing proof, stale revision, foreign identity/profile drift,
email collisions and revoked writes refuse. Revoked readback preserves profile/revision.
Include profile receipts and all revision audit settings in the native database backup.

Once initialized, native UserRepo profile/subject edits and deletion return 403. Native
writers and initialization share the same user-row transaction; no process-memory ownership
flag is used. Unrelated native fields and uninitialized accounts retain their existing
behavior. Fresh signed OIDC resolves the owned user without resetting its projected profile.
The profile interface does not project administrator roles, change permissions or grant the scoped
HTTP offboarding token profile-write authority. Resident directory composition, other native writers, audit delivery and restore remain gates.


Module 0.15.0 additionally provides `php artisan kyquickstart:roles --request=/private/roles.json`:

```json
{"contract":"ky.bookstack-roles/1","action":"initialize","setup_id":"2c676879-9c71-4913-b8a7-995521d9ed20","user_id":3,"issuer":"https://identity.example.test","client_id":"bookstack-client","subject":"exact-subject","everyday_role":"editor","ordinary_role_ids":[2,4],"administrator":false,"expected_revision":0}
```

Use actual native IDs, sorted and unique. An explicit `ordinary_role_ids: []` preserves
no ordinary membership for an administrator-only account; missing/null arrays are rejected.
Each account is initialized against its own policy. Initialization requires exact current roles and
never grants authority. Capturing an initial administrator requires source-bound native
verified OIDC proof. Ordinary roles must lack user/role/settings management permissions and
match the native everyday claim or configured default role. Their definitions, permissions,
claim mapping and default-role setting are fingerprinted and pinned. The native system
administrator role is separate. `apply` compares the revision and changes only that grant,
preserving ordinary roles. Native membership, receipt and immutable revision audit commit
in one user-row transaction. `check` verifies current binding, policy, membership and audit
without writing; its requested administrator/revision values do not select authority.
Unknown/lost results require inspection; exact desired repeats retain revision. Native drift,
foreign policy/identity, missing proof, stale revisions and revoked writes refuse.

Managed native role/subject edits and deletion return 403. After native signature/issuer/
audience verification, OIDC claims must agree with the owned administrator grant; native
login-time group synchronization is skipped. Missing/empty signed roles permit only an
owned ordinary account. Unknown, malformed, duplicate or stale claims deny login without
changing native roles. Existing-session authorization follows current native roles; every
authenticated native request also verifies owned role/policy/audit state. Keep role receipts,
revision audits, native memberships and role definitions in consistent database backups.
The profile and role receipts own separate settings and revisions; they do not overwrite
one shared state document.

The resident bridge supplies SCIM over this HTTPS directory interface; its protected
configuration pins each account policy. Global role-definition and alternate membership
writers, dedicated administrator ordinary-workflow
separation, audit delivery and restore remain gates. A native grant is not proof of those
broader contracts. The scoped HTTP offboarding token keeps its existing revocation authority.


Module 0.15.0 adds `php artisan kyquickstart:create --request=/private/creation.json`
for atomic native ordinary-account birth. It is a container-local command; the existing
HTTPS directory token still permits only initialized check/apply.

```json
{"contract":"ky.bookstack-creation/1","action":"create","installation_id":"271333e2-c221-4601-b249-563b3d828530","account":{"contract":"ky.bookstack-account/1","action":"create","setup_id":"57a9c424-6499-4bfe-8f66-458b1c1136f0","issuer":"https://identity.example.test","client_id":"bookstack-client","subject":"exact-new-subject","name":"Everyday","email":"daily@example.test","administrator":false},"everyday_role":"editor","ordinary_role_ids":[2,4]}
```

Use verified role IDs and retain both UUIDs. All native account/assignment, ordinary-role,
profile/role initialization and creation audit writes join one transaction. Empty or management
role policy refuses; prior setup accounts without a creation receipt cannot be adopted.
For a read-only recovery, set both actions to `check` and retain every other original field.
An identical create repeat observes the retained account rather than resetting its profile,
roles or revoked status. Checks can observe later separately projected administration with
matching directory proof, but creation never grants it. Back up native
`kyquickstart-creation:<account setup UUID>` and `:audit` settings with all account/directory
receipts. Complete lifecycle, restore and production release remain gates.

Module 0.15.0 exposes this ordinary creation protocol at `POST /api/kyquickstart/creation`.
Provision `KYQ_BOOKSTACK_CREATION_TOKEN_FILE` as canonical application-owned 0600 JSON
`{"token":"<64 lower-case hexadecimal characters>"}` in a protected 0700 directory outside
public/upload roots. Its token must differ from both directory and offboarding credentials.
Provision `KYQ_BOOKSTACK_CREATION_POLICY_FILE` with the same private-file protection:

```json
{"installation_id":"271333e2-c221-4601-b249-563b3d828530","issuer":"https://identity.example.test","client_id":"bookstack-client","everyday_role":"editor","ordinary_role_ids":[2,4]}
```

Every call must match this fixed policy. Use verified HTTPS and `Authorization: Bearer`.
Both `probe` actions return `ky.bookstack-creation-absence/1`, binding the absence result to
installation/account UUIDs, source and subject digests and ordinary roles. Probe is read-only:
any ownership, subject/email collision or revocation fence prevents absence. An absent result
is permission to attempt the transaction, which rechecks collisions, rather than a reservation.
The Go `CreationHTTPClient` probes before mutation and checks exact creation receipts when
absence is false. A transport/5xx/bad-proof result after a write raises `ErrCreationUncertain`;
retry inspects immutable ownership before another create. Preserve the identical original
spec for recovery. Prior setup accounts cannot be adopted. Cookies and native API tokens
are refused; this credential grants no directory projection, revocation or content access.
Back up creation token/policy with all receipts. The resident bridge additionally requires explicit protected creation opt-in and native scope lookup.

All scoped HTTP endpoints return 503 for database exceptions and failures while reporting
a completed native operation. Such a response leaves the outcome uncertain even when a
particular failed transaction rolled back; inspect native receipts before retrying.

Module 0.15.0 adds read-only `ky.bookstack-creation-scope/1` at the same scoped endpoint:

```json
{"contract":"ky.bookstack-creation-scope/1","action":"resolve","installation_id":"271333e2-c221-4601-b249-563b3d828530","account_setup_id":"57a9c424-6499-4bfe-8f66-458b1c1136f0","issuer":"https://identity.example.test","client_id":"bookstack-client","subject":"exact-new-subject","everyday_role":"editor","ordinary_role_ids":[2,4]}
```

`resolve` verifies assignment/account intent, immutable creation/audit joined to installation
and ordinary policy, and current profile/role proof before returning the unchanged native ID
and setup UUID. It does not need an original or projected name/email, and it never adopts a
prior setup account. `subject-probe` checks only subject existence and expected owned receipt/
audit keys; malformed/orphan ownership and revocation deny absence. Account birth still
separately checks email and immutable profile intent. Both operations preserve retained
state and enforce the same source and private policy. Native scope resolution lets the
bridge manage created accounts after restart without changing its initial protected map.
