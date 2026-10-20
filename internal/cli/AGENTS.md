# Purpose

Plain terminal planning and command orchestration for the operator-run installer.

## Ownership

- Guided questions, resume answers, plan review and publishing: `guided.go`.
- CLI dispatch, target connections and locked deployment: `cli.go` and `driver.go`. `apply`
  and every plan change give a plan its `installation_id`, published after the steps pass;
  markers record it only once published. `--adopt-edge-hosts` and `--take-over-target` are
  apply-only. A `kube://` edge gets its token and tunnel from the
  Kubernetes driver; an app's `node_ports` override the catalog's in the session only.
  `version` prints the release set; `version --runner-image` prints `Options.RunnerImage` or
  fails "this build pins no suite runner image (source build); use a release binary".
- `suite.go` owns `install`, `uninstall`, `unmanage`, `upgrade` and `status`, the upgrade gate
  and plan publication. The exported `Install`, `Uninstall`, `Unmanage`, `Upgrade` and `Status`
  take a context, state directory and `Options` (with `Progress`) so the suite runner calls
  them without flag parsing; they return `internal/suite` errors and status.
- `handoff.go` owns `handoff` (grant, `--revoke`) and `pull-plan`: it reaches the runner only
  through the KyYard target driver's `execRunner` (`docker exec` over SSH, or `pods/exec` into
  the one runner pod `--runner-pod` selects, permission-checked first), holds every target lock
  while granting, and publishes the plan before markers change owner. `handoff.json` in the state
  directory makes `plan` refuse while the runner owns the plan. `session.guard` is the
  ownership rule for every locked command and CLI `status`.
- Explicit existing KyIdentity container setup and capability checks: `identity.go`, documented in `../../docs/guided-setup.md`.
  It also owns `identitySetupStep`, apply's staged setup for a catalog KyIdentity, reusing the same
  staging and setup functions; `nativeProductExec` finds an installed app's pod per call.
  `refuseMovedSetup`, first in `deploySteps`, refuses a moved issuer or catalog-derived client
  of a completed setup with `issuer_changed`.
- `preparation.go` owns shared bounded native preparation receipts/readback and typed Identity
  staging; product-specific metadata and credentials remain native product responsibilities.
- `credential_transfer.go` owns explicit Notes `identity_client` transfer: require both native
  capabilities before destination writes, strictly validate bounded private export against
  saved intent, resolve native owner metadata, stage/import/check and re-export source before
  ordinary setup. Clear private byte buffers and return fixed import/export failures.
  Connect only declared source/destination; reuse destination lock and existing transports.
  Product setup clears edge reconciliation scope and reads no NPM credentials.
  Guided resume retains the selection and review discloses it. Real SSH and restricted kind
  transfer and native TLS setup are qualified. SSH also qualifies installer-composed native
  HTTP sign-on, reciprocal owner/everyday Notes access and post-login inspect/resume;
  browser UI, MFA, Kubernetes sign-on and full lifecycle acceptance remain open.
- `backup.go` owns `backup` and `restore` (Docker apps only; `docs/guided-setup.md` "Backups
  and restore on Docker"). Both refuse before connecting a Kubernetes app, an app without a
  `backup` block or a target without a destination (`backup_unconfigured`), backup volumes in
  named Docker volumes (`backup_unsupported`) and a plan without `installation_id`; both hold
  the app's target lock (restore also KyIdentity's) and are refused under `owner: runner`.
  `dockerhost.Store` does the target work. `removalGate` refuses restoring an app with an
  `oidc` block unless the plan's KyIdentity deployment record is `replayVersion` (0.4.4) or
  later (an `existing` KyIdentity is refused); `replayRemovals` runs `resync-system --client
  <identity_client> --wait 10m` through the target driver in KyIdentity's container (`kube.ExecReport` on Kubernetes, managed-service `docker exec` on Docker) and accepts only `delivered`, else `restore_incomplete`. `restoreLocked` refuses a
  running app and existing data without `--replace` before contacting the repository, then a
  foreign snapshot; `--replace`/`--yes` without `--snapshot` are refused; it wants the typed name
  or `--yes`, reads a lost password from `Options.Stdin` (no echo on a terminal) and records
  its stage in `.restore-pending`, so a rerun resumes; while that file exists every other
  Docker operation on the app refuses: deploy (`apply`, `install`, `upgrade`; `App.Restoring`
  is set only by restore), `backup` and `unmanage` (`dockerhost.refuseUnfinishedRestore`). `finishRestore` deploys through
  `dockerhost.RestoreSteps` and `Steps`, replays removals as soon as the app is healthy, then
  runs the setup steps' `Verify`; for a gated app any failure before the replay is delivered
  stops the app (decision 11). A cold `backup` says it stops the app and, on failure, whether
  it runs again. `backupLines` adds
  the backup, restore and password-record lines to apply's handover.
