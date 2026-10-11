# LibreChat native account inspection

Community module 0.1.0 targets LibreChat v0.8.8 commit
`e8f3be08623663d4ad7f7241e693c94469b63bb0`. It adds the identity readback missing from the
stock admin list. This is an implementation candidate, not a qualified container release.
No live application has been changed.

Copy `account.cjs` and `command.cjs` into a product-owned private module directory in the
application image. Run as the same application user. Native packages resolve from `/app`,
or canonical `KYQ_LIBRECHAT_APP_ROOT`. The CLI checks the native package name/version.
It reads the target's existing native environment and `.env`, requires the exact configured
`OPENID_ISSUER` and refuses token reuse. Database credentials never enter installer state.

`node /app/kyquickstart/command.cjs --capabilities` reports only `account-inspection` without
connecting, reading native environment files or modifying anything. For inspection, stage
an application-owned 0600 request beneath protected nonsymlink parents outside client,
public, uploads and images roots:

```json
{"contract":"ky.librechat-account-inspect/1","user_id":"0123456789abcdef01234567","issuer":"https://identity.example.test","subject":"exact-native-subject"}
```

Run `node /app/kyquickstart/command.cjs --file <absolute-path>`. The product uses native
`getUserById` under `runAsSystem`, selecting only identity and role. Automatic collection and
index creation are disabled before connecting; model seeding is never invoked. A successful
report binds the native ID and SHA-256 of `issuer + NUL + subject` and reports ADMIN or USER.
It includes no profile, subject, tokens or database connection string. Missing accounts,
foreign providers/sources/subjects, malformed files and unknown/custom roles refuse.

The CLI registers native schemas with `createModels` and obtains the product's shared native
method from `createMethods` directly, avoiding API-loader cache initialization. The native
library opens its configured log directory (`LIBRECHAT_LOG_DIR` when set); retain the existing
native logging path and permissions. This does not authorize changing application configuration.

The Go consumer owns a fresh private request directory per call, discovers the interface,
validates bounded reports and cleans up its request on success or failure. Its executor is
already bound to an explicit container/application user. Inspection does not adopt an account,
grant administration, publish setup ownership, configure OIDC, revoke access or reactivate anyone.
Native role readback alone does not prove actual administrator authorization or separation.

Unit checks execute the private-file reader and inspection with a substituted native model
boundary, plus real shell/Node staging. The parent disposable fixture additionally executes
the built pinned native schemas/methods and production CLI/SDK against MongoDB 8.0.20. It
verifies ordinary/admin readback and foreign/local/missing refusal, compares every collection,
index and document before/after, and checks repeat/refusal and request cleanup. Native source
is mounted read-only and the fixture has an isolated log destination. This proves inspection,
not native server login/authorization. Consistent identity writers, persistent revocation and authorization hooks, initial setup,
actual OIDC/administrator proofs, backup/restore and release provenance remain gates. The
installer catalog and resident bridge do not enable LibreChat based on this module.

The internal `revocation.cjs` now accepts `ky.librechat-revocation/1` with exact account
binding and `check`/`revoke`. It atomically adds `kyquickstartRevocation` to the matching
native User document: source/subject digest, native ID, operation UUID, publication time and
local publication audit action. This binds identity and record in a single journaled majority
write on standalone MongoDB. It refuses duplicate identities and never overwrites an existing
record. Session and agent API-key cleanup uses native methods only after verifying that record.
Lost publication/cleanup replies require inspection; repeat retains the operation UUID and
does not reset roles, profiles or content. Corrupt records remain refused.

Its `ky.librechat-revocation-proof/1` is a physical-state proof, **not an access-denial verdict**.
`active_sessions_remaining` counts active sessions; native deletion removes expired records
as well. Guard helpers deny recorded native IDs/subjects, corrupt records and failed lookups
without exposing native diagnostics. The real MongoDB fixture exercises committed lost replies,
partial cleanup, duplicate identity/corruption refusal and preserves the original native User
fields, other accounts' credentials and authored conversation content.

