## Purpose

Product-owned account inspection and internal retained-revocation primitives for pinned LibreChat.

## Ownership

- `account.cjs` owns strict request validation, private-file reads and native identity reports.
- `command.cjs` connects through the product's own models and exposes the read-only CLI.
- `revocation.cjs` owns the atomic native account/subject publication record, local publication
  audit, native credential cleanup and guard functions. `revocation.native.cjs` is a disposable
  real-MongoDB check, not an installed runtime command.
- `runtime.cjs` and `source.patch` own candidate native authentication hooks. `jwt.native.cjs`
  executes patched native Passport, token issuance, cookie and remote-agent API-key middleware
  with native models, real cryptography and isolated Express HTTP; remote OIDC/M2M use
  native HTTPS JWKS verification.
- `browser.native.cjs` owns disposable actual KyIdentity and native LibreChat Passport/PKCE
  callback qualification, including ordinary registration and signed app-role projection.
- `writers.cjs` owns native User model query/save/bulk guards installed before compilation;
  raw publication remains inside `revocation.cjs`.
- `credentials.cjs` owns native session/key write admission and bounded private in-flight
  records on the User; publication joins that state atomically on the same account.
- `credential-recovery.cjs` owns internal confirmed-offline claim recovery and its atomic
  local audit receipt; `credential-recovery.native.cjs` qualifies killed/restarted owned writers.
- `credential-lifecycle.native.cjs` qualifies native session upsert/rotation/expiry and API-key
  last-use writes, exact owner admission and late-match/deletion races against real MongoDB.
- `refresh-controller.native.cjs` owns actual native local-session AuthController HTTP
  qualification with the patched issuance service and scoped native MongoDB sessions.
- The parent owns the Go consumer, source qualification and remaining admission gates.

## Local Contracts

- Community module 0.1.0 supports only `ky.librechat-account-inspect/1`; it does not advertise
  initial setup or revocation and must not enter the deployable catalog yet.
- Internal `ky.librechat-revocation/1` takes exact native ID/source/subject and check/revoke.
  A native User compare-and-set pins the currently matching identity and publishes
  `kyquickstartRevocation` plus its local publication audit in one journaled majority write.
  Identity/fence/audit binding is one atomic invariant; no extra shared lock or global counter.
  Duplicate native source/subject accounts refuse. Module repeats never replace the record.
- Publication requires no native agent-deletion fence and removes `expiresAt` atomically.
  Native model guards add record-absence conditions to query mutations, replacements,
  deletions, document saves and bulk operations. Ordinary model writers cannot supply or
  rename the publication field; update pipelines refuse. Guard registration is idempotent
  and refuses first installation after User model compilation. Native db entry points must
  retain their model exports. Direct collection access bypasses hooks and stays privileged;
  raw maintenance/restore writers and cross-account creation races remain unqualified.
- User source/fence and credential admission are one atomic invariant on standalone MongoDB:
  a writer claims one UUID key in `kyquickstartCredentialWrites` only before publication;
  publication and User mutations require no in-flight claims. Normal/error completion
  releases only that owned key. Cap 32; validate record shape, generation/PID/model/time.
  Claim/release use journaled majority writes and store no credentials. Unknown replies
  require inspection; never automatically reclaim a claim based on age or PID alone.
  Session/key save and single-owner query writers are guarded; batch credential writes refuse.
  Native credential ownership cannot be removed, renamed or rebound to another User.
  Persisted document saves reject modified ownership and condition writes on the current owner.
  Query writes schema-cast before raw inspection and pin the credential ID and owner;
  a non-upsert no-match remains a
  no-op if a row arrives later. Initial upserts require the admitted owner and preserve one
  equality per path for native MongoDB insertion. Existing-row writes disable upsert so
  concurrent logout/deletion cannot resurrect a credential. Multi-row/batch writes refuse.
- Internal `ky.librechat-credential-recovery/1` requires explicit confirmation that ALL
  writers/replicas are stopped, exact native source/subject and a SHA-256 claim-map digest.
  Digest canonical sorted tuples `[UUID,generation,model,PID,started_at]` as UTF-8 JSON.
  Atomically clear the exact map and append one immutable private
  `kyquickstartCredentialRecovery` receipt (cap 64) with operation UUID, binding/digest,
  cleared count, offline confirmation and time. Reject stale state, duplicate identities,
  corrupt history, deletion admission and exhausted history. Retry a lost reply by receipt;
  an old receipt reports current pending claims and never clears newer ones. Retain native
  credentials/profile/content; normal revocation still publishes before credential cleanup.
  Ordinary User writers cannot alter recovery metadata; replacements/deletions preserve
  audited accounts. Profile updates remain available. This is an internal maintenance
  primitive, with no public CLI or authenticated replica-stop workflow yet.
