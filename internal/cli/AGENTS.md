# Purpose

Plain terminal planning and command orchestration for the operator-run installer.

## Ownership

- Guided questions, resume answers, plan review and publishing: `guided.go`.
- CLI dispatch, target connections and locked deployment: `cli.go` and `driver.go`.
- `suite.go` owns `install`, `uninstall`, `unmanage`, `upgrade` and `status`, the upgrade gate
  and plan publication. The exported `Install`, `Uninstall`, `Unmanage`, `Upgrade` and `Status`
  take a context, state directory and `Options` (with `Progress`) so the suite runner calls
  them without flag parsing; they return `internal/suite` errors and status.
- `handoff.go` owns `handoff` (grant, `--revoke`) and `pull-plan`: it reaches the runner only
  through `docker exec` of serve-* over the KyYard target's SSH, holds every target lock while
  granting, and publishes the plan before markers change owner. `handoff.json` in the state
  directory makes `plan` refuse while the runner owns the plan. `session.guard` is the
  ownership rule for every locked command and CLI `status`.
- Explicit existing KyIdentity container setup and capability checks: `identity.go`, documented in `../../docs/guided-setup.md`.
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
- Product adapter discovery, bounded reports and activation verification: `product.go`; only implemented product interfaces enter its registry.
- `immich.go` owns initial setup dispatch for the pinned community Immich module on
  Docker only: require native capabilities/version, validate bounded account/role reports,
  compare installation/UUID/subject readback, and render fixed labels/native UUIDs only.
  Pending initial setup is an error. This does not admit Immich or prove ongoing lifecycle,
  actual KyIdentity delivery or administrator ordinary-workflow separation.
- `integration.go` owns guided setup selections and unattended `integrate-stack` dispatch.
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
- KyYard uses the common contract with zero-restart activation, signed directory updates
  and exact `kyyard.admin` assignments. Platform administrators receive no implicit tenant
  access; organization/endpoint composition and shared upgrade orchestration remain gates.
- `post.go` maps KyPost native capability/configuration reports, requires nonmutating inspection, preserves external readiness blockers and runs Docker setup explicitly as `kypost`.
- KyCalendar uses the common contract and graceful activation; owned issuer bindings and application grants remain separate from personal calendars and CalDAV, and sign-in settings are read-only in its administration screen.
- KyMessages uses the common contract and graceful activation; exact console grants stay
  separate from Matrix chat/call access, whose assignment composition remains a gate.
- KyRecovery setup runs explicitly as `kyrecovery`, activates live, commits exact administrator grants with its keyed SQL audit, and never performs the custodian ceremony.
- KyDNS uses the common contract, exact administrator subjects and live setup; its interface refuses ordinary identities and unsupported installer backup enrollment.
- `notes.go` maps the native KyNotes version-1 capability/report contract, requires `dedicated-admin`, `protected-setup-files` and present-only read-only inspection; older mixed-use or unprotected-file builds are refused before setup. It does not replace KyNotes security or backup logic.
- Morning test instructions and supported scope: `../../docs/guided-setup.md`.
- Stack schema, app admission and target implementations remain owned by the root contract.

## Local Contracts

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

## Work Guidance

Use one question per plain terminal prompt and show defaults. Name unsupported capabilities
before deployment rather than reporting their absence as success.

## Verification

- `make ci`: guided resume, declined edits, existing references and credential rejection tests.
- `make e2e`: a guided demo plan drives a real SSH/Docker deployment; Kubernetes container exec and edge regressions also run.
  With `KYQ_TEST_IDENTITY_BINARY`, real SSH and restricted kind fixtures check target-held
  preparation, creation/check/resume and changed-intent preservation; SSH also verifies
  missing owner credentials are never regenerated. Optional Drive, Notes, Post, Pulse, DNS, Forge, Mark, Recovery, Message, Calendar and Yard binary fixtures exercise the same actual SSH path; Post also requires an explicit local fixture image and preserves external acceptance blockers.

## Child DOX Index
