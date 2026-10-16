# Immich owned setup and retained-account access extension

Community extension for Immich v3.2.0, source
`1b6098c9dbfffe978bec2d414606ed7a4c8e019a`, under upstream AGPL-3.0.
`source.patch` retains upstream copyright/license notices. This is unfinished installer
integration and a disposable qualification image, not an admitted production release.

`GET /api/admin/users/:id/kyquickstart/access` reports provider/account pins, immutable
installation ownership, revocation time and session/API-key counts. `POST
/api/admin/users/:id/kyquickstart/revoke` accepts only `setupId`, `issuer`, `clientId`,
and `subject`. Both require a native administrator API key with the corresponding
native user-read/update permission. A browser or bearer session alone is insufficient.
Self-revocation and mismatched identity/provider/installation pins are refused.

The product commits a write-once `ky.immich-access/1` fence in user metadata together
with deletion of that user's sessions and API keys. Fenced accounts cannot authenticate
with sessions, keys or shared links, or sign in again; subject denial survives OAuth
unlinking. Revocation disconnects the user's subscribed sockets. A second authentication
check after joining rooms closes the connection/revocation race. Account, asset and
album fields are retained. Repeats remove late unusable credentials and retain the first
revocation timestamp. There is no clearing/reactivation route. Requests authorized before
commit may finish; this is not cancellation of in-flight requests. Restore must retain
the metadata fence with the account and content.

Build in an isolated, disk-backed source checkout; `/tmp` on the development workstation
is RAM-backed. Use the pinned tag with a sparse checkout of `server` and `packages`,
confirm the exact commit, then apply `source.patch` with `git apply --check` followed by
`git apply`. From the source root:

```sh
export NODE_OPTIONS=--max-old-space-size=1536
pnpm --filter immich... install --frozen-lockfile --ignore-scripts --network-concurrency=2 --child-concurrency=1
pnpm --filter @immich/sdk build
pnpm --filter @immich/plugin-sdk build
pnpm --dir server exec tsc --noEmit
pnpm --filter immich build
```

Build `Dockerfile.fixture` with the source `server` directory as context and the SHA-256
of `source.patch` as `KYQ_SOURCE_PATCH_SHA256`; retain the `--iidfile` image digest.
The overlay keeps runtime dependencies and the web UI from the official pinned amd64
image and replaces server `dist` with this source build. Source/patch labels identify
fixture inputs; they do not constitute a production provenance attestation.

From this repository, set `KYQ_TEST_IMMICH_MODULE_IMAGE` to that immutable local
`sha256:` image digest and run the parent's native qualification command with
`KYQ_TEST_IMMICH_NATIVE=1`. Use a disk-backed `TMPDIR`, `GOMAXPROCS=2`,
`GOMEMLIMIT=1GiB` and `GOFLAGS=-p=1`. All accounts, photos, storage and network are disposable.
Existing live installations are never used.

Complete identity lifecycle, deployment composition,
dedicated administrator workflow separation, recovery/restore and production release
remain acceptance gates. The parent tracks the exact qualification evidence and limits.

## Native directory account creation

An installation marker makes native OAuth login require a provisioned account and owned
directory receipt. First login cannot create or adopt an account by email, even if the
native auto-registration setting is enabled. Managed OAuth link/unlink requests are refused
before provider exchange or native mutation. Stock installations retain upstream behavior.
New assignments use the protected directory creation opt-in below. Other native writers,
configuration changes and operations already in flight at initial setup still require
deployment coordination; this guard is not a database uniqueness constraint.

`GET /api/admin/users/kyquickstart/creation` accepts `setupId`, `issuer`, `clientId`
and `subject`; `POST` accepts those fields plus `profile` with `name` and `email`.
They require native administrator API-key read/create permissions. Session-only
administrator authority is insufficient. The installation receipt and current secure
OAuth provider must match. Existing subjects, retained subject receipts and email
collisions refuse adoption, including deleted or unlinked accounts.

Creation commits a passwordless ordinary account, its cluster group, a revision-zero
directory receipt and an immutable `ky.immich-creation/1` receipt in one serializable
transaction. The receipt binds its native UUID, original profile, provider/subject,
installation and intent digest. Native directory updates accept this independent
ownership proof as well as the two initial setup IDs. Administrator projection remains
a separate operation. Exact creation repeats verify ownership and current directory
state and retain later profiles/roles; they never reset authority or reactivate a fence.
Preserve both creation and directory metadata with database backups.

Known precondition conflicts return a fixed 409. Unexpected native failures return a
fixed-message 503: a database commit can succeed before its acknowledgment is lost.
The consumer holds unknown writes for reconciliation rather than assuming rollback,
retrying creation or adopting the resulting account.

Add `KYQ_TEST_IMMICH_CREATE=1` to the parent's setup/module qualification command to
exercise this native path on disposable data. Protected resident
`directory.create_accounts:true` separately enables verified ordinary creation through the
bridge; omission keeps it disabled. With actual producer mode, the same test flag also
drives real KyIdentity new-assignment delivery, subject-bound PKCE sign-in, global-disablement
session/key revocation and reopened creation/initialization proofs. Complete lifecycle, native-writer
coordination, audit/restore and production deployment remain gated.


## Container-local setup