`source.patch` adds candidate server hooks through `runtime.cjs`: native JWT acceptance,
OIDC subject/account handling, token issuance, agent user resolution and optional cookie-based
file authentication. The disposable fixture applies this patch to owned copies and executes
the patched native Passport strategy against real MongoDB and JWT cryptography. Valid tokens
work before publication; invalid signatures and old/fresh valid tokens after publication are
refused while another account retains access. Only the application model alias is resolved
by the fixture, using actual native model methods. Native token issuance uses real native
session/hash/JWT methods, accepting both ObjectId and string IDs. The cookie middleware runs
through isolated Express HTTP with real native refresh cookies: authentication works before
publication and loses identity immediately afterward while sessions still exist; another
account keeps access. Refused token issuance changes no session. This is middleware/issuance
qualification, not the full LibreChat/OIDC server or actual share ACL. Configuration/email
fixture boundaries for issuance are unused refusals, not qualified integrations.

Candidate image packaging must place `runtime.cjs`, `writers.cjs`, `credentials.cjs`, `revocation.cjs` and `account.cjs` under
`/app/api/kyquickstart` so the native `~/kyquickstart/runtime.cjs` alias resolves. The CLI's
private module directory may remain separate. This is a packaging contract for a future
qualified release, not an instruction to alter a live image.

The patch also checks account state at common Passport success boundaries (including RUM)
and secure image session lookup. Native secure cookie image authorization gives 403 for the
revoked viewer before session cleanup and retains control access; anonymous access gives 401.
Common bearer HTTP denies the revoked user and preserves control access. Native image owner
lookup is retained so existing ownership/ACL information remains available. RUM/provider
reuse, legacy image reuse and full shared-image ACL remain unqualified; CLI token reuse is
still refused.

The same fixture loads the patched native agent middleware and real native authentication
factories with explicit API-key-enabled/OIDC-disabled configuration. Native generated keys,
hashes, user lookup and principal checks authenticate over isolated HTTP before publication.
Publication denies the retained valid key before cleanup; the key is still denied after
cleanup, while another account keeps access. Missing/invalid keys refuse. HTTP requests use
normal request context; LibreChat rejects system-context request routes. Email/agent permission
fixture boundaries refuse if exercised. This proves API-key authentication, not actual agent
authorization, configuration delivery or strict multi-tenant acceptance.

Signed remote OIDC and machine authentication are separately exercised with actual native
HTTPS JWKS retrieval and RS256 verification, native source/subject lookup and tenant-bound
client/user bindings. The owned worker trusts its disposable CA via `NODE_EXTRA_CA_CERTS`;
TLS verification is preserved. Publication denies both old/fresh signed user and machine
tokens while control accounts retain access. Signature, expiry, issuer/audience/scope and
client/subject mismatches refuse. TLS files and the key server are cleaned up. These are
native middleware proofs with explicit fixture configuration, not KyIdentity browser login,
production configuration delivery or full server authorization.

With an explicitly selected absolute `KYQ_TEST_IDENTITY_BINARY`, the fixture also runs actual
KyIdentity inside its owned worker, capped with GOMAXPROCS=1/GOMEMLIMIT=128MiB. Protected
target-local setup files create assigned-only client/users. Verified HTTPS serves both the
issuer proxy and callback; private callbacks are opted in only for the fixture. Native
LibreChat discovery/Passport/PKCE and userinfo processing create an ordinary account and
project the signed app ADMIN role. Fresh revoked-subject login refuses without changing
native users/sessions; control administrator login still works. The fixture uses explicit
config, native Keyv memory cache and refusing unused avatar/email boundaries. This qualifies
the exercised browser strategy and native issuance, not all native routes, actual administrator
capabilities/purpose, initial setup ownership, production config delivery or release provenance.

`writers.cjs` registers native User model hooks before compilation through both db entry
points, retaining their model exports. Query mutations, replacements, deletions, document
saves and bulk writes require absence of the publication record at the actual MongoDB write.
Ordinary model writers cannot supply/rename the record; update pipelines refuse. Publication
requires no prior native deletion fence and removes expiry atomically. Real native checks
retain the exact User through these attempts, including a document loaded before publication,
while active control writes and browser registration work. Raw collection access bypasses
these hooks and remains privileged. Raw maintenance/restore writers, concurrent new-account/
source races, credential creation and complete deletion-controller orchestration remain gates.

