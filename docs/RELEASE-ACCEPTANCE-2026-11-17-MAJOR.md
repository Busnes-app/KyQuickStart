# Ky suite acceptance — 2026-11-17/18

Status: **whole suite incomplete; no release qualification claimed**.

The [candidate matrix](release-candidate-2026-11-17-major.json) records all 14 frozen
catalog entries and immutable image references, the exact old installer `v0.6.0.8`
(source `724fc276c77a56c392ba0e402a2a90ddcd408257`), and per-product coverage.
All ten Ky primary images passed exact-tag/workflow attestation verification. No runtime
source, dependency or image pin is changed by this acceptance extension.

## Coverage

All 14 baseline catalog components have named executed checks. Nine Ky products passed
controlled previous-version primary-product upgrade cases, including the Yard case below.
The owner separately confirms that an existing KyDNS in-place upgrade to v0.6.1 is working;
its prior version and detailed retention checks were not supplied. Thus all ten Ky products
have successful upgrade evidence: nine controlled cases plus owner-confirmed DNS.
This is not a count of fully qualified products or published runner artifacts.
Four named content/recovery campaigns passed: Drive, Messages, Calendar and Notes, each
within its own scope. Baseline coverage does not prove content migration or full recovery.

| Product | Executed real-image evidence | Direct evidence still missing |
| --- | --- | --- |
| KyIdentity | Native owner creation, OIDC/setup amendments; historical 0.4.3 to candidate upgrade with verified pre-upgrade claim copy; both pinned replica/database HA cases including native sessions, database TLS, primary/node loss and stop/start/reinstall | Published previous-catalog upgrade passed with owner access and unchanged JWKS. HA does not prove every recovery topology; powered-off kind node return was not tested. |
| KyDrive | Native setup and SCIM; old 0.6.0.1 to candidate 0.6.1 failed-upgrade recovery from the upgrade's own verified snapshot; exact 300 KiB file, owner access, credential hashes/modes and removal/deletion replay; admin API 200, admin file 403 with separation refusal, everyday admin API 403 and everyday file download | Other workspace/content/migration/failure models; actual browser editing/save and synchronized or mounted desktop saves. |
| KyRecovery | Native administrative sign-in, everyday identity gate, throwaway public-key ceremony and capsule listing/deposit | Previous-catalog key/pairing retention passed. Restored service with existing capsules/ceremony, administrative revocation and session invalidation remain untested here. Deposit/listing is not product restore. |
| kyPulse | Native administrative sign-in, everyday identity gate, monitoring targets healthy and real paired deposit | Previous-catalog config/pairing binding and candidate deposit with prior credentials passed. Restored monitoring state and offboarding/session revocation remain untested. |
| KyNotes | Reciprocal admin/content API and native config/account upgrade; V12 published old-client encrypted notebook/page Unicode seed, retained-key candidate decryption, exact Unicode edit/native save and cache-evicted exact edited-body reload passed | Offboarding/session revocation, restored notes/client-key-loss recovery and other content/page types remain unqualified. |
| KyCalendar | Admin users API 200 / calendars 403; everyday users API 403 / calendars 200 | Old-image event preservation/candidate edit and native candidate capsule recovery with restored ordinary event/edit passed. Offboarding, installer resume, recurrence and meeting delivery remain untested. |
| KyPost | Native Docker setup, pre-bind-volume layout copy, admin users API 200 / everyday 403, session-ending restart and cold backup | Native config/account upgrade passed in IMAP-proxy mode. Real mail migration/restored mail, offboarding and dedicated-admin ordinary-mail denial remain untested. This fixture disables native mail listeners; it does not qualify the workforce mail stack. |
| KyDNS | Native setup, authoritative UDP/TCP DNS, forged-header rejection, unsafe-gateway trust refusal and fail-closed edge behavior; owner confirms an existing in-place upgrade to v0.6.1 is working | Owner report is successful operational upgrade evidence; its prior version and detailed checks were not supplied. Controlled previous-catalog zone/config retention, successful edge/admin sign-in on Major, offboarding and restore remain unqualified. Never broaden trust to force a pass. |
| KyMessages | Real encrypted chat, offboarding and history restore; original device decrypts history and removals replay before Synapse starts. Dedicated admin chat sign-in receives KyIdentity access_denied and MAS has no admin account. | Previous 0.6.0.1→0.6.1 upgrade with own verified snapshot, original keys/sessions and old/new encrypted events passed; later candidate offboarding/history restore passed. Upstream Matrix-version migration and direct authorization with an otherwise-valid admin token remain unproven. |
| KyYard | Relay-corrected published primary 0.6.0.3-to-0.6.1.1 Kubernetes CLI upgrade; observed exact image digests, selected key/setup hashes and original admin session retained; fresh native admin SSO/everyday denial plus existing runner ownership/recreation/revoke checks passed | Published runner artifact/source provenance, Docker upgrade, offboarding and restored management service remain unqualified. The resident runner was an explicit local fixture with a preflight-verified relay-isolated embedded catalog. |
| Nginx Proxy Manager | Both NPM shards: routing, ownership, administrative bootstrap, token refresh and reruns | No general upstream data migration/recovery claim. |
| nginx edge | Both nginx shards plus node/pod failover, route reload/ownership and certificate assertions | Cluster topology/storage/network conditions beyond these fixtures. |
| CloudNativePG | Pinned operator/database HA fixture: synchronous replication, TLS, primary/node loss, hibernation/reinstall and object ownership | Node return and production storage/topology behavior beyond kind. |
| Euro-Office | Health, signed callback 200/401, restricted egress and lifecycle integration passed | Production source/build provenance, real browser editing/save and desktop-save qualification remain open. |


