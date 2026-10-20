# Resident offboarding runtime

`make build` produces `kyoffboard` alongside the installer. This runtime groundwork supports
BookStack, Forgejo and community-module Immich revocation. An explicit Forgejo or Immich directory configuration additionally
updates profiles and app-scoped administrator grants on already registered native accounts.
Production admission still requires complete native lifecycle, actual KyIdentity connector
composition, audit/failure-alert deployment and restore proof.

## Private target configuration

The service reads `ky.offboarding-runtime/1` JSON from a canonical service-owned 0700 directory.
Every referenced file is a basename in that directory, a service-owned 0600 regular file with
no symlink. State is a separate canonical 0700 target directory. Neither directory is installer
workstation state. Existing files are read, never created or replaced by configuration loading.

```json
{
  "schema": "ky.offboarding-runtime/1",
  "setup_id": "271333e2-c221-4601-b249-563b3d828530",
  "listen": "127.0.0.1:8443",
  "state_dir": "/var/lib/kyoffboard/state",
  "cert_file": "server.pem",
  "key_file": "server-key.pem",
  "audit": {
    "origin": "https://pulse.example.test",
    "token_file": "pulse-source-token",
    "require_alerts": true
  },
  "apps": [
    {
      "app": "wiki",
      "kind": "bookstack",
      "origin": "https://wiki.example.test",
      "issuer": "https://identity.example.test",
      "product_token_file": "wiki-product-token",
      "scim_token_file": "wiki-scim-token"
    }
  ]
}
```

Use the installation's UUID, not the example. Targets refuse bindings from another installation.
Forgejo entries additionally require the native `source_id` already verified against the issuer.

Immich entries use `"kind":"immich"` and require the exact native OIDC `client_id` alongside
`origin` and `issuer`. Use the community retained-account module described in
[`internal/adapters/immich/module/README.md`](../internal/adapters/immich/module/README.md);
stock Immich lacks this interface. Its protected product token is a native administrator
API key with the required native user/config permissions. A registered Immich receipt uses
`native_uuid` and `client_id`, `native_id:0`, and no username or numeric source ID. Numeric
BookStack/Forgejo receipts keep their existing shape; foreign or mixed shapes are refused.
Registration inspects exact native identity/provider/module ownership before publishing state.
Runtime configuration loading itself remains read-only.

BookStack module 0.15.0 additionally has protected container-local revisioned profile and role
interfaces with native audit proofs. `kyquickstart:directory` initializes, inspects or projects
both in one native transaction; a role/audit refusal rolls back a preceding profile write.
Native role policy stays pinned, and signed login cannot overwrite its grant. A separate
protected `KYQ_BOOKSTACK_DIRECTORY_TOKEN_FILE` enables check/apply over HTTPS for already
initialized owned receipts; it must differ from the offboarding credential. Initialization
stays container-local. BookStack runtime directory mode is an explicit registered-account
opt-in; the real resident TLS service is qualified against native BookStack with a controlled
SCIM sender and reopened audit proofs. The separate producer mode qualifies actual identity
worker profile/role delivery, assignment-loss revocation and single-product global
disablement on registered accounts. New-account delivery requires the separate protected
creation opt-in below; groups and production deployment remain unqualified.
The offboarding token gains no profile or role projection
authority, and the directory token grants no revocation/content access.

Module 0.15.0 also provides atomic container-local ordinary creation (`kyquickstart:create`)
and a strict native Go SDK. Its installation/account receipts, validated ordinary roles and
initial directory audits commit together; prior setup accounts cannot be adopted. The native
creation fixture proves signed ordinary use and retained revocation. This supplies a native
prerequisite for new-account delivery. BookStack's resident creation opt-in additionally
requires its own protected creation credential and receipt-owned scope lookup.
Separate `KYQ_BOOKSTACK_CREATION_TOKEN_FILE` and protected fixed
`KYQ_BOOKSTACK_CREATION_POLICY_FILE` enable HTTPS probe/create/check over the same native
transaction. The policy pins installation/source/ordinary roles; the SDK checks matching
creation receipts before repeating an ambiguous result. Cookie/native/directory/offboarding
credentials cannot create, and creation credentials cannot project, revoke or access content.
Database exceptions and failures reporting a completed native operation return 503 and retain
the uncertain outcome; native receipt inspection is required before retry.
Back up these private files with immutable database receipts. This endpoint supplies a
prerequisite for resident birth; enable it explicitly as documented below.

