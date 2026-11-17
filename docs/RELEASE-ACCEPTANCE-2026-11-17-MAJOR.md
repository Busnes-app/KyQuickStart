# Ky Suite candidate acceptance — Major, 2026-11-17

Status: **IN PROGRESS — not release qualified**.

The [candidate matrix](release-candidate-2026-11-17-major.json) freezes installer runtime
source `48bfb674ab664f91b07b79fda6df221f75a23362`, all 14 catalog entries and their immutable
image references. All ten Ky primary images passed exact-tag/workflow GitHub attestation
verification. The previous catalog is installer `v0.6.0.8`, source
`724fc276c77a56c392ba0e402a2a90ddcd408257`. No runtime source or image pin was changed.

## Current execution and evidence

The seven-app configuration/pairing and Calendar content campaign
`kyq-acceptance-app-content-v3-20261117` **PASSED** at 20:35:59 UTC:
integration 1224.364 s, full controller 1400.410 s. Fresh gate/affected/full CI
passed 8.095 s / 4.491 s / 163.326 s. Tested source was `suite-calendar-etag`,
base `77d9f7aabee3bed6d7ccde361ef11ced4e4bde09`, frozen test patch
`314128ae7c2f3fa570954387110fc1fadc1d6de394f34bd2918faaceb6996510`.

All seven actual previous-image upgrade commands, deployment/health and required verified
backups passed. Selected Docker credentials, Identity JWKS and Recovery pairing/key binding
were retained; candidate Pulse deposited using its prior pairing. The available Kubernetes
Secret payload count was **zero**, so this is not evidence that volume-held keys of every
product were compared. Candidate reciprocal native API sign-ins passed. The ordinary
Calendar event retained exact text/times/IDs/ETag/access and accepted a valid candidate
If-Match edit. The run also retained KyDNS's expected fail-closed/unsafe-gateway refusals;
successful edge sign-in remains unproven on this Docker topology.

The consolidated review tree `suite-lifecycle-acceptance`, building on the tested Drive
extension, passed fresh affected compilation/relay6.689 s and full CI163.476 s (controller
170.240 s). Its341 non-documentation files, except the inherited exact Drive test, match
the successful app integration tree byte for byte; runtime, pins and dependencies match.
This reuses the named integration evidence and is not a fresh combined integration run.
No wider product-data/offboarding/restore qualification is inferred.

The Messages previous-primary campaign `20261117-major-message-upgrade-v2` **PASSED**
at21:03:48 UTC: fresh affected7.045 s/full CI162.118 s, real integration511.402 s,
full controller680.688 s. It used the complete historical manifest/Compose entry,
upgraded0.6.0.1→0.6.1 with its own verified pre-upgrade snapshot, retained selected
keys and original sessions, and decrypted both old server-fetched history and a new
encrypted event. Candidate offboarding and the existing candidate-backup/history-restore
drill passed afterward. This is not an upstream Matrix-version migration or a restore
of the old pre-upgrade snapshot itself. The failed V1/source remain intact.

Content-v2 remains exit1 / 1082.514 s (controller1256.364 s): exact Calendar readback
passed but the fixture's bare HTTP If-Match was rejected400. V3 only quoted that header
relative to V2. Every failed run/source remains preserved.

The original content-v1 run failed 801.169 s before any upgrade: later apply replaced
Calendar's test-only CA mount/environment, so the new old-image seed sign-in returned
502 and its native log reported x509 unknown authority. It had already passed all seven
monitoring targets and old Recovery/Pulse pairing/deposit. No Calendar content or final
upgrade-retention outcome was obtained.

The isolated correction makes the existing CA helper repeatable through keyed strategic
merge, skips a restart when the pod template is unchanged, and restores Calendar trust
before seeding. Calling it twice against the actual API server exercises duplicate-write
refusal. Upgrade deployment reuses the same fixture patch. Hostname/certificate validation
remains enabled; product TLS behavior and pins are unchanged.

V1 failed before cluster creation on DNS. V2 failed on fixture target count/Post DNS.
V3 passed all seven monitoring targets and old-image pairing/deposit, then the real
Identity 0.6.0.1 → 0.6.1 deployment/health and verified pre-upgrade claim copy. It stopped
at Recovery's correct `ack_required` gate (integration exit 1 / 851.258 s; controller
1020.521 s). The test omitted the acknowledgement for a non-migrating upgrade. Final
all-app account/key/content retention assertions were not reached, so no completed
seven-app qualification is claimed.