- Publish/inspect the record before native session and agent API-key cleanup. Unknown write
  or cleanup replies require inspection; never claim rollback. Keep native user, role/profile
  and authored content. Corrupt/mismatched records refuse rather than being repaired.
- `ky.librechat-revocation-proof/1` proves physical record and current credential counts only.
  It has no disabled/access-denial verdict. Active-session counts exclude already expired
  sessions; cleanup uses the native all-session deletion. Guard functions query native
  account/subject records without cache and refuse corrupt records or lookup failures.
  `session_and_agent_keys_cleared` covers those two native stores only, not stateless JWTs,
  passwords, provider tokens or other credentials.
- `credential_writes_in_flight` reports validated claim count. Physical cleanup success
  requires zero claims; inspect claim state again before cleanup, including repeated revoke.
- Candidate source hooks cover JWT acceptance, OIDC subject/account handling, native token
  issuance, agent user resolution and optional cookie file authentication. JWT strategy,
  token issuance, refresh-cookie middleware and remote-agent API-key authentication are
  behavior-qualified, as are native remote OIDC and machine-token authentication; full
  server authorization, alternate browser paths and native writer coordination
  remain gates. Keep the public CLI read-only until complete integrations pass.
- Runtime account guards accept native ObjectId or string IDs; they use the existing native
  connection and uncached model queries. Token issuance must refuse before session creation.
  Back up the new User field with the native database. Publication audit is local to that
  record; cleanup completion auditing and native audit relay remain gates.
- Common Passport success boundaries (including RUM) check current account state. Secure
  image session lookup checks the viewer without hiding native owner records; retain existing
  ownership/ACL information after revocation. Cookie image and ordinary bearer HTTP paths
  are qualified before cleanup; RUM/provider-token reuse and legacy image reuse are not.
- Require exact native ObjectId, configured HTTPS issuer and stored OIDC subject/provider.
  Inspect only explicit native ADMIN/USER roles; unknown/custom authority needs qualification.
- Request files are canonical, nonsymlink regular 0600 files owned by the process beneath
  protected root/process-owned ancestors; public/upload paths are refused. Bound bytes before parse.
- Inspection uses native `getUserById` under `runAsSystem` with only identity/role fields.
  It neither seeds models nor creates accounts, collections, indexes or receipts. Disable
  native automatic collection/index creation before connecting.
- Register native schemas through `createModels` and obtain the native method through
  `createMethods` directly. The broad API model loader initializes unrelated cache stores.
  Preserve the product's native log destination; opening native log files is separate from
  database inspection and is not a collection/configuration mutation.
- The CLI refuses a noncanonical application root or native package metadata other than
  LibreChat v0.8.8. Capability discovery reads no native environment/database/request file.
- Inspection reports contain only native ID, role, module version and source/subject binding digest.
  Errors contain no request, database credentials or native exception text.
- Real native package/MongoDB/CLI inspection is qualified by the parent fixture. Full native
  server authorization, persistent revocation, setup ownership and production release remain gates.

## Work Guidance

Persistent revocation must precede any revoke interface and cover native authorization paths.
Inspection alone proves neither installer ownership nor dedicated administrator separation.

## Verification

- Parent `node --test module/account.test.cjs` checks native identity validation and actual
  filesystem private-file boundaries; `module/writers.test.cjs` checks every Mongoose 8 query
  write/delete op is fenced. Parent real MongoDB fixture covers native model transport.
- Parent MongoDB fixture also runs `revocation.native.cjs`: actual lost publication/cleanup
  replies, duplicate identities, corrupt record refusal, generic lookup failures, scoped
  native credential removal, same-operation repeat and retained user/chat content.
- The fixture applies the complete patch to disposable source copies, never the pinned checkout.
  Native Passport/JWT/MongoDB checks accept signed tokens before publication, deny old and
  freshly signed tokens afterward, reject invalid signatures and preserve another account access.
  Model alias resolution uses actual native methods. Token issuance uses native session/hash/
  JWT methods and both ObjectId/string IDs. Real refresh cookies pass isolated Express HTTP
  middleware before publication, then lose identity before credential cleanup; another account
  keeps access and refused issuance changes no session. This is not the full LibreChat/OIDC
  server or share ACL qualification. Unused configuration/email fixture boundaries refuse.
