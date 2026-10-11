# LibreChat native inspection and qualification

## Purpose

Build native identity inspection and qualify retained-account lifecycle prerequisites before deployment.

## Ownership

- `native_qualification.cjs` executes pinned upstream native ban, JWT and admin-route modules.
- `native_mongo_qualification.py` owns disposable real native package/model/CLI/SDK inspection
  against MongoDB and complete collection/index/document nonmutation checks.
- `client.go` owns bounded private container-local inspection calls and exact native report pins.
- `module/` owns community native inspection and internal atomic retained-revocation primitives;
  candidate source guard integration, public revocation and production release remain gated.
- The parent owns third-party admission and adapter boundaries.

## Local Contracts

- Pin LibreChat v0.8.8 commit `e8f3be08623663d4ad7f7241e693c94469b63bb0` and native source hashes.
- Native temporary bans and destructive account deletion do not satisfy retained offboarding.
- Require exact native ID, OIDC issuer and subject inspection before account mutations.
- The source prerequisite fixture substitutes storage and dependency boundaries; it is not a running MongoDB,
  HTTP, signed-token or complete product qualification. Never claim admission from it.
- No setup adapter or resident connector is enabled until the missing native interfaces exist.
- Inspection reports native role only. It proves neither product setup ownership nor native
  administrator capability coverage; dedicated administrator ordinary-workflow separation remains gated.

## Work Guidance

Reuse native OIDC claim-based administration. Build retained revocation and ownership inspection
inside the product; the installer must not edit MongoDB or infer ownership from email.

## Verification

- `KYQ_TEST_LIBRECHAT_SOURCE=<checkout> go test -race ./internal/adapters/librechat` runs
  the same pinned native probe with a bounded Node heap and 20-second timeout.
- `node --max-old-space-size=256 internal/adapters/librechat/native_qualification.cjs <checkout>`:
  requires the pinned upstream checkout; verifies temporary-ban expiry, JWT strategy behavior
  after refresh-session deletion and native user-route registrations.
- `native_mongo_qualification.py` requires disk-backed `TMPDIR`, the clean pinned source with
  built native data-provider/data-schemas, and `KYQ_TEST_LIBRECHAT_NODE_IMAGE` /
  `KYQ_TEST_LIBRECHAT_MONGO_IMAGE` digest pins. It runs real native CLI/SDK checks in owned,
  unexposed containers (Node 512 MiB, MongoDB 768 MiB, one CPU each), then removes its containers,
  anonymous volumes and network. It does not qualify full native server, signed OIDC or
  administrator access.
  It also runs the internal revocation primitive against native schemas/methods, verifying
  lost replies, retained records/content, generic guard failures and credential cleanup.
  It applies candidate source hooks to disposable copies and executes the native Passport JWT
  strategy with actual crypto/models, proving old/fresh token denial after record publication.
  It also qualifies native token/session issuance and refresh-cookie identity middleware
  over isolated Express HTTP, including denial before cleanup without new session creation.
  Patched remote-agent API-key mode also runs through actual native factories over isolated
  HTTP, with generated native keys/hashes, denial before/after cleanup and control account access.
  Remote OIDC/M2M additionally use actual native HTTPS JWKS/RS256 verification, scoped native
  user/client bindings and old/fresh token denial. The fixture owns/removes its TLS key/cert
  and trusts that CA only in the disposable worker. Optional `KYQ_TEST_IDENTITY_BINARY` adds
  actual KyIdentity HTTPS discovery/assigned authorization/PKCE and native LibreChat browser
  callback, ordinary registration and signed app ADMIN role projection. Revoked-subject
  login refuses before user/session changes; owner login remains accepted. This uses fixture
  config, native Keyv memory cache and unused refusing avatar/email boundaries. Full server,
  alternate browser paths, administrator capability/purpose and writer coordination remain gates.
  Secure image cookie authorization and common bearer HTTP are additionally qualified with
  denial before cleanup and control access. Image owner lookup is retained; RUM, reused
  provider tokens, legacy image reuse and complete shared-image ACL remain unqualified.
  User model query/save/bulk guards retain the exact published account under native update,
  replace/delete and stale-save attempts while active control writes work. Actual patched db
  model exports are checked. Publication refuses prior deletion admission and removes expiry.
  Raw writers, new-account/source races and complete credential lifecycle remain gates.
  Native session/key write admission now coordinates with publication on the same User:
  actual held inserts block publication until release and new creation refuses afterward.
  Failed inserts and lost claim/release replies remain inspectable; corrupt state refuses.
  Internal confirmed-offline recovery additionally qualifies killed/restarted native Node
  writers, atomic claim/audit updates, stale/source refusals and inspectable lost replies.
  Public maintenance authority, replica-stop orchestration, restore and remaining credential
  lifecycle/raw writers remain unqualified; see module contracts.
  Native provider upsert/repeat, refresh rotation/expiry and API-key last-use writes are now
  additionally qualified under exact credential/owner admission. Late-match, foreign-owner
  upsert and concurrent native logout races preserve scoped writes and retained revocation.
  Multi-row/batch mutation refuses; full controller and maintenance flows remain gates.
  Actual native local-session AuthController HTTP additionally qualifies signed refresh,
  returned-cookie rotation, immediate retained-session denial before cleanup and control
  refresh access. Fixture tenant context is explicit; provider reuse/bridges, full routing
  and setup ownership remain gates.

## Child DOX Index

- `module/AGENTS.md`: private inspection, retained revocation and confirmed-offline recovery contracts.