Private evidence root on Major: `/home/yoshi/.local/state/kyquickstart-acceptance/`.
Logs remain private; per-stage result files retain commands, timestamps, exit codes and
source hashes. Reruns have separate names and preserve every failure:

| Directory | Role and final/current state |
| --- | --- |
| `20261117-major` | Original seven standard shards and docs passed; original optional HA/controller failure retained |
| `20261117-major-ha-rerun-1` | Corrected HA, Euro-Office and remaining controller passed; references original seven passes |
| `20261117-major-drive-upgrade` | Original queue superseded before execution |
| `20261117-major-drive-upgrade-v2` | Prerequisite refused the failed original baseline (exit 75); no Go checks ran |
| `20261117-major-drive-upgrade-v3` | Affected checks, full CI and real previous-Drive recovery passed |
| `20261117-major-app-upgrades-v1` | Affected/full CI passed; integration failed before cluster creation on DNS |
| `20261117-major-app-upgrades-v2` | Integration failed on fixture target count/Post DNS before upgrades; original results retained |
| `20261117-major-app-upgrades-v3` | Fresh full CI passed; integration stopped at the correct Recovery no-backup acknowledgement gate after Identity upgrade |
| `20261117-major-app-content-v1` | Full CI passed162.266 s; integration failed801.169 s at Calendar seed sign-in after fixture CA was replaced |
| `20261117-major-app-content-v2` | Gate/affected7.092 s /4.472 s and fresh CI162.151 s passed; integration1082.514 s failed at malformed fixture If-Match after seven upgrades/retention and exact Calendar readback |
| `20261117-major-app-content-v3` | PASS: gate8.095 s /affected4.491 s /full CI163.326 s; real integration1224.364 s /controller1400.410 s |

All heavy work is serial under `/tmp/ky-heavy-test.lock`: Go cgroups have 2 GiB memory,
no swap, two CPUs and persistent caches. Docker/kind fixtures have their own bounds.
Disposable apps explicitly blank both production relay URLs. Existing services,
credentials, containers and the separate mail-capacity workstream stay preserved.

## Executed checks

| Check | Result | Fresh execution |
| --- | --- | --- |
| Ten Ky image release/source attestations | PASS | Major, selected immutable image digests |
| Baseline full `make ci` | PASS | 487.725 s, unmodified candidate source |
| Full `make ci` with safe HA fixtures | PASS | 148.221 s |
| `TestRelaysOffCatalog` | PASS | 53.519 s including compile |
| NPM edge integration shard | PASS | 480.770 s |
| NPM joins integration shard | PASS | 963.021 s |
| Messages / Kubernetes apply shard | PASS | 620.712 s; Messages 442.98 s, Kubernetes apply 176.25 s |
| Drive restore / KyYard shard | PASS | 550.107 s; Drive restore 297.30 s |
| nginx edge / node and pod failover shard | PASS | 909.374 s; core edge test 632.05 s |
| nginx joins shard | PASS | 954.632 s |
| Docker catalog startup/restart (within final shard) | PASS | Six products, 82.02 s |
| Docker KyYard runner (within final shard) | PASS | 6.01 s; test-built runner |
| Final standard e2e shard | PASS | 318.592 s; all seven standard shards passed |
| mdBook build / generated local links | PASS | Pinned 0.5.4; 0.026 s / 0.053 s |
| Pinned KyIdentity database-cluster HA | PASS (corrected fixture) | 526.26 s; original fixture failure retained |
| Pinned KyIdentity replicas | PASS (corrected fixture) | 272.23 s; operator-provided and installer-managed database cases |
| Combined corrected pinned-image HA stage | PASS | 804.611 s including compilation |
| Euro-Office integration | PASS | 385.807 s; existing health/callback/egress/lifecycle assertions |
| Corrected acceptance controller | PASS | 1368.495 s; references seven original standard passes |
| Previous-image Drive upgrade/recovery | PASS | 384.324 s; fresh full CI 163.607 s; combined controller 554.558 s |
| Initial seven-app extension (app-upgrades-v3) | FAILED; superseded by passing content-v3 | V3 full CI 162.783 s passed; integration 851.258 s stopped at Recovery acknowledgement; final retention checks not reached |
| Seven-app configuration + Calendar content | PASS (content-v3) | Fresh gate8.095 s /affected4.491 s /full CI163.326 s; integration1224.364 s/controller1400.410 s; prior failed runs retained |
| Previous-image Messages encrypted-history upgrade | PASS (message-v2) | Fresh affected7.045 s/full CI162.118 s; real integration511.402 s/controller680.688 s; old/new encrypted history, selected keys/sessions and subsequent candidate offboarding/restore |