For new BookStack accounts, set `create_accounts:true` and
`creation_token_file:"bookstack-creation-token"` inside the protected `directory` object.
The file holds only the separate 64-character lowercase hexadecimal credential, with the
same service-owned 0600 protection as other runtime tokens. The product-side policy must
pin the same installation UUID, issuer/client, everyday alias and ordinary-role IDs.
The bridge derives a separate stable native account setup UUID and requires joined native
receipts before publishing the unchanged numeric binding. Existing account maps stay fixed;
new accounts resolve their native ID/setup through verified receipts on each directory call.
Creation/initialization audit proofs remain mandatory across restart. Keep this token file
configured for scope lookup after setting `create_accounts:false`; that configuration enables
no new-account writes. Restore it with the product policy and all native/bridge receipts.
Actual worker birth, later profile projection, ordinary OIDC, global denial and retained
content are tested by combining BookStack producer and create fixture flags; complete
lifecycle, audit relay, restore and production remain gates.

For a BookStack app, add this object to its protected runtime configuration:

```json
"directory": {
  "administrator_role": "admin",
  "everyday_role": "editor",
  "create_accounts": false,
  "token_file": "bookstack-directory-token",
  "bookstack": {
    "client_id": "bookstack-client",
    "ordinary_role_ids": [2],
    "accounts": {
      "exact-identity-subject": {
        "native_id": 3,
        "account_setup_id": "43f864f0-991c-43bf-8b94-d93c2916a98f"
      }
    }
  }
}
```

Use verified native role IDs and account receipts, rather than the example values. Each
subject has its own native account setup UUID; the top-level runtime setup UUID continues
to identify the bridge installation. This mapping does not rewrite old numeric bindings.
An account may supply its own `ordinary_role_ids` alongside its setup UUID. Absent/null
uses the app policy; an explicit `[]` preserves an administrator-only account with no ordinary
roles. These policies are protected configuration, never SCIM input.
Registration checks both native access and initialized profile/role receipts. The private
runtime token file contains the raw directory token (one optional trailing newline), unlike
the product's token JSON file; retain both protected copies in sealed backups. Every runtime
token and basename must be distinct. BookStack groups remain refused. Informational
SCIM userName never selects an account; reads retain its subject. Unknown writes and failed
post-write proof hold durable jobs across restart for explicit reconciliation. Revocation
remains available through its separate retained binding and credential.

The disposable BookStack OIDC fixture's `KYQ_TEST_BOOKSTACK_DIRECTORY=1` exercises this
runtime configuration and actual native profile/grant/demotion/repeat. It joins service
shutdown before reopening the binding and every directory audit, and independently checks
native profile and old/fresh ordinary access. This controlled sender does not prove that
KyIdentity's worker produces or acknowledges the corresponding events.

Use `KYQ_TEST_BOOKSTACK_PRODUCER=1` instead of that controlled directory flag, with the
actual identity binary and immutable `KYQ_TEST_BOOKSTACK_SSH_IMAGE`, to exercise the real
worker. The parent provisions an app-scoped SCIM connector through native setup. A pinned
SSH fixture carries raw TLS TCP to the loopback resident service; server certificates, host
keys and separate credentials are verified. Both owned account policies initialize before
registration. Require newer native producer acknowledgments, profile changes, old-session
administrator grant/demotion and assignment-loss denial of sessions/tokens and fresh OIDC.
Retain content, join shutdown and reopen both bindings and all directory audits. This
fixture also globally disables the owner while another identity administrator observes
its newer inactive acknowledgment. Verify old identity/product sessions, token and fresh
OIDC denial, unchanged owner receipts and read-only native retained content, then reopen
both completed revocation/audit chains. This qualifies existing registered accounts in
one BookStack connector; cross-product global lifecycle, new-account/group lifecycle,
audit relay, restore and production deployment remain gates.

