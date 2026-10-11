**Repo:** KyQuickStart and Ky suite server repositories
**Worktree:** /home/yoshi/git/busnes.app/KyQuickStart (branch feat/phase-4-edge)

# Adapter implementation handoff — 2026-10-10

User requested this updated handoff and explicitly asked to stop afterward. Pause the
active goal after the exact mirror is verified; resume only when requested. The all-product
implementation is unfinished. Notes has the
strongest composed acceptance; remaining preparation, credential composition, third-party
interfaces and whole-stack acceptance require substantial work. No percentage or delivery
estimate has been established. No live services were changed, and no commits, PR operations,
merges or deployments occurred in the latest work.

## Resume first

1. Read this note, the applicable AGENTS.md hierarchy and the shared board folder
   `kyquickstart-installer-design`; claim it before implementation. This handoff releases
   the folder to `open`. Previous instance name: `basalt`, author note
   `Usagi / gpt-6.1-sol / basalt`.
2. Preserve the large uncommitted worktree changes, including other agents' changes.
   Detailed history and product inventory are in `2026-10-09-all-product-adapters.md`
   beside this file; read bounded sections rather than loading the entire ledger.
3. Address the newly identified unsupported-recovery preparation gap and remaining
   verification described below before extending the interface to other products.
4. Wire typed Drive preparation and scoped native owner/everyday credential composition
   into the installer. Then continue every supported Ky server and admitted third-party
   product; a first working adapter is not completion of the user's request.

## Working locations

| Repository/worktree | Branch | Current focus |
| --- | --- | --- |
| `/home/yoshi/git/busnes.app/KyQuickStart` | `feat/phase-4-edge` | CLI, saved integration graph, Docker/Kubernetes transport and fixtures |
| `/home/yoshi/git/busnes.app/ky-primitives-installer-setup` | `feat/installer-setup` | Unpublished shared `applysetup` package |
| `/home/yoshi/git/busnes.app/KyDrive-installer-setup` | `feat/installer-setup` | Native `cmd/server/installer.go` preparation |
| `/home/yoshi/git/busnes.app/KyIdentity-installer-setup` | Verify locally | Native preparation and scoped client export |
| `/home/yoshi/git/busnes.app/KyNotes-installer-setup` | Verify locally | Native preparation and scoped client import |

Use the primitives **installer worktree**; the main primitives checkout lacks this
unpublished package. Product worktrees contain additional existing changes; preserve them.

## Completed and verified

- Identity-to-Notes native credential transfer is wired behind the explicit saved
  `identity_client` selection. The installer validates the graph before connections,
  resolves the native owner, transfers private stdout/stdin in memory, stages/imports,
  inspects the destination and rechecks the source before ordinary setup. Credentials
  and guessed owner subjects do not enter workstation installation state.
- Actual SSH acceptance qualifies installer-created owner/everyday accounts signing in
  through password/CSRF, discovery, PKCE, authorization code, confidential token exchange
  and JWKS. Dedicated owner gets Notes administrator access and is refused ordinary
  content access; everyday identity gets the inverse. Native inspection/resume succeeds
  after both sign-ins. This is HTTP protocol acceptance; browser UI and MFA are open.
- Restricted kind acceptance qualifies actual native private transfer, named CA-verified
  TLS, canonical owner/credential binding, apply/check/resume, changed-file preservation
  and absence of secrets in workstation state. It does not prove production edge topology,
  NetworkPolicy enforcement or Kubernetes downstream sign-on.
- Latest shared preparation race tests passed: `applysetup` 1.025s and `keyfile` 1.063s,
  log `/home/yoshi/.cache/kyquickstart-work/kyq-common-preparation-final.log`.
- Final Drive preparation race test passed after the last test refinement: `cmd/server`
  1.023s, log `/home/yoshi/.cache/kyquickstart-work/kyq-drive-preparation-final.log`.