The corrected controller completes the named fixture campaign, not the whole-suite
qualification. A generic fixture pass or a container restart cannot close a product's
content migration, offboarding or restored-service gate.

## Per-product qualification ledger

PASS applies only to the named assertions. Corrected content-v3 completed all seven native
configuration upgrades, specified retention and Calendar content checks. This is not
whole-suite release qualification or every product's ordinary content/offboarding/restore.

| Product | Executed real-image evidence | Direct evidence still missing |
| --- | --- | --- |
| KyIdentity | Native owner creation, OIDC/setup amendments; historical 0.4.3 to candidate upgrade with verified pre-upgrade claim copy; both pinned replica/database HA cases including native sessions, database TLS, primary/node loss and stop/start/reinstall | Published previous-catalog upgrade passed with owner access and unchanged JWKS. HA does not prove every recovery topology; powered-off kind node return was not tested. |
| KyDrive | Native setup and SCIM; old 0.6.0.1 to candidate 0.6.1 failed-upgrade recovery from the upgrade's own verified snapshot; exact 300 KiB file, owner access, credential hashes/modes and removal/deletion replay; admin API 200, admin file 403 with separation refusal, everyday admin API 403 and everyday file download | Other workspace/content/migration/failure models; actual browser editing/save and synchronized or mounted desktop saves. |
| KyRecovery | Native administrative sign-in, everyday identity gate, throwaway public-key ceremony and capsule listing/deposit | Previous-catalog key/pairing retention passed. Restored service with existing capsules/ceremony, administrative revocation and session invalidation remain untested here. Deposit/listing is not product restore. |
| kyPulse | Native administrative sign-in, everyday identity gate, monitoring targets healthy and real paired deposit | Previous-catalog config/pairing binding and candidate deposit with prior credentials passed. Restored monitoring state and offboarding/session revocation remain untested. |
| KyNotes | Admin backup-status API 200 / ordinary containers 403 with admin-account refusal; everyday backup-status 403 / containers 200 | Native config/account upgrade passed. Real notes migration, offboarding/session revocation and restored notes remain untested. |
| KyCalendar | Admin users API 200 / calendars 403; everyday users API 403 / calendars 200 | Old-image event content/times/IDs/ETag/access and candidate edit passed. Offboarding/restored meetings and recurrence/meeting delivery untested. |
| KyPost | Native Docker setup, pre-bind-volume layout copy, admin users API 200 / everyday 403, session-ending restart and cold backup | Native config/account upgrade passed in IMAP-proxy mode. Real mail migration/restored mail, offboarding and dedicated-admin ordinary-mail denial remain untested. This fixture disables native mail listeners; it does not qualify the workforce mail stack. |
| KyDNS | Native setup, authoritative UDP/TCP DNS, forged-header rejection, unsafe-gateway trust refusal and fail-closed edge behavior | Successful edge/admin sign-in is UNPROVEN on Major because Docker rewrites source addresses. Previous zones/config upgrade, offboarding and restore remain untested. Never broaden trust to force a pass. |
| KyMessages | Real encrypted chat, offboarding and history restore; original device decrypts history and removals replay before Synapse starts. Dedicated admin chat sign-in receives KyIdentity access_denied and MAS has no admin account. | Previous0.6.0.1→0.6.1 upgrade with own verified snapshot, original keys/sessions and old/new encrypted events passed; later candidate offboarding/history restore passed. Upstream Matrix-version migration and direct authorization with an otherwise-valid admin token remain unproven. |
| KyYard | Native admin sign-in/everyday identity gate, runner ownership and session/credential persistence; Docker startup/runner checks | Previous-catalog upgrade, offboarding and restored management service remain untested. Source-built runner fixtures do not qualify a published runner artifact. |
| Nginx Proxy Manager | Both NPM shards: routing, ownership, administrative bootstrap, token refresh and reruns | No general upstream data migration/recovery claim. |
| nginx edge | Both nginx shards plus node/pod failover, route reload/ownership and certificate assertions | Cluster topology/storage/network conditions beyond these fixtures. |
| CloudNativePG | Pinned operator/database HA fixture: synchronous replication, TLS, primary/node loss, hibernation/reinstall and object ownership | Node return and production storage/topology behavior beyond kind. |
| Euro-Office | Health, signed callback 200/401, restricted egress and lifecycle integration passed | Production source/build provenance, real browser editing/save and desktop-save qualification remain open. |