Immich currently supports revocation of verified registered accounts, with durable intent,
SCIM authentication, worker retry and audit proofs. A protected `directory` mapping now
also enables profile/administrator updates for its initially owned accounts through their
native revision receipts. Use `administrator_role: "admin"` and `everyday_role: "user"`,
matching the verified KyIdentity app mapping. No native username exists: incoming SCIM
userName is bounded informational metadata; reads expose the immutable subject, and neither
names nor email select or rebind an account. Creation requires the separate protected opt-in
described below; groups remain unavailable.
Controlled native qualification verifies profile/repeat/grant/demotion/readback, restarted
directory audit proofs and refusal after permanent revocation, plus an unavailable
write followed by real worker retry after backoff, retained UUID ownership across restart,
session/key/socket and fresh-login denial, and retained account/photo records and bytes.
The separate `KYQ_TEST_IMMICH_PRODUCER=1` mode uses the actual identity worker for initial
profiles, name/email updates, old-session administrator grant/removal and assignment-loss
revocation. It verifies identity acknowledgment/revision and reopened protected runtime
proofs. A pinned private SSH fixture carries raw TCP to host loopback; TLS remains verified
end to end, and no host firewall changes are needed. Retained authorization codes can be
refused by identity before native claims arrive; new identity authorization is also refused.
Adding `KYQ_TEST_IMMICH_CREATE=1` also enables protected creation: real identity assignment
creates an initially absent owned ordinary UUID account, PKCE sign-in uses that exact
subject/account, and global account disablement removes native sessions/keys while retaining
its profile. The registered-account scenario separately verifies app assignment loss.
Joined shutdown verifies reopened creation, initialization and revocation proofs and native
ownership. An assigned ordinary account may have empty roles; these never grant administration.
The optional `KYQ_TEST_IMMICH_MULTI=1` producer/create fixture additionally verifies one
actual global disablement across native Immich and Forgejo, retained content, independent
app assignment loss and reopened per-product proofs. Full-suite connector coverage, native-writer coordination, administrator purpose separation,
audit/failure-alert deployment, restore and production release remain gates.

To enable registered-account directory updates for Forgejo, also include:

```json
"directory": {
  "administrator_role": "admin",
  "everyday_role": "editor"
}
```

These values must match the verified native source and the KyIdentity app's roles; they are
not inferred from account names. Both are required, distinct lowercase identifiers of at
most 64 bytes. This object is supported only by Forgejo and the community Immich module. Omission retains revocation only.
Role mapping alone does not authorize new-account creation or adoption.

Forgejo, BookStack and the community Immich module may additionally set `"create_accounts": true` inside this protected `directory`
object. Omission keeps account creation disabled. This enables active User POSTs with the same
strict profile/role fields. Native source/subject and username collisions are refused, with
no adoption. A new ordinary-account receipt is verified and retained before its initial
profile/administrator projection. The 201 response follows verified directory completion.
Immich uses installation/provider/subject-scoped absence and immutable native creation
metadata, not a username/email match. Its initial native account always has ordinary
authority; directory projection follows the verified UUID receipt. Stock Immich is refused.
Installer-managed OAuth requires an already provisioned, receipted account;
first-login registration, email adoption and link/unlink changes are refused. Omission of
creation opt-in therefore does not enable account creation through login. Other native
writers and setup-time operations still require coordination.

Each created account retains private `.creation`, immutable `.creation-audit-*` records and a
`creation:true` binding marker. Its completed creation references the initial directory audit
attempt, which survives later updates. Missing proofs fail closed. A lost/unknown native POST
holds all subsequent creation changes across restart; no read automatically clears it.
A known verified receipt can repair interrupted binding publication without another POST.
GET/PUT refuse unfinished initialization. Identical creation repeats do not reset profiles,
roles or passwords, and any revocation intent refuses creation before native I/O. Restore all
these artifacts together; creation holds and audit relay remain deployment admission gates.

An authenticated `PUT /scim/<app>/Users/<registered-id>` replaces display name, primary email
and administrator projection. Send the User schema, exact registered `externalId`, unchanged
native `userName`, `active:true`, one email and an explicit roles array. Only configured roles
are accepted; `roles:[]` removes administrator authority. Role `primary` is accepted as
selection metadata and never determines administrator authority. Forgejo's non-administrator account
state is reported using the configured everyday-role alias. Unknown roles, duplicate keys,
renaming and unsupported name components are refused before intent or native writes. PUT is
bounded to 8 KiB; display names to 100 bytes and email addresses to 254 bytes. GET inspects the
real native profile/role; it does not acknowledge an unresolved write.

Each attempt persists private `.directory` intent and separate immutable directory audit
files before and after native I/O. Completion requires native verification and both proofs.
Known refusals may retry. A lost/interrupted/uncertain native write holds newer directory
updates across restart and returns 503 so KyIdentity retains its mutation fence. The bridge
does not clear this hold from a read-back; native completion/reconciliation support remains
an admission gate. Revocation still works during a directory hold, and pending revocation
intent answers active updates with 429 before native I/O. A PUT after completed revocation is a
reactivation request (see "Reactivation"); no product reactivates natively yet.
Directory audit files are currently local proofs; the kyPulse relay handles revocation audits.