- Product adapter discovery, bounded reports and activation verification: `product.go`; only implemented product interfaces enter its registry.
- `immich.go` owns initial setup dispatch for the pinned community Immich module on
  Docker only: require native capabilities/version, validate bounded account/role reports,
  compare installation/UUID/subject readback, and render fixed labels/native UUIDs only.
  Pending initial setup is an error. This does not admit Immich or prove ongoing lifecycle,
  actual KyIdentity delivery or administrator ordinary-workflow separation.
- `integration.go` owns guided setup selections and unattended `integrate-stack` dispatch,
  which skips installed apps' entries (`apply` runs those).
  Validate every selection before connecting; run KyIdentity first and otherwise preserve
  saved order. Optional typed KyNotes and KyIdentity preparation settings are retained by guided resume,
  disclosed in plan/review and sent over bounded native CLI stdin. Verify the prepared
  bundle before applying; `--check` compares metadata without creating files. Reuse each
  product's target lock, native adapter and live verification. Identity requires native
  `bundle-preparation`, `target-credentials` and `protected-setup-files`, generating
  credentials only on its target and refusing changed/missing preparation state;
  emit only its sanitized report. First failure/cancellation stops remaining products,
  preserving completed mutations for native idempotent resume. Reject unsupported
  product/target combinations across the entire batch before connecting. Interactive host-key
  prompts remain visible and separate from report serialization.
- kyPulse additionally requires native monitoring-target, log-source and offboarding-alert
  setup capabilities. Receipts use ordinary configuration objects with stable target/source
  keys and native IDs; credentials stay in protected target files. Its optional binary SSH
  fixture verifies account/configuration activation plus live monitoring enrollment and
  source capability without adopting foreign objects or mutating live installations.
  Apply's catalog kyPulse reuses that adapter (`pulseSetup`) through `clientSetupStep`;
  `pulseTargets` makes its bundle's `monitoring` targets, one per other routed app.