- Remote-agent API-key checks use native generated keys/hashes and actual user/principal
  methods. Publication denies a retained valid key before cleanup; missing/invalid keys
  and deleted keys refuse, while another account keeps access. The explicit fixture config
  enables API keys and disables OIDC; email/agent permission boundaries refuse if exercised.
  Run HTTP requests in normal request context, never `runAsSystem`: native tenant middleware
  correctly rejects system-context request routes. This does not prove agent authorization,
  strict multi-tenant acceptance.
- Signed remote OIDC/M2M uses actual native HTTPS JWKS retrieval and RS256 verification;
  trust only the owned fixture CA through `NODE_EXTRA_CA_CERTS`, never disable TLS checks.
  Check issuer/audience/scope, signature, expiry and client/subject bindings. Published
  revocation denies old/fresh signed user and machine tokens while control accounts keep
  access. Native tenant-bound M2M lookup uses seeded fixture users and explicit configuration.
  This is neither KyIdentity/browser login nor production config delivery or full server
  authorization. Delete fixture TLS keys/certificates and close the owned key server.
- Optional `KYQ_TEST_IDENTITY_BINARY` runs the actual KyIdentity binary inside the owned
  worker (GOMAXPROCS=1/GOMEMLIMIT=128MiB), with protected target-local setup data. Both issuer
  proxy and callback use verified fixture HTTPS; private callback opt-in is fixture-only.
  Check actual assigned authorization, PKCE, native ordinary registration, signed app ADMIN
  role projection and revoked-subject refusal before user/session changes. Use native Keyv
  memory storage for the fixture cache and refusing unused avatar/email boundaries.
  This proves the exercised browser strategy and native issuance, not full application
  routes, administrator capabilities/purpose, setup ownership or production release.
- Native secure image cookie access uses actual native authorization/model/session methods:
  publication gives 403 before session cleanup, while control images remain accessible.
  Anonymous secure image access gives 401. Common bearer HTTP also denies the revoked user
  and preserves control access. This is not shared-image ACL or provider-reuse qualification.
- Real native writer checks deny update/identity/delete/replace/bulk and a document loaded
  before publication, retaining the exact native User. Active control update/delete and
  native browser registration still work. Publication refuses an existing deletion fence;
  later native deletion admission cannot acquire the revoked account. Complete credential lifecycle,
  raw maintenance, concurrent new-account/source races and full controller deletion remain gates.
- Real native session/key inserts held after MongoDB insertion block publication and User
  mutation until release. After publication new native creation refuses before insertion.
  Failed insert releases admission; a lost claim reply retains inspectable blocking state;
  a lost release reply exposes the committed credential without claiming rollback.
  Corrupt claims refuse, including on an already published record. Internal offline recovery
  additionally kills actual native Node writers after MongoDB insertion, including a restarted
  process generation. Check stale/source/unconfirmed requests, corrupt/full audit history,
  lost recovery replies, old receipts against newer claims, protected metadata and retained
  credentials/profile/chat, followed by normal revocation cleanup. Public maintenance authority,
  replica-stop orchestration, restore, complete controller flows and raw writers remain gates.
- Actual native local-session refresh controller accepts a signed session and issues a
  verifiable access token/cookies before publication. Follow every returned refresh cookie;
  prove the current cookie still has a native session before checking revoked access.
  Published revocation gives 403/no cookies without User/session mutation before cleanup;
  afterward access remains denied while control refresh works. Issue and serve in ordinary
  fixture tenant context, never system request context. Loader binds the actual patched
  AuthService and refuses unused Graph/provider/bridge/flight boundaries. This does not
  qualify reused provider tokens, refresh bridges, full server routing or setup ownership.
- Actual native methods additionally qualify provider session insert/repeat upserts, refresh
  rotation, expiration changes and API-key last-used updates. Published accounts refuse these
  writes while credential rows still exist; control accounts remain usable. Document/query
  owner changes, late-match broad/no-match races, foreign-owner upsert races and concurrent
  native logout cannot redirect or recreate admitted credentials. Multi-row/batch writes
  refuse without mutation. Complete native controllers, batch maintenance and raw writers
  remain outside this qualification.

## Child DOX Index

None.