- Shared root dependency-boundary race tests passed, 1.008s
  (`kyq-preparation-nodeps.log`); whole shared-module `go vet ./...` and `go build ./...`
  passed (`kyq-preparation-primitives-vet.log`, `kyq-preparation-primitives-build.log`).
- Actual Docker `TestDockerFixtureLimits` passed 0.879s. It inspects HostConfig from three
  owned containers, proving defaults, selected-memory/no-extra-swap and explicit equal-sign
  memory/swap/CPU/PID override preservation. Source `test/e2e/docker_limits_test.go`, log
  `kyq-docker-fixture-limits.log`. Owned containers were removed by test cleanup.
- Actual static headless Drive binary built and passed native CLI capabilities, absent
  check, stage/check/resume, private modes, changed-owner preservation, arbitrary destination
  refusal, incompatible flag refusal and no invented secret/socket. Binary
  `/home/yoshi/.cache/kyquickstart-work/kydrive-preparation-headless`; runnable check
  `drive-preparation-cli-check.py` and log `kyq-drive-preparation-cli.log` in that directory.
  Disposable data was cleaned. This is command-interface proof, not a UI release build.
- Final installer `make ci` passed tidy diff, formatting, ordinary/e2e-tag vet and whole
  race suite (`kyq-preparation-fixture-ci.log`; CLI 2.386s, offboard 27.104s, stack 1.040s).
  No build/test process remains running from this checkpoint. New test/helper sources
  match these results; later changes were documentation only.

Earlier completed acceptance logs, which predate the newest fixture-helper edit:

- SSH composed sign-on: `kyq-composed-signon-proof-ssh.log`, TestApply 44.10s.
- Installer full CI: `kyq-composed-signon-ci.log`, passed tidy/format/vet/race.
- Kind transfer: `kyq-client-transfer-kind.log`, TestKubernetesApply 115.69s.
- Kind checkpoint CI: `kyq-client-transfer-kind-ci.log`, passed.

Those logs are under `/home/yoshi/.cache/kyquickstart-work/`. Do not attribute their results
to later source edits.

## Latest implementation, still needing closeout

Shared primitives `applysetup/stage.go`, `stage_linux.go`, `stage_other.go`,
`stage_test.go`, and modified `setup.go` implement:

- Explicit `ParsePreparation` opt-in for `--stage`; legacy `Parse` refuses the flag.
- Strict preparation credential-reference validation: clean absolute, distinct,
  bounded paths without control characters; no bundle aliasing.
- Linux protected-directory ancestry checks and immutable, no-clobber canonical metadata
  at `<data>/installer/<setup UUID>/setup.json`. Check creates nothing; changed or unsafe
  retained input is refused. Non-Linux staging fails closed.
- Fixed secret-free `ky.setup-preparation/1` receipt. Stage callers must first strictly
  decode and validate their product's canonical metadata, including product extensions.
  The filesystem helper does not supply product validation or create credentials/accounts.

Drive `cmd/server/installer.go` now advertises bundle preparation and handles bounded
stdin metadata before socket/configuration work. It validates service/callback/common
metadata and requires receiver SCIM-token references for SSO, rejecting the Notes webhook
substitute. `installer_prepare_test.go` covers creation, check/resume, conflicting intent,
unknown/oversized input, safe output and no invented credentials/socket.

Installer `test/e2e/e2e_test.go` now injects missing Docker-run fixture defaults:
512 MiB memory, swap equal to selected memory, one CPU and PID128. Explicit resource flags
are preserved. Actual Docker acceptance now passes, as recorded above.

DOX closeout is now complete for this implementation: shared primitives root,
`applysetup/AGENTS.md` and README; Drive root and `docs/INSTALLER-SETUP.md`; installer root
fixture guidance. Existing child indexes and Drive docs child remain unchanged because no
ownership boundary/index changed. Diff checks passed in all three worktrees.

Remaining closeout:

1. Closed 2026-10-10 (saxifrage): Drive preparation now refuses `backup.recovery` before
   staging, with a no-mutation regression in `installer_prepare_test.go`. Mutation-checked:
   the test fails with the guard removed. Logs `kyq-drive-recovery-refusal.log` and
   `kyq-drive-recovery-full.log`. Drive full race suite passes except
   `TestInstallerSocketActivationAndAdministratorSeparation` under the long disk-scratch
   TMPDIR: the socket path exceeds the 108-byte `sun_path` limit. It passes with
   `TMPDIR=/tmp`. A long production `KY_DATA_DIR` hits the same opaque bind error.
2. Drive's `web/dist` currently contains only `.gitkeep`; command tests/headless binary do
   not qualify a production UI release. Rebuild the UI before release/full UI acceptance.
3. Shared-module checks cover focused preparation/keyfile race, root boundary race and
   whole build/vet; whole shared-module race, macOS and vulnerability jobs were not rerun.
4. Keep the CLI fixture and real Docker proof distinct from full SSH/kind suite reruns;
   earlier composed SSH/kind logs predate the fixture-helper change.
5. Mirror any further local status/handoff to myslop before stopping. This handoff changes
   no product behavior or ownership boundary, so parent instructions/indexes are unchanged.

## Review pass 2026-10-10 (saxifrage)

Four read-only reviewers covered CLI/offboard, adapters, transport/edge and product
worktrees. Fixed, each with a regression test confirmed to fail without the fix:

- Offboard `group_membership.go`: a repeat DELETE of a retired group no longer runs native
  I/O (a refusal used to overwrite the retirement proof and let PUT revive the group); a
  `refused` directory job now holds memberships (it records the requested, not native,
  profile, so a refused demotion let an administrator join groups).
- CLI `product.go`: Kubernetes targets are refused for adapters with a runtime user
  (kypost, kyrecovery); Kubernetes exec silently dropped the user.
- Primitives `keyfile.openKey`: non-regular paths are refused before open; a FIFO used to
  hang `ReadPrivate` forever (Linux test `fifo_linux_test.go`).
- Primitives `applysetup.StartLocal`: socket paths over 107 bytes get a named error.
- Drive `internal/config/installer.go`: installer refuses to enable SSO the operator
  disabled (`KY_SSO_ENABLED=false`); new `internal/config/installer_test.go`.
- `.gitignore`: `__pycache__/` and `/morning-test/` (personal cluster plan; not deleted).

Checks: installer `make ci` passed (`kyq-review-fixes-ci.log`); Drive vet/race passed
except the known TMPDIR socket-length test, which passes with `TMPDIR=/tmp`
(`kyq-drive-review-fixes.log`); primitives vet plus keyfile/applysetup race passed
(`kyq-primitives-review-fixes.log`). The bounded runner is `bounded.sh` in the scratch dir.

Rejected: "encoding/json folds Unicode keys (ſ→s)". Probed on Go 1.26.6 and 1.27.1: both
reject `ſalue`/`valuK` as unknown fields, so `uniqueJSON` is correct.

Confirmed open, highest first:

1. ACCEPTED RISK (Yoshi, 2026-10-10; recorded in `immich/module/README.md`), not open:
   Immich setup (`immich/module/source.patch:594-673`) ignores pre-existing admins and
   never disables `passwordLogin`; a first-run sign-up admin survives with password access.
   Refuse setup when any other user exists and set `passwordLogin.enabled=false`. Needs
   patch spec tests and fixture rebuild.
2. BookStack default `admin@admin.com` is never retired (unproven at deploy level).
3. BookStack ordinary-role policy is a denylist; `restrictions-manage-all` passes. Use an
   allowlist. Role middleware locks role rows on every request (availability).