The pinned real-image fixture with `KYQ_TEST_FORGEJO_DIRECTORY=1` and an actual identity
binary verifies protected TLS delivery, profile/role changes, repeated PUT, native reads and
joined shutdown. After native KyIdentity app-role removal, this delivery removes access from
the old Forgejo administrator session before another OIDC login. It does not yet prove the
KyIdentity SCIM worker produced that update, new-account/group/rename delivery, administrator
workflow separation or complete deployment/restore acceptance.
The additional `KYQ_TEST_FORGEJO_PRODUCER=1` fixture uses a fresh identity binary with
`KYIDENTITY_LISTEN_HOST` support. An isolated no-network probe verifies that support before
host networking; both services bind ephemeral loopback endpoints with normal certificate
verification. Native setup creates the assigned-only app and its owned connector. The actual
worker delivers registered-account profiles/roles, native profile edits, assignment-loss
revocation and administrator removal. Producer acknowledgments/revisions, old-token/session
denial, retained repositories and joined durable bridge proofs are checked. Groups,
renaming, uncertain-write reconciliation, deployment and restore still need qualification.

Add `KYQ_TEST_FORGEJO_CREATE=1` to producer mode to prove actual worker account creation for
a newly assigned user. Native SSO, token/session revocation, retained repository and joined
creation/directory/revocation proofs pass. This does not qualify groups, held-write resolution,
creation audit relay, deployment or restore.

Native group ownership uses a protected setup API and an explicitly enabled SCIM Groups route.
`Store.CreateGroup` records `.group-creation` intent and immutable `.group-creation-audit-*`
proofs before publishing `.group-binding` with the verified Forgejo team ID and protected
organization/source/permission scope. Every group binding read requires its native receipt.
Known rejection permits an identical retry; unknown or interrupted creation holds changed
scopes across restart. A retained verified receipt can repair failed publication after native
inspection without another create. Restore these artifacts together. The teams fixture also
proves native ownership/restart/collision/missing-proof refusal through the real bridge store;
production organization setup and central group delivery remain gates.

Protected `Store.ApplyGroupMembership` takes verified bridge account IDs as the owned scope
and desired members. It retains `.group-membership` intent and separate immutable membership
audit proofs, checks native team/member authority, and verifies the final native result before
completion. The group and its supplied owned-account delivery locks (at most 100) serialize
membership with roles, creation and native revocation; other accounts remain independent.
Revocation intent may publish immediately and is checked before and after native work. Desired
revoked, uninitialized, held-directory and administrator accounts are refused. Unknown or
interrupted membership holds changed requests across restart, while account revocation remains
available. Missing proofs fail closed. Restore these artifacts with their creation/account
receipts; membership reconciliation and its audit relay remain gates. The real fixture verifies
scoped native repository access, withdrawal, admin/foreign-member refusal and content retention.
The native teams fixture alone does not qualify actual KyIdentity group production. The
separate groups/producer mode below verifies that path; suite-wide administrator
ordinary-workflow policy remains unfinished.

To enable `/scim/<app>/Groups` for Forgejo, add a protected app policy alongside `directory`:

```json
"groups": {
  "organization_id": 12,
  "organization": "owned-suite-org",
  "permission": "read",
  "permissions": {"repo.code": "read"}
}
```

Use the exact organization receipt from verified native setup, not the example. Runtime check
validates this policy without native requests; production organization provisioning remains a
gate. Omission disables group routes. The app's direct-TLS bearer also authorizes groups.
POST/full PUT requires the Group schema, exact `externalId`, nonempty `displayName` and an
explicit member array of unique bridge User IDs; events cannot set organization IDs or
permissions. Stable native team names derive from group identity; human labels update only
the native description. Withdrawal scope derives from the latest group proof under its lock.
Filtered GET proves native absence or returns the verified owned resource; held outcomes return
503. Logical DELETE verifies empty membership, retains repositories/team/receipts and records
permanent retirement. GET then returns 404 and later PUT/POST refuses reactivation. Repeated
DELETE re-reads native membership without rewriting that proof; any member or failed read
returns 503. Group routes
share the existing four-call/five-second bound. The native fixture proves TLS create, rename,
read, retirement and content retention. Add `KYQ_TEST_FORGEJO_GROUPS=1` with
`KYQ_TEST_FORGEJO_PRODUCER=1`, `KYQ_TEST_FORGEJO_TEAMS=1`, the actual identity binary and
the pinned native image to test group delivery through KyIdentity’s real worker. Owned
app-group setup is checked and resumed before audited native rename, member removal/regrant
and deletion. Ordinary repository access follows those changes and bridge retirement proofs
are checked after shutdown. Audit relay, uncertain-outcome reconciliation, production
organization setup and restore remain gates.