- KyYard uses the common contract with zero-restart activation, signed directory updates
  and exact `kyyard.admin` assignments. Platform administrators receive no implicit tenant
  access; organization/endpoint composition and shared upgrade orchestration remain gates.
  `yard.go` owns the catalog KyYard (admin-only client, `yardSetup` through `clientSetupStep`
  with no everyday account): `yardPlan` (plan, review, every command) refuses owner admin usernames its setup refuses; `runnerImage`/`checkRunnerImage` (plan, review,
  preflight, apply, install, upgrade on the workstation) refuse deploying an app with
  `kubernetes.suite_runner` without a digest-pinned `Options.RunnerImage` (empty: "source builds
  pin no runner image; use a release binary"); `yardHandover`
  prints the break-glass and `handoff` lines. Under the runner (`Options.RunnerID`),
  `deploySteps` replaces that app's workload with the `<app>.runner` note step, reading and
  writing nothing of it, and `refuseOwnPod` refuses `install`, `uninstall`, `unmanage` and
  `upgrade` naming it (`runner_host`); `guard` refuses a workstation `apply`/`upgrade`
  (`session.deploys`) that would redeploy it while the runner owns the plan.
- `post.go` maps KyPost native capability/configuration reports, requires nonmutating inspection, preserves external readiness blockers and runs Docker setup explicitly as `kypost`.
  It also holds the catalog KyPost's native bundle (`postBundle`) and handover (`postHandover`).
- KyCalendar uses the common contract and graceful activation; owned issuer bindings and application grants remain separate from personal calendars and CalDAV, and sign-in settings are read-only in its administration screen.
- KyMessages uses the common contract and graceful activation; exact console grants stay
  separate from Matrix chat. `message.go` owns the catalog KyMessages (Docker only):
  `messagePlan` (plan, review, every command) refuses a plan without `installation_id`, a
  `chat_client` other than `<app>-chat`, owner usernames its setup refuses and an unsafe
  `identity.mail.password_file`; `withRoutes` checks its six roles (one edge, one forward_host,
  one forward_port per service port) and fills its role references; `routePaths` gives the
  edges' routes their catalog paths. `composeClients` derives the console client and
  `chatClientFor`'s everyday-only MAS client (redirect at the auth host with `providerID`, a
  ULID derived from installation_id), both in the first setup or one amendment.
  `messageSteps` puts `<app>.matrix-init` (seed `matrix/secrets/upstream_provider_id`, refusing
  another with `provider_id_mismatch`, then `dockerhost.RunInit`) and `<app>.credentials`
  (both clients' export, `hostFile` writes on the host, never replacing, then the second pass)
  before its deploy, and its `clientSetupStep` (everyday binding, chat client rechecked by
  `after`) after its health. `refuseMovedServerName` refuses `server_name_changed` from the
  installed Synapse's `homeserver.yaml`. `identitySetupStep` copies `identity.mail`'s workstation
  password file (`readMailPassword`) to KyIdentity's `<installer>/smtp.password` before staging,
  and requires `mail-relay`. `messageHandover` and `initAdminLine` are what apply prints.
- KyRecovery setup runs explicitly as `kyrecovery`, activates live, commits exact administrator grants with its keyed SQL audit, and never performs the custodian ceremony.
  `appsetup.go` owns apply's catalog apps that sign in through KyIdentity (KyRecovery,
  kyPulse, KyNotes, KyCalendar and KyYard on Docker or Kubernetes, KyDrive, KyPost and KyDNS on Docker): `composeClients` adds each one's catalog-derived client to the installed
  KyIdentity's intent in the session only (the saved plan is unchanged; a hand-written client
  must match), `clientSetupStep` (`<app>.setup`) exports that client, places bundle and secrets
  with `placeScript` (secrets no-clobber, kyPulse's bundle replaced, compare-only on check)
  through the app's shell, on another target than KyIdentity's too, and runs the product
  adapter's setup; on Docker `nativeProductExec` finds the app's one running managed container
  per call (`dockerAppContainer`). A client without a directory gets no directory secret; an
  `oidc.scim_token_file` goes into the bundle, and `scimStep` (`kyidentity.scim.<app>`, which
  `deploySteps` puts after the edges) reads that token through the app's driver (`appSecret`),
  places it in KyIdentity's pod with `placeScript` (never replaced) and applies `scimPass`, the
  registering setup's pass with `scim` added. KyCalendar admits only the identities its setup names: its step
  adds `everyday`, the owner's everyday account, whose subject `everydaySubject` takes from
  KyIdentity's read-only `--check` of its first setup (the one reused `user` with the plan's
  everyday username; any other report is a fixed refusal). `calendarAccounts` (plan, review
  and apply) refuses owner usernames KyCalendar's setup would refuse. KyPost's step sets
  `native` to `postBundle`, its version-1 bundle (sign-in with `autoProvision`, `clientSecretFile`
  beside it), placed and applied as `kypost` through `postSetup`; KyPost has no directory (it
  cannot verify KyIdentity's generated webhook secret), so no directory secret. `recoveryCeremony`,
  `pulseHandover`, `calendarHandover`, `driveHandover`, `postHandover`, `dnsHandover` and `yardHandover` are what apply prints.
  KyDNS's distroless image has no shell, so `hostPlace` runs `placeScript` on its Docker host
  over SSH as the target user, mapping the bundle's `/data/installer/<uuid>/` to
  `<root>/kydns/data/installer/<uuid>/` (anything else refused, symlinked directories refused),
  and `waitForSocket` holds its setup until `<data>/installer/setup.sock` exists (no Compose
  healthcheck). `gateStep` (`kydns.gate`, after its setup, before the edges) sends the
  workstation's direct request with `X-Forwarded-Proto: https` to its admin port and passes only
  on 403, so an `egress_address` other peers share (a Docker gateway hiding clients) fails apply.
  `edgeTrustStep` (`kydns.edge`, setupSteps' second return, so it runs after the edges) curls
  the same URL from the catalog NPM's pod and passes only when KyDNS reports the edge's headers
  trusted over https; KyDNS's route on any other edge is refused.
  `withRoute` fills a Compose template's route references for both `newSession`
  and `validateSelection`, so `{{edge_address}}` without the edge's `egress_address` is refused
  by `plan` and `review` too.
  KyNotes' distroless image has no shell: `notesSetupStep` (`kynotes.setup`) reuses
  `transferNotesCredentials` and `notesSetup` with the preparation `notesPreparation` derives
  (catalog client, KyIdentity's issuer, secrets beside the fixed bundle, backup from its env);
  `deploySteps` holds it and KyNotes' route (`routedFirst`, `edge.RouteApp`) until after the other
  edge steps, setup first. `notesHandover` names the KyRecovery pairing as a human step.
  `amendLate` (apply only, read-only, before preflight) records an `identity_amendment` for each
  app whose client the running KyIdentity's retained setup lacks, all or one, when it advertises
  `setup-amendments`; composeClients then puts that client in an `identityAmendment`, whose
  bundle `identitySetupStep` stages as `kyidentity.amend.<app>` and whose path the app's export
  and `movedClient` read. Without the capability `identitySetupStep` names
  KyIdentity-server#110 and 0.4.3. `retireBootstrapStep` (`kyidentity.retire-bootstrap`) runs
  `disable-user` when advertised, else leaves `bootstrapNote` for apply to print.
- KyDNS uses the common contract, exact administrator subjects and live setup (`dnsSetup`); its interface refuses ordinary identities and unsupported installer backup enrollment. The catalog KyDNS runs on Docker only.
- `notes.go` maps the native KyNotes version-1 capability/report contract, requires `dedicated-admin`, `protected-setup-files` and present-only read-only inspection; older mixed-use or unprotected-file builds are refused before setup. It does not replace KyNotes security or backup logic.
- Morning test instructions and supported scope: `../../docs/guided-setup.md`.
- Stack schema, app admission and target implementations remain owned by the root contract.

## Local Contracts

- All catalog Ky products with Kubernetes manifests also have Docker templates. Docker resolves issuer/callback metadata from the same route, runs as the target user, and retains product-owned setup. `docker_catalog.go` adds KyYard's digest-pinned runner with private state/socket binds; only the managed `app` service is a native product exec target. Issuer drift, late amendments and removal replay work on both targets.

- `plan` changes only local planning files. Remote preflight or deployment requires choosing
  `check` or `deploy`; each run defaults to `finish` regardless of the previous action.
- Persist validated answers after every question; publish `stack.yaml` atomically only after
  full validation and confirmation. An interruption preserves the published plan.
- Keep credentials out of prompts and state. Read actual deployment credentials through target drivers.
- Plan/review names the required digest-pinned temporary credential generator for every
  selected Kubernetes workload with declared secrets; its execution remains in the target
  driver and never runs during planning.
- `existing` records references only. It grants no adoption, mutation, administrator access or SSO integration.
  Report integration as pending until a product adapter proves it.
- Outside demo mode, offer supported referenced/selected products a separate setup choice,
  default no. Save explicit container/bundle references and optional typed secret-free
  KyNotes/Identity preparation metadata in `integrations`; actual credential values stay on target. Demo
  planning offers no real-product setup by default. Resume retained selections, and
  declining setup removes intent while preserving the installation reference. Planning
  and its finish/check/deploy actions do not execute selected product integrations.
- `integrate-identity` explicitly authorizes container-local setup on the selected target.
  Require the advertised product contract, hold the target lock for apply, and run the
  product's read-only check afterwards. Preserve workload ownership and existing data.
  A protected bundle is referenced inside the container; render only validated report fields.
- KyIdentity must advertise native per-app role/group assignments, directory groups and
  SCIM connector/group setup and signed suite-webhook connectors.
  Connector receipts expose only kind, client name and native ID; the bounded report accepts
  at most 162 objects (two accounts, up to 32 groups and 32 clients, each with two roles and a connector).
  Role receipts use a bounded
  `client/role` name and contain no credentials. Read-only setup rejects creation reports.
  Group receipts allow bounded ASCII group names including ordinary spaces; reports omit
  arbitrary native fields. The optional binary SSH fixture includes actual owned group,
  app-group, SCIM group and signed suite connector configuration with present-only inspection/resume.
  Dedicated owner/everyday role claims do not prove downstream authorization or separation.
- `integrate-product` shares the explicit target selection and locking path. Require product capabilities, reject mutated inspection reports and never count pending activation as ready. Request the product's graceful restart only during apply and recheck its live configuration before reporting success.
- Demo selection is explicit and saved in the stack. Its catalog is separate from production;
  the diagnostic service is never described as a Ky product or a completed suite installation.
- Skipping an installed product preserves its workload and data.
- Accept computer DNS names at the initial target prompt, deriving a safe internal label.
  Use the entered computer name as the Docker SSH address default.
- `review` and `plan --noninteractive` validate a saved plan without target connections.
- `plan` asks a Docker target's backup destination (not in demo mode; blank for none) after
  its directory; `review` lists it. It holds paths only, never the rest-server password.

## Work Guidance

Use one question per plain terminal prompt and show defaults. Name unsupported capabilities
before deployment rather than reporting their absence as success.

## Verification

- `make ci`: guided resume, declined edits, existing references and credential rejection tests;
  `backup_test.go` drives backup and restore over a real sh with `dockerhost/fakedocker` as
  docker, including a kill at every command; `golden_test.go` pins every Ky app plan's step
  inputs and KyIdentity intent (`-update-golden` rewrites it, only for an intended change).
- `make e2e`: a guided demo plan drives a real SSH/Docker deployment; Kubernetes container exec and edge regressions also run.
  With `KYQ_TEST_IDENTITY_BINARY`, real SSH and restricted kind fixtures check target-held
  preparation, creation/check/resume and changed-intent preservation; SSH also verifies
  missing owner credentials are never regenerated. Optional Drive, Notes, Post, Pulse, DNS, Forge, Mark, Recovery, Message, Calendar and Yard binary fixtures exercise the same actual SSH path; Post also requires an explicit local fixture image and preserves external acceptance blockers.

## Child DOX Index