4. Immich `ConfigureProvider`/`SetAdministrator` are exported test-only bypasses; move to tests.
5. Edge: (connector IP vs IPAM `IPRange` fixed in PR #7: preflight requires a reserved range); NPM host ownership keyed
   by target name only (two installs collide); removed routes are never disabled in NPM.
6. Product: Identity export resolves owner by username after the ID check
   (`setup_export.go:73`); Notes SSO save and audit row are separate writes; Drive refusal
   reasons are discarded (`local.go:81`); Drive dedicated-admin check uses setup-time role.
7. Offboard lows: refused creation can never retry with corrected data; directory write
   lacks a post-I/O revocation recheck; audit receipt count does not check unique sequences;
   orphan `tmp-*` files are never swept; cancellation recorded as native failure.
8. CLI lows: guided NPM-token prompt names a host path for Kubernetes targets; stale
   `plan-answers.json` overrides a hand-edited `stack.yaml`; `plan.lock` is not reclaimed
   after a crash; activation-wait parse error aborts instead of retrying.

## Remaining full scope

- Common native apply interfaces exist across Identity, Drive, Notes, Pulse, Post, DNS,
  Forge, Mark, Recovery, Message, Calendar and Yard, but most still need manually supplied
  bundles/target credential files. Build secret-free preparation, native credential import,
  canonical owner/everyday composition and real lifecycle qualification for all of them.
- Drive needs receiver-owned SCIM-token generation and reverse transfer to Identity's
  connector. Notes' source-generated directory signing key cannot substitute. Current
  Identity export intentionally omits SCIM tokens and bootstrap passwords and carries
  owner metadata only; everyday composition needs a deliberate validated interface.
- Pulse has extra native monitoring metadata: preserve and validate its product envelope
  before shared staging; do not silently discard extensions.
- Forgejo, BookStack, Immich and LibreChat have partial native adapter work; complete their
  admission/owner/lifecycle gates. Home Assistant, Dolibarr and Holm remain incomplete.
  Check the current inventory before assuming an old snapshot is a blocker.
- Vaultwarden only for new password installs. Native KyIdentity administrator integration
  remains open; its token-only admin portal is insufficient. Preserve old KeePass KyVault
  installations and data.
- Fresh guided inputs, mixed-use account migration, administrator/everyday separation,
  directory/offboarding lifecycle, owner MFA/printable handover, sealed and independent
  bulk backup/restore, source/build provenance and full-stack deployment acceptance remain.
  Production catalog is gated; successful interface tests do not open its release gates.

## Memory and safety requirements

Run builds/tests serially with disk scratch `/home/yoshi/.cache/kyquickstart-work`;
`/tmp` is RAM-backed. Use `GOMAXPROCS=2`, `GOMEMLIMIT=1GiB`, `GOFLAGS=-p=1`, that scratch
as `TMPDIR`, and a transient systemd user unit with MemoryMax=2G, MemorySwapMax=0,
TasksMax=128 and CPUQuota=200%. Bound owned containers separately. Static fixtures use
`CGO_ENABLED=0`. Preserve unrelated containers, including the Synapse acceptance container.

Drive tests use `GOWORK=/home/yoshi/.cache/kyquickstart-work/drive-preparation.work`;
the old `/tmp/kyq-adapters.work` is gone. Existing static binaries are
`kyidentity-client-export` and `kynotes-client-import` in the disk scratch directory.

Preserve retained credentials and foreign data. Use product-owned native commands;
raw target file access in owned tests does not authorize installer file scraping.
Do not weaken symlink, ownership, TLS or literal-loopback guards to pass fixtures.
Shared staging protects its own directory ancestry; existing generic private-file decode
has not thereby gained global ancestor validation.

NPM choice, domains, Cloudflare/VPS and target connections are runtime installer inputs,
not development blockers. Live testing requires authorization for that deployment.
Historical PR #3/reviewer-loop status in the initial conversation was not reverified in
this work; verify remote state before any merge or branch/worktree cleanup.