App names and product origins are unique; up to 32 entries are allowed. Config is at most 128 KiB.
The listener uses an explicit local IP and nonzero port. Deployment must restrict ingress to
KyIdentity and outbound access to configured product APIs and kyPulse.

The certificate/key are PEM files, at most 16 KiB each. They must match an unexpired leaf
certificate explicitly usable for server authentication; a CA private key is refused. Native
outbound requests use verified standard TLS trust, no environment proxy and no redirects.
Certificate trust distribution remains part of deployment qualification.

Token files contain raw visible ASCII, with at most one final newline. BookStack's product token
and each SCIM token are 64 lowercase hex characters encoding 32 bytes. Forgejo uses its native
API token. Product token files are at most 16 KiB; SCIM token files at most 128 bytes. Every file
and credential authority is distinct, including across apps. No secret is an inline JSON field,
command-line value, diagnostic or report.

```sh
./kyoffboard --config /var/lib/kyoffboard/runtime/config.json --check
./kyoffboard --config /var/lib/kyoffboard/runtime/config.json
```

`--check` makes no product requests and writes nothing. Its JSON contains schema, `valid` status
and application count; validity proves file/configuration checks, not native readiness. Help exits
0, usage errors 2 and configuration/service failures 1. SIGINT/SIGTERM cancels and joins HTTP
handlers and native worker calls before releasing the state lease, then exits 0.

## Audit destination

The optional `audit` object uses a separate kyPulse paired log-source token, at most 128
visible ASCII bytes plus one optional newline. Its private file and credential must differ
from every product and SCIM credential. Use one dedicated source for this installation's
relay. Loading checks the files only; serving authenticates an OPTIONS capability request
before its first audit POST. An older kyPulse without `ky.log-ingest/1` is refused before
any audit mutation. This native server change is not yet a released dependency.

For operational configuration set `require_alerts:true`. A kyPulse administrator creates
the dedicated source's pairing code with `POST /api/log-sources/pairing` and
`{"target_id":"<owning watched target UUID>","offboarding_alerts":true}`. Claim the code
and save its token in the private target file. The administrator selects the alert target;
the source cannot change it. Serving requires exactly one
`Ky-Log-Alerts: ky.offboarding-alerts/1` capability header before sending. A plain paired
logging source lacks this grant. False/omission remains an ingestion-only development path.

The relay pins kyPulse origin, native source UUID and installation UUID in target state.
It publishes a body/sequence before sending, checks the returned sequence and SHA-256, then
publishes an immutable per-event acknowledgment before advancing. A lost response retries
the same bytes after retained backoff; rate-limit Retry-After is honored. Started events
precede their results. Completed events retain their proof and are suppressed on restart.
Missing historical acknowledgments, a changed destination/source or a conflicting sequence
fail closed and need explicit reconciliation. Keep `pulse-state.json`, all audit files and
their `.sent` receipts together in backup and restore. Payloads contain fixed outcomes and
app/binding digests, never subject values, native errors or credentials. These imported
activity records remain separate from kyPulse's own keyed audit chain.

Transient kyPulse failure retains the outbox while native revocation can progress. Protocol,
authentication or damaged-state failures stop the service. Shutdown joins the relay before
state closes. Receipt acceptance is subject to kyPulse retention; independent audit archival
and restore qualification are still required. Granted sources now create native failure
Alerts atomically with the audit receipt, independent of the watched target's poll state.
The kyPulse sender consumes pending failures across restart and uses its configured webhook.
Paused/silenced targets and absent webhooks retain explicit `paused`, `silenced` or
`unconfigured` notification results. Other send failures retain fixed reason codes; shutdown
interruption keeps delivery pending. Receipt replay creates no duplicate alert or recorded
successful webhook. A crash between external webhook success and saving its outcome can
repeat that webhook. Run one kyPulse monitoring service per database.
Production admission still requires released native capabilities, protected deployment and
actual webhook/connector/restore qualification; omitting `audit` is
supported for isolated development and does not qualify an operational deployment.

## Revocation protocol and retained state

