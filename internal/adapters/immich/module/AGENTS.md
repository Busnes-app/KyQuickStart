# Purpose

Owned installation setup, native directory projection and content-preserving access revocation inside pinned Immich v3.2.0.

## Ownership

- `source.patch`: container-local setup command, atomic native configuration/account and creation receipts,
  administrator API, atomic revisioned directory projection, transactional fence/credential removal and authentication denial.
- `Dockerfile.fixture`: disposable image overlay for native qualification; no production release.
- `README.md` and `LICENSE`: build/restore limits and upstream AGPL-3.0 licensing.
- The parent owns the HTTPS consumer and qualification; the root owns admission and deployment.

## Local Contracts

- Apply only to source commit `1b6098c9dbfffe978bec2d414606ed7a4c8e019a`.
- This is an AGPL-3.0 community source extension, not an upstream capability claim.
- `immich-admin apply-setup --file <absolute-path>` reads `immich.setup/1` from a 0600
  regular file owned by the command user, beneath protected nonsymlink parents. Installation
  UUIDs are lowercase to match the workstation report contract. Check is
  read-only; resume requires an existing immutable `ky.immich-setup/1` receipt.
- `--capabilities` reports the module's bundle/version/features without a file or writes;
  it cannot combine with setup/check/resume flags. Stock Immich lacks this contract.
- Initial setup requires a blank native OIDC provider and absent administrator/everyday
  subjects/emails. Configuration, both native accounts/cluster groups and the receipt commit
  in one serializable transaction. Existing accounts are conflicts, never adoption candidates.
- Repeat verifies exact initial intent hash and native UUIDs/subjects, plus current profiles/roles
  against the independently owned directory receipts and active passwordless/unfenced accounts.
  Drift requires reconciliation; setup never restores access.
- Setup is refused with IMMICH_CONFIG_FILE. Retain unrelated native settings and accounts.
  External native configuration/account writers still require coordination for deployment.
  Dedicated administrator ordinary-workflow separation and complete identity lifecycle remain gates.
- Initial setup also creates `ky.immich-directory/1` per-user metadata at revision zero.
  Native GET/POST directory endpoints require administrator API-key permissions and owned
  initial account UUIDs or independently verified creation receipts. Updates compare the expected revision, provider/subject, current
  native profile and immutable setup before atomically writing name/email/administrator and
  the next receipt. Revoked accounts, self-updates, missing receipts and unsafe config-file
  installations are refused. Inspection can prove retained revoked profiles; it restores no access.
- `GET/POST /api/admin/users/kyquickstart/creation` require native administrator API-key
  read/create permissions. Pin the immutable installation and current secure provider.
  Absence includes retained subject receipts after OAuth unlinking; subject/email collisions
  never authorize adoption. Creation commits a passwordless ordinary user, cluster group,
  revision-zero directory receipt and immutable `ky.immich-creation/1` receipt together.
  The creation receipt binds native UUID, provider/subject, original profile and intent digest.
  Exact repeats verify current owned profile/authority, retain identity and never reset later
  projection. Revoked, deleted, unreceipted or conflicting accounts refuse repeat; there is
  no reactivation path. Known precondition conflicts return a fixed 409; unexpected native
  failures return fixed-message 503 because commit acknowledgment may have been lost.
  Never treat an unknown transaction outcome as a definite rollback.
  Protected bridge creation is a separate per-app opt-in with verified durable initialization
  before acknowledgment. Full connector admission remains independent of this native API.
- Verified OAuth `invalid_grant`/`access_denied` token responses with 4xx status return 401
  with a fixed message and no provider diagnostic logging. Invalid-client, transport and
  server failures retain error semantics; never classify a 5xx grant response as user denial.
- Existing owned OAuth users must match their current directory authority; missing/empty roles
  confer ordinary access only when that owned receipt is non-administrative. Explicit roles
  must be known `admin`/`user` values. Unknown/malformed claims, absent administrator roles
  and stale signed grants refuse login; OAuth cannot overwrite a projected grant or native drift.
- Presence of the installation marker requires an existing OAuth account and owned directory
  receipt before login. Native first-login registration and email linking cannot create/adopt
  an account in managed mode, even when OAuth auto-registration is enabled. Link/unlink routes
  refuse managed identity changes before exchanging a code or updating sessions/accounts.
  Database/marker read failures deny access; stock installations retain upstream behavior.
- Require a native administrator API key and exact issuer/client/subject; refuse self-revocation.
- Bind access inspection/revocation to the stored fence, else the directory receipt (including
  setup ID); only unreceipted accounts use live issuer/client. Never require enabled OIDC or an
  active/undeleted account there. Grant, sign-in, creation and directory paths keep those checks.
- Persist `ky.immich-access/1` in native user metadata, write-once to one installation binding.
- Commit the fence and scoped session/key deletion together. Preserve native user/content fields.
- Fence metadata presence denies all authenticated access; pin-based subject denial survives OAuth unlinking.
- No reactivation or content-deletion route exists. Repeats retain the first revocation time.
- Disconnect subscribed user sockets and recheck authentication after joining rooms.
- Requests already authorized before the fence commits may finish; do not claim cancellation of in-flight work.

## Work Guidance

Use disk-backed scratch and the parent's resource limits. Keep the patch reproducible from the pinned checkout.

## Verification

- Build SDK, plugin SDK and server in upstream order; type-check the complete server and lint changed source.
- Native setup qualification lives in the parent fixture with `KYQ_TEST_IMMICH_SETUP=1`;
  it requires the current immutable module image and does not use a live installation.
- `KYQ_TEST_IMMICH_CREATE=1` additionally exercises native atomic ordinary creation,
  scoped collision rollback, exact repeat, independent directory/role projection and retained
  permanent revocation. Controlled native API calls do not qualify actual worker provisioning.
- With actual producer mode, the same create flag also qualifies new identity assignment,
  native creation receipt, PKCE sign-in with empty ordinary roles, global-disablement session/key removal
  and reopened runtime proofs. Full lifecycle/restore/native-writer coordination remain gates.
- Owned OAuth role tests qualify missing/empty ordinary roles and explicit grants while
  refusing stale/missing administrator and unknown/malformed role claims without native updates.
- Managed OAuth tests refuse unprovisioned subjects, unreceipted users and link/unlink changes
  without native writes. Synthetic native setup mode checks signed first-login refusal and
  retained subject absence; both provider modes check link/unlink denial and stable ownership.
- `oauth.repository.spec.ts` uses a real local OAuth discovery/token exchange to distinguish
  known grant denials from invalid-client, server and transport failures without log leakage.
- Creation service tests preserve known conflicts, fence lost database commit acknowledgment
  as 503 and refuse session-only authority before repository access. Transactional creation
  and ownership are qualified against the real server/database in the parent fixture.
- Apply-check `source.patch` against a clean pinned checkout.
- Native fixture image labels identify the source/patch used; labels are fixture metadata, not release attestations.

## Child DOX Index

None.