`credentials.cjs` coordinates native session/key writers with publication using bounded,
private per-write UUID claims on the same User. Source/fence/admission is one atomic invariant
on standalone MongoDB; publication and User mutations require no in-flight claims. Normal/error
completion releases only its owned key. Claims contain generation, PID, model and time, no
credentials. Cap 32. Saves and single-owner queries are guarded; batches refuse and ownership
cannot be removed/renamed/rebound. `credential_writes_in_flight` joins physical cleanup proof;
cleanup requires zero claims, including repeated revoke. Actual held native session/key
inserts block publication until release; creation after publication refuses before insertion.
Failed insertion releases, lost claim/release replies remain inspectable and corrupt claims
refuse. A lost reply never claims rollback. Dead-owner recovery requires proof the owner has
stopped, never age/PID alone. The internal `credential-recovery.cjs` primitive now requires
`all_writers_confirmed_offline: true`, exact native ID/issuer/subject, a new operation UUID and
an exact validated claim digest. Hash UTF-8 JSON of sorted tuples
`[UUID,generation,model,PID,started_at]`; ordinary object property ordering is irrelevant.
One journaled majority compare-and-set clears those claims and appends a private immutable
`kyquickstartCredentialRecovery` audit receipt. Cap 64 receipts; corrupted/full history and
stale state refuse. A repeated receipt resolves lost replies and reports newer pending claims
without clearing them. It retains credentials, profiles and conversations; normal revocation
subsequently publishes and cleans credentials. Model replacements/deletions preserve audited
accounts, while ordinary profile updates remain available.

The native fixture kills actual writers after MongoDB session insertion and repeats with a
fresh process generation. It checks stale/unconfirmed/source refusals, atomic lost replies,
old receipts against newer claims and retained data. This is an internal product maintenance
primitive, not a public command or an authenticated administrator interface. Stop every
replica/background writer before asserting offline confirmation. Packaging maintenance must
include `credential-recovery.cjs` with its account/revocation/credentials dependencies in a
product-owned private directory. Replica-stop orchestration, restore qualification, remaining
native controllers/batch maintenance and privileged raw writers remain gates.

`credential-lifecycle.native.cjs` also qualifies actual native provider session insert/repeat
upserts, refresh rotation/expiration and API-key last-use writes. Persisted document saves
reject owner changes and condition on the current owner. Query writes schema-cast the
native filter before raw inspection, then pin the inspected
credential ID/owner; no-match updates cannot mutate rows arriving afterward. Initial upserts
retain native MongoDB insertion semantics without duplicate owner equalities. Writes to an
inspected existing row disable upsert: a concurrent native logout remains a deletion.
Native races check late foreign-owner rows, broad queries and ownership changes at actual
MongoDB write boundaries. Published accounts refuse these mutations before credential cleanup;
control accounts keep working. Multi-row and batch writes refuse. These are native method
proofs, not complete browser/refresh controller, raw maintenance or public setup acceptance.

Alternate browser paths and complete native writer lifecycle are not behavior-qualified. The CLI
therefore continues to advertise only read-only inspection. Full server/session and signed
OIDC denial, coordinated writers, cleanup completion audit/native audit
relay and full setup/lifecycle remain gates. Back up the new User field with the native database;
the primitive never removes it or reactivates a recorded account.


`refresh-controller.native.cjs` additionally executes the actual native local-session
`AuthController.refreshController` over disposable HTTP/MongoDB with the patched native
issuance service. Session issuance and HTTP reads share ordinary tenant context. Follow
returned refresh cookies; the fixture verifies the current cookie still has a native session
before testing publication denial. Refresh accepts signed sessions/returns verifiable access
tokens before publication and gives 403 without cookies or User/session mutation afterward,
while credentials remain stored. Access stays denied after cleanup and control accounts
continue refreshing. Loader fallback uses clean pinned native files when no candidate patch
exists and binds the patched AuthService explicitly. Unused Graph/provider/bridge/flight
boundaries refuse; these are not fabricated provider successes. Provider-token reuse, native
refresh bridges, full server routing, administrator purpose and initial setup remain gated.