An app's connector URL ends in `/scim/<app>`. Default targets support exact externalId lookup
on verified registered bindings and a single `replace active=false` PATCH. Missing bindings
are conflicts and never prove absence. Explicit Forgejo/Immich directory mapping additionally enables
profile/role PUT; Forgejo's separate creation opt-in enables new User POST after verified native
absence. Retained disabled-account activation and content deletion remain refused. Groups,
native rename and unresolved-write recovery are incomplete, so this protocol does not yet
qualify a complete generic KyIdentity SCIM lifecycle connector.

The bridge accepts direct TLS and the app's bearer credential; cookies, browser origins and
forwarded TLS claims provide no authority. Pending work returns 429 with Retry-After. Only
verified native revocation plus durable audit proof returns 204 and reports inactive. A lost
response retains KyIdentity's normal uncertainty fence; an observation cannot clear it.

The resident worker independently resumes due jobs, with four background calls and streaming
directory scans. State keeps immutable native bindings and attempt audits beside durable jobs.
Bindings are permanent; revocation ends only through reactivation (see "Reactivation"). Back
up and restore the private configuration, server key, product/SCIM credentials and entire state
together, preserving ownership and permissions.
Suite recovery/sealed-backup composition and restore drills remain acceptance gates.

## Reactivation

A KyIdentity re-assignment arrives as a full `PUT` with `active:true` (KyIdentity 0.4.3 never
re-sends a POST). Each account's access history is a sequence of epochs: epoch 1 is the original
revocation job (`<id>.job`, `<id>.audit-*`, read unchanged, so an older store loads as epoch 1),
then even epochs reactivate and odd epochs revoke, each in its own `<id>.epoch-<n>` job with
immutable `started`/`result` audit files. A completed epoch is never rewritten.

- A PUT while revocation is pending or attempting returns 429 and leaves it untouched.
- A PUT after completed revocation starts a reactivation epoch, records its intent and audits,
  then calls the product's native reactivation through the durable job path (the worker resumes
  it). In directory mode the PUT's profile and roles are the projection; native administration
  must be set from them before sign-in is re-enabled. Without a role mapping, a native
  administrator is refused (409, audited `administrator_refused`).
- A product without native reactivation returns 409, audited `reactivation_unsupported`. The
  account stays revoked; another PUT is another audited attempt.
- A definite product refusal returns 409, audited `native_refused`; the worker does not retry
  it. Any other failure, including a lost response, keeps the epoch pending (429) until the
  product's state is verified.
- A later `PATCH active=false` starts the next revocation epoch, also over an unfinished
  reactivation. GET reports `active` from the latest completed epoch.
- Bindings, group retirement by DELETE (still 404) and the creation refusal for a retained
  subject stay permanent. Reactivated accounts stay out of group membership.

### Immich reactivation

Immich reactivates natively when its directory mapping is configured (revocation-only Immich
targets answer `reactivation_unsupported`). The epoch's job calls the community module's
`POST /api/admin/users/<uuid>/kyquickstart/reactivate` with the service administrator's key,
the exact setup/issuer/client/subject, the even epoch and the PUT's profile. In one native
transaction the module sets name, email and `isAdmin` and the directory receipt, then removes
the `ky.immich-access/1` fence and records `ky.immich-reactivation/1`. It restores no session
or API key; those were deleted at revocation, so every pre-revocation credential stays denied.

The bridge reports success only after reading back: no fence, the module's reactivation
receipt at this epoch, no session or key after its own write, and the directory profile equal
to the PUT's. A lost
response or unverified readback stays pending; the retry reads the receipt and verifies instead
of posting again. If a later PUT changed the profile after the epoch committed, the retry
applies it through the directory update. The module's 409 (never revoked, an older epoch, or a
repeated epoch with another profile) is `native_refused`.

If a real-product check shows any old credential working after reactivation, remove the one
`t.reactivate` line in `internal/offboard/target.go`; Immich then answers
`reactivation_unsupported` like the other products.

### Other products

BookStack has no module route yet; its app job adds one. Forgejo 16.0.5 cannot revoke web sessions: its session check
(`services/auth/method/session.go`) only loads the session's user ID, the admin API has no
session revocation, and upstream tracks it as a feature request
([forgejo#796](https://codeberg.org/forgejo/forgejo/issues/796)). Revocation denies old
sessions only through `prohibit_login`, so re-enabling sign-in revived the pre-revocation
session in the real-image fixture. Forgejo therefore refuses with `reactivation_unsupported`
until a Forgejo release with session revocation reopens it. The kyPulse relay transports epoch-1
revocation audits only; later epochs' audits are local proofs.