Recovery/Pulse everyday denial in the shared sign-on helper occurs at KyIdentity's assigned
user gate. Role reporting after sign-in alone is not an independent management/content
API isolation check. Notes and Calendar do perform reciprocal API checks. The ordinary
Drive sign-on helper reports roles; the separate passed Drive recovery extension supplies
its real reciprocal admin/file API assertions. Post's sign-on helper checks `/api/users`,
so no ordinary-mail denial is inferred.

Source anchors:

- [NPM installation assertions](../test/e2e/npm_kube_test.go) and
  [join orchestration and KyDNS fail-closed branch](../test/e2e/npm_joins_kube_test.go).
- [Drive role-reporting sign-in](../test/e2e/drive_kube_test.go),
  [Calendar reciprocal authorization](../test/e2e/calendar_catalog_kube_test.go),
  [Notes reciprocal authorization](../test/e2e/notes_catalog_kube_test.go) and
  [Post admin endpoint authorization](../test/e2e/post_catalog_kube_test.go).
- [Historical Identity upgrade and Drive restore/removal replay](../test/e2e/restore_kube_test.go).
- [Docker startup/restart fixtures](../test/e2e/docker_catalog_test.go).
- [Messages encryption, admin admission and history-restore assertions](../test/e2e/message_chat_test.go).

These relative links refer to the frozen baseline checkout. New opt-in upgrade assertions
remain in their named isolated worktrees pending consolidation into a mergeable PR.

## Real previous-Drive recovery

`KYQ_TEST_DRIVE_UPGRADE_MANIFEST` selects the exact attested old Drive manifest. The drill
seeds real data/accounts, invokes candidate upgrade, requires the native candidate health
endpoint to answer while an injected Docker health probe makes deployment fail, and then
restores the upgrade's own verified restic snapshot. Failed data moves aside. Exact file
bytes, owner access, secrets and subsequent SCIM removal/deletion replay must pass.
This qualifies the specific induced health failure; it is not every failed schema migration.

The all-candidate baseline restore remains separate: historical Identity 0.4.3 upgrade,
Drive disaster/replacement restores, append-only refusal and removed/deleted-user replay.
No restored-leaver session was observed in that sample; this does not prove a zero-duration
access window under all conditions.