## Notes published-client encrypted-content upgrade

`TestKubernetesNPMEdgeJoins` with `KYQ_TEST_NOTES_BROWSER=1` and independently attested
historical manifests passed at 2026-11-18T02:17:26Z:

| Check | Duration | Result |
| --- | ---: | --- |
| Browser paragraph/React input regression | 3.553 s | PASS |
| Affected compilation, relay isolation and issuing-CA/hostname controls | 6.567 s | PASS |
| Fresh full `make ci` | 162.588 s | PASS |
| Real integration command | 1376.794 s | PASS |
| Full campaign controller | 1549.637 s | PASS |

The tested base was `8f36d2085c2c4110373733b01b6df62aee508ad9`, with frozen patch
`9a2d0439e5acdce70e21cd1869ad748d2427ff125bfd5bd3f6a42f539509be31`.
The Notes publication in PR #211 preserved all five test/helper files byte for byte.
Its newer base added documentation only, including the current-head CodeQL merge gate.
The publication CI was a separate execution; no second Notes integration run was claimed.

The actual old bundled Notes client completed everyday native OIDC, action-bound Identity
confirmations, device-key enrollment, recovery-copy download/type-back/save and encrypted
notebook/page creation with an exact Unicode title/body. The CLI then upgraded all seven
old images with deployment, health, required backup and selected credential/JWKS/pairing
retention checks. A candidate Pulse deposit used its prior pairing. Fresh candidate Notes
SSO decrypted the exact old server-backed content using retained browser keys.

The candidate append targets the original paragraph using a normal click and End key;
a read-only selection proof requires a collapsed caret inside that paragraph at its end.
Both before and after native Save, the edited rendered text matched the exact expected
78 characters in one paragraph. After another owned note-cache eviction, the exact edited
Unicode content reloaded and decrypted. Later candidate Calendar/Post checks also passed.
No whitespace normalization satisfies the assertions. Earlier V10/V11 failures diagnosed
a fixture paragraph-boundary mismatch; all failed sources and private evidence remain
preserved rather than relabelled as passes.

Nine seed and two edit request bodies were inspected, with no uninspectable bodies or
plaintext markers. Each last object PUT digest matched server-fetched raw ciphertext.
Because the installed Playwright BiDi implementation omits protocol request bodies, a
read-only observer copies native fetch arguments for inspection and passes original
arguments, headers and responses unchanged. Unknown/oversized inspection bodies fail
closed. The observer supplies no login, key, cookie or confirmation substitute.

Native Save and empty pending/upload queues precede eviction of only the owned browser's
`notes` IndexedDB store. Client `keys` records remain byte-identical, excluding cached
plaintext as the source of the later read. ASCII credentials use keyboard events; Unicode
uses the browser editing engine. Product crypto, MFA/CSRF/roles, TLS hostname verification
and published images remain intact. Only the private test browser profile trusts its
named issuing CA. No global trust or host DNS changes are required.

## Yard published-primary Kubernetes upgrade

The relay-corrected `TestKubernetesKyYard` case **PASSED** on 2026-11-18. It used the
exact published 0.6.0.3 primary from installer 0.6.0.8 and frozen candidate 0.6.1.1.
Both deployed repository/digest identities matched; the old exact-tag release-workflow
attestation remains independently verified. Product runtime, dependencies and all 14 pins
are unchanged.