`immich-admin apply-setup --file /protected/setup.json` accepts a target-local
`immich.setup/1` bundle with lowercase `setupId`, `provider` (`issuer`, `clientId`, `clientSecret`,
`rolesClaim`), and separate `administrator`/`everyday` objects (`subject`, `email`, `name`).
Subjects come from protected KyIdentity provisioning receipts. Client secrets stay in the
container bundle and native configuration; reports include only account UUIDs/subjects/roles.
The file must be an absolute canonical nonsymlink path, a regular 0600 file owned by the
command user, beneath root/user-owned parents without group/other write permission.
`IMMICH_CONFIG_FILE` installations require a separate migration and are refused here.

`--capabilities` reports the native bundle/version/features without a bundle. It refuses
combined setup flags. `--check` validates and reports plans without writes. `--resume` requires an existing
installation receipt. Initial setup accepts only a blank OIDC provider and absent accounts;
email/subject matches are conflicts. It creates passwordless native administrator/everyday
accounts and cluster groups, preserves unrelated accounts/settings and commits configuration
and an immutable `ky.immich-setup/1` system-metadata receipt in one serializable transaction.
Repeated CLI processes verify exact intent, UUIDs, current owned profiles/roles and active/unfenced accounts;
changes and revoked accounts require reconciliation. Setup cannot reactivate accounts.

Accepted risk (Yoshi, 2026-10-10): setup does not refuse pre-existing administrators and
does not disable `passwordLogin`. An admin created through Immich's first-run sign-up
before setup keeps password access. Run setup before Immich is reachable by anyone else.

Access inspection and revoke accept OIDC disabled, a rotated provider and inactive or
soft-deleted accounts. The binding comes from the stored fence, else the account's directory
receipt (setup, issuer, client, subject); live configuration is used only for accounts with
neither, so a rotated provider cannot revoke a receipted account or match another issuer's
subject. Restore keeps the fence. Unreceipted accounts still need the live issuer/client.

The provider enables native first-login auto-registration, which requires Assigned users only
in KyIdentity; it does not provide an ongoing directory lifecycle. Other native configuration
and account writers must be coordinated before production deployment. Dedicated administrator
ordinary-workflow denial, complete identity lifecycle, deployment and restore remain
unqualified. Preserve the setup receipt with native database backups and recovery drills.

The parent native fixture accepts `KYQ_TEST_IMMICH_IDENTITY_BINARY` for an explicit
static local KyIdentity build, together with `KYQ_TEST_IMMICH_SETUP=1`. It creates
disposable owned users and an assigned-only client, then tests genuine PKCE code
exchange against the module-created native accounts. Administrator API access,
everyday refusal, unassigned ordinary/global-administrator denial and permanent
fresh-code denial after revocation are checked. Identity runs only on the fixture
network behind its own trusted TLS proxy. Without producer mode, bridge events come from
a controlled SCIM helper. `KYQ_TEST_IMMICH_PRODUCER=1` additionally uses actual identity
worker mutations, durable acknowledgments and reopened protected runtime proofs for owned
profiles/roles/assignment-loss revocation. A pinned private SSH fixture forwards raw TCP to
host loopback without changing host firewall rules; TLS remains verified end to end.
The local binary and overlay provide test evidence rather than production release provenance.


## Owned directory projection

Initial setup creates `ky.immich-directory/1` metadata on both native users with revision zero.
`GET /api/admin/users/:id/kyquickstart/directory?setupId=<UUID>` returns the current owned
profile/revision and explicit revocation state. `POST` to the same path accepts the exact
installation/provider/subject, `expectedRevision` and strict `profile` (`name`, lowercase
`email`, `administrator`). Both routes require native administrator API-key permissions;
self-updates are refused. Ownership comes from the initial setup IDs or an independently
verified native creation receipt. Arbitrary existing users are never adopted.

The product locks and validates native state in a serializable transaction. A changed profile
and its incremented receipt commit together; identical repeats retain the revision. Missing/
foreign receipts, stale revisions, native drift, duplicate emails and revocation refuse writes.
Original setup check/resume verifies the independently owned current projection without resetting
it to initial names or roles. Its report reflects current roles; workstation verification still
requires owner administration and everyday denial. Revoked accounts cannot be reset through setup
or directory updates. Older local overlays without directory metadata need explicit reconciliation;
this module supplies no automatic adoption/migration.

A genuine OAuth role conflicting with the current managed receipt fails login rather than
restoring a stale grant. Missing or empty roles permit ordinary access only for a verified
non-administrator receipt; unknown/malformed roles and missing administrator authority
are refused. Demotion denies existing-session administration. Preserve both initial
and directory metadata with native database backups. Directory receipt revision is an outcome
proof; complete connector audit/recovery and production acceptance remain gates. Protected
resident directory mode consumes these receipts for profile/role updates on registered owned
accounts. Real KyIdentity registered-account profile/role/revocation delivery is qualified
through the protected resident fixture. Separate protected creation mode also qualifies
actual new assignment, ordinary subject-bound PKCE sign-in and global-disablement revocation
with reopened creation/initialization proofs. The optional `KYQ_TEST_IMMICH_MULTI=1` producer/create fixture additionally verifies one
actual global disablement across native Immich and Forgejo, retained content, independent
app assignment loss and reopened per-product proofs. Full-suite connector coverage, native-writer
coordination and complete connector audit/restore still need work. This is qualification
of local artifacts, not a production release.


Known OAuth token-endpoint grant denials (`invalid_grant` or `access_denied`, 4xx only)
return a fixed 401 without logging provider diagnostics. This covers codes rejected after
assignment loss or expiry before native claims arrive. Invalid-client, transport and server
failures remain failures; an error-shaped 5xx is not treated as a user denial. The producer
fixture separately checks refusal to issue a new authorization after assignment loss.