The exact tested Drive tree is now [PR #206](https://github.com/Busnes-app/KyQuickStart/pull/206),
head `704cf0ab33cbb38cd5b1fbdcd745050d92172482`, built on #205 and now human-merged as `597ec94`. A separate committed/pushed
`drive-upgrade-acceptance` worktree preserves the completed execution checkout. All tracked
file bytes match the passed Drive worktree, so its local full-CI/real-image evidence applies;
GitHub current-head checks and authenticated security are pending. These gates use their
own event-driven watcher. The PR does not claim whole-suite qualification.

## Seven-app native configuration upgrade extension

`KYQ_TEST_RELEASE_UPGRADE_MANIFESTS` selects independently attested historical manifests
for Identity, Recovery, Pulse, Drive, Notes, Calendar and Post. The seven old primary-image
attestations bind exact repository/workflow/tag identities. Current transport templates,
fixture CA trust and blank relay settings remain explicit; this is not an old-installer
binary upgrade or a claim about every historical transport layout.

The case seeds owner/OIDC/SCIM configuration, Recovery ceremony and Pulse pairing/deposit
on old images, then CLI-upgrades all seven to candidates. Passed assertions: native
owner/everyday access again, Identity JWKS unchanged, Kubernetes Secret payload hashes
that exist (the count can be zero), selected Docker key hashes, retained Recovery
pairing/service/key ID, and candidate Pulse deposit through the old pairing/token.
The separately described Calendar case seeds one real ordinary event. Notes/mail content and DNS/Messages/Yard upgrades are outside this case.
Full CI passed 169.321 s on V1/V2 source. V3 changed fixture monitoring, DNS and CA reuse,
so it ran fresh affected/full checks before integration (6.369 s / 162.783 s, both passed). The helper retains the original
four-target expectation for the ordinary edge suite and receives all seven explicitly for
the upgrade case. It still requires every target healthy; no health assertion is relaxed.
Content-v3 later completed with fresh full CI163.326 s and integration1224.364 s; retain its frozen source/hash as evidence.

## Messages coverage audit and next upgrade case

The passed candidate Messages baseline explicitly refused the dedicated admin at the
KyIdentity → MAS chat gate and checked that MAS had no such account. This evidence was
present in the executed test and is now recorded above; it does not create a direct
Matrix authorization result for an admin token the fixture never received.

The previous-primary-image history case passed after correcting the V1 historical-template
mismatch. It crosses console migrations24–27 at `data_since: 0.6.0.2` and takes its
own verified pre-upgrade backup; no acknowledgement substitutes for it. Details below.

## Calendar content case in corrected acknowledgement rerun

The corrected optional extension runs in `suite-calendar-etag`, branch
`test/suite-calendar-etag`; earlier failed worktrees remain unchanged.
It creates a real everyday-owned calendar and event through the previous image's native
API. The passed candidate checks compare text (including Unicode/newlines), location,
times, calendar/event IDs, ETag and edit access through a newly signed-in session, then
use the old ETag for an optimistic-concurrency edit and verify unrelated fields remain.
No attendees or outgoing mail are involved.

The original prepared source and patch remain in `20261117-major-calendar-content-prepared`.
After diagnosing V3's gate refusal, the isolated source now supplies the normal CLI
acknowledgement only where `Upgrade.Migrates(previousVersion)` is false and asserts that
it is recorded. Where migration is required, no acknowledgement flag is supplied and
`<app>.pre-upgrade-backup` must be `ok`. The existing `TestUpgradeGate` regression runs
fresh before affected compilation, full CI and integration. This uses the intended
operator path in disposable fixtures; runtime gates remain unchanged.

The new frozen run is `20261117-major-app-content-v3` in `suite-calendar-etag`;
earlier failed evidence/source remain intact. V2 did retain the seeded event exactly, then
failed because its edit sent an unquoted HTTP If-Match. V3 quotes the original ETag, as the
product API tests require, and the complete integration **passed1224.364 s**.
Calendar fixture trust is restored after later apply calls with the repeatable CA helper.
It qualifies this seeded event, not Calendar restore/offboarding/recurrence/meeting
transport or other products' content. Notes content needs its actual encryption/key flow;
arbitrary opaque bytes are not substituted as a user-note workflow.
The product already has native encryption-client and browser-format migration tests, but
these do not close the installed-image gate: the MCP test seeds users directly in its
router/database fixture, and the browser test injects an older ciphertext format rather
than upgrading the previous published server. The meaningful next Notes case needs actual
ordinary-user key enrollment and encrypted notebook/page creation on the old pinned image,
then candidate decryption/edit with retained client keys. Source review alone is not a pass.

Source-only feasibility audit `20261117-major-notes-content-audit-2029Z` checked the
actual old `v0.6.0.2` source `2fb29f3871a243728d0794c3b93688fe2de45797` against
the frozen candidate `v0.6.1` attested source `ba93e8281b3f64a9b0c5a4b786971da56180dc65`.
Their identity, reauthentication, recovery, notebook UI, crypto and migration-fixture
files match byte for byte. A real SSO case must create its device-only key through the
old bundled client, complete action-bound KyIdentity confirmation and save a recovery
copy: notebook creation refuses an unrecoverable key. The local-password browser helper
cannot replace this installer-created SSO path. Reuse actual UI content creation and
retain the isolated browser key store across the upgrade; do not inject ciphertext or
skip reauthentication/recovery. No Notes browser/runtime case was implemented or run.


## Passed previous-image Messages history upgrade

The tested `suite-message-catalog-upgrade` worktree (branch
`test/suite-message-catalog-upgrade`, base `78b993ea8628bae6d5ab80c45a62e3ef0bdfb0f0`)
implements the opt-in `KYQ_TEST_MESSAGE_UPGRADE_MANIFEST` case with its sibling historical
Compose template. V1 passed affected6.406 s/full CI162.365 s, then failed integration157.190 s
(controller326.060 s) before old chat/upgrade: its old initializer could not create the
candidate guest-page bind. Corrected V2 passed affected7.045 s/full CI162.118 s and the
complete real-image integration511.402 s/controller680.688 s. The failed source is unchanged.
The old primary `0.6.0.1` image passed exact repository/workflow/tag attestation at
20:16:26 UTC. Its manifest is byte-identical to installer `v0.6.0.8`; all auxiliary
Matrix image pins match the candidate.

The corrected case installs the complete historical manifest/Compose entry with explicit
fixture host/CA plumbing, uses the existing real Megolm room/device flow, then
requires the CLI upgrade's own verified pre-upgrade snapshot across `data_since: 0.6.0.2`.
It compared selected keys, renewed both original device sessions, fetched and decrypted
the old raw encrypted event with original client keys, exchanged a new encrypted event,
then completed existing candidate offboarding and history restore. The pre-upgrade and
later restore destinations have separate server-work and credential directories; no live
server's certificate/password files are overwritten. This does not test an upstream
Matrix-version migration or restore of the old-image pre-upgrade snapshot itself.

Prepared evidence: `20261117-major-message-upgrade-prepared`. Failed `20261117-major-message-upgrade-v1` and its source remain intact. New execution
evidence `20261117-major-message-upgrade-v2` records fresh stages and patch/manifest/Compose
hash guards. The corrected content-v3 prerequisite and all V2 stages passed. A separate
publication tree `message-history-acceptance` matches344 non-documentation files of this
passed tree byte for byte, including runtime/pins/dependencies; its later report edits do
not constitute a fresh test execution.

## HA correction and release gates

The original HA fixture used `kind load docker-image` directly on a digest reference.
Read-only diagnostics captured a broken containerd import record and key-generator
`CreateContainerError`. Both optional fixtures now use existing `kindLoad`, which loads
a local tag while workloads retain immutable digests, plus the relay-disabled catalog.
The exact four-line test correction is [PR #205](https://github.com/Busnes-app/KyQuickStart/pull/205),
head `77d9f7aabee3bed6d7ccde361ef11ced4e4bde09`. Local full CI, relay and pinned HA passed.
An event-driven Tick verified all 18 reported GitHub checks successful and authenticated
security clearance on that exact head. The three required checks passed. Its watcher was
stopped after both gates cleared; the PR was later human-merged as `57b2676`. This does not
complete the wider acceptance campaign or authorize publication.

Euro-Office's frozen digest is
`beca380debb9b4eadb7e4c662d112a325c5df0f2179453a4affd8e6ba5d00562`.
Exact GitHub attestation verification returned HTTP 404 on November 17. Upstream successful
run 33166218952, tag `v9.3.4-hotfix.1`, `.github/workflows/build.yml`, source
`4ba0c0f202ae484eb202f24c59ddf5f4a7004161` matches observed build correspondence.
Unsigned BuildKit metadata is neither a signed attestation nor an independent rebuild.
This is a separate open production gate, not a failed baseline CI assertion.

Remaining delivery work: add feasible missing real content/
lifecycle cases; consolidate tests/report/matrix into mergeable PRs with required full and
current-head security checks; verify owned-resource cleanup. Never remove dirty/unpushed
worktrees or alter pre-existing services. Native-client distribution is separate: the Mac
development archive/signature passed, but Developer ID export remains blocked on a valid
direct-distribution profile. This server report certifies no Mac or Android artifact.

DOX pass: root `AGENTS.md` was updated to document the opt-in seven-app/Calendar upgrade-test
contract (`KYQ_TEST_RELEASE_UPGRADE_MANIFESTS`).
`docs/AGENTS.md` and the child indexes remain unchanged because documentation ownership
and workflow did not change. This report and the candidate matrix record observed evidence
and qualification limits; prior versions are preserved in the timestamped private
report-audit directories.