| Check | Duration | Result |
| --- | ---: | --- |
| Actual unsafe cached-runner preflight | 6.788 s | Expected refusal, exit 1; no container started |
| Affected/source/relay/issuing-CA controls | 1.571 s | PASS |
| Bounded static fixture build | 5.112 s | PASS |
| Actual corrected cached-runner preflight | 1.641 s | PASS |
| Fresh full `make ci` | 162.286 s | PASS |
| Real primary-upgrade integration command | 255.414 s | PASS |
| Full campaign controller | 433.124 s | PASS |

Tested base `e38f3c7612eea0ee24071f721ec26affe909ddfd`, frozen source patch
`9f5b223cf11e2e5bccd7320a56c8223a24b6b119a8e155ab40791a231fa44b15`.
Only this report and its matrix changed after execution; test/runtime inputs stayed
byte-identical. The final report has a separate fresh mdBook/link check.

The CLI upgrades Yard before handoff. Selected session/encryption/instance/client/
directory/setup hashes and the original administrator session remain unchanged. Fresh
candidate native administrator SSO and everyday Identity denial pass, followed by runner
socket, ownership, self-upgrade refusal, pod recreation and revoke checks. This specific
transition requires the explicit no-backup acknowledgement, which was observed; no
required migration backup or disaster restore is claimed. Both image references are
canonicalized using `catalog.PinnedImage`, retaining exact repository/digest equality.

Earlier V4/publication commands exited 0 (282.398 s/287.535 s), but later inspection found
their local runner embedded an unpatched Identity catalog and redeployed Identity. Those
primary assertions remain historical evidence; they did not prove relay isolation through
the runner phase. No production registration has been observed; no production service
query was performed to infer otherwise. Original sources and results remain preserved.

The corrected runner builds from copied source with existing Identity/Post relay patches
applied before embedding. Both default builds and cached overrides must pass a binary
inspection of the exact patched templates before kind imports or starts the image.
Identity's explicit empty relay values were observed in its Deployment and pods at all
five stages: initial install, before handoff, after runner upgrade, after pod recreation,
and after revoke/CLI apply. The actual old image failed the guard without being started;
the corrected image passed it. The constrained-host fixture was compiled under the same
Go cgroup bounds, then inserted into a never-started networkless container based on the
preserved local fixture. It required no Docker build. The original image was preserved.

`KYQ_TEST_YARD_FIXTURE_RUNNER_ID` accepts an immutable local image ID and uses an owned
per-PID import tag, removed after the test. The ordinary CI fixture builds from the patched
source copy when the override is absent. This remains local fixture plumbing: published
runner provenance, Docker upgrade, offboarding and restored management service are open.

## Reproduction and boundaries

Read the optional-case contract in [AGENTS.md](../AGENTS.md). Supply explicit existing
`KYQ_TEST_BROWSER_MODULE` (Playwright module) and `KYQ_TEST_BROWSER_EXECUTABLE` (Firefox)
paths, plus `certutil`. `KYQ_TEST_RELEASE_UPGRADE_MANIFESTS` names the independently
attested historical manifests. An optional `KYQ_TEST_BROWSER_WRAPPER` may enforce the
approved browser-only process limit. Run the named integration command from the repository root; Go runs the browser helper
from the package working directory `test/e2e`. The historical manifests and local browser
prerequisites are explicit operator inputs; no dependency download or trust bypass is
performed by this helper. The matrix records the exact integration command.

One campaign holds `/tmp/ky-heavy-test.lock`. Go is bounded to 2 GiB/no swap/2 CPUs/128
tasks, `GOMAXPROCS=2`, `GOMEMLIMIT=1GiB`, `GOFLAGS=-p=1`, with persistent disk-backed
scratch/caches. The synchronous owned browser-only scope has the separately approved
256-task cap and the same memory/CPU limits, while Go waits; it is not another heavy
campaign. Both `PUSH_RELAY_URL` and `APNS_RELAY_URL` are explicitly empty. Existing
services, credentials, unrelated containers and all frozen pins remain preserved.

## Remaining qualification

The Notes case does not qualify offboarding, loss of client keys, restored Notes or all
page/content types. Further suite gates include native Post stored/incoming mail, direct
MX receipt and workforce capacity; Euro-Office production provenance, actual editing and
synchronized/mounted desktop saves; DNS positive edge and controlled zone/config
retention; published suite-runner artifact and Yard Docker/restore qualification; and additional offboarding/restore cases.
Euro-Office health/callback success is not provenance or editing qualification.

The owner confirms iOS App Store publication. macOS Developer ID delivery remains a
separate unfinished gate. The Mac is intentionally powered off and receives no operations.
No merge, tag, release or deployment is part of these recorded acceptance results.
