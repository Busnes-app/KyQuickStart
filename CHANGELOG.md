# Release notes

Human summaries and upgrade impact for published KyQuickStart versions through v0.4.2.7.
Subsequent versions publish their notes on the [release page](https://github.com/Busnes-app/KyQuickStart/releases). These
notes describe the tagged source and published release records; they do not certify
an installation or fill the unfinished acceptance gates in [the redirect record](docs/redirect.md).
Four-part versions are maintenance releases on the corresponding three-part line.

Before replacing an installer, [verify its binary and matching runner image](docs/VERIFYING-RELEASES.md).
Replacing the workstation binary alone does not deploy anything. Review an existing
plan before running `apply` or `upgrade`: catalog pins and rendered configuration can
change, and product-specific migration/backup acknowledgements still apply. Keep the
state directory, target data and target-held credentials; do not reset them as an upgrade.

## Unreleased

- Node-failure recovery on Kubernetes (owner amendment, 2026-11-11). Every app's pod now leaves
  a node 30 seconds after it is marked unreachable instead of five minutes, prefers a node with
  fewer of the suite's pods, and has TCP startup and liveness probes. **Upgrade impact:** the
  next `apply` or `upgrade` restarts every Kubernetes app once, and pods may move to other
  nodes. Apps remain one replica; storage must release a dead node's volumes for the move to
  finish ([guided setup](docs/guided-setup.md#node-failure-on-kubernetes)).
- Hostile-review fixes (council pass 1). Backup and restore: kyPulse's `restic_password` is no
  longer in its own snapshots and survives `restore --replace`; KyMessages stops its console
  for the dump and snapshot so its SQLite is at rest (chat stays up); a backup on a rebuilt target
  no longer replaces the repository password it cannot open; restore refuses a crash-looping
  app and a snapshot from a newer version or outside the upgrade rules; KyDrive with
  `euro-office` backs up `editor_secret`; KyPost's volume migration no longer loops on an empty
  volume or blocks its restore. Upgrades: `upgrade euro-office` no longer panics; a Docker app
  that does not come back healthy after a verified pre-upgrade snapshot is still upgraded;
  on Kubernetes an app is stopped only once its pods are gone, and a failed deploy no longer
  records the new version. `plan` keeps the fields it does not ask (`stopped`, `node_ports`,
  LoadBalancer addresses, `recovery_key_id`, `egress_address`, `lan_dns`, `identity_amendment`,
  `domains`), accepts a `kube://`
  `npm_url` and an installed app's setup, and its `deploy` no longer collides with its own
  `plan.lock`. Refusals: removing `npm` while its edge routes other apps, KyMessages without
  the edge's `egress_address`, and `frp` on a cluster's NPM. Upgrade impact: a KyMessages plan
  without `egress_address` on its edge is refused until it is set.
- Hostile-review fixes (council pass 3). The suite runner can install with a setup bundle and
  upgrade KyIdentity when `identity.mail` names a workstation password file: it keeps KyIdentity's
  copy. On Kubernetes, uninstall ends only once the app's pods are gone. A Docker deploy refuses
  a Compose project `kyq-<app>` deployed from another root. KyMessages' `matrix-init` no longer
  records the new version before the deploy pulls it. Listing snapshots with `restore APP` no
  longer ends a `stop`. `install` refuses a missing dependency with its own route (KyIdentity)
  up front, naming `plan`. pull-plan's refusal of another installation's plan gives steps that
  work. A MetalLB advertisement counts only for the pools it selects. Upgrade impact: a target
  whose root moved under running apps is refused until the root is put back or the apps are
  uninstalled from the old one.
- Hostile-review fixes (council pass 4). After handoff, `restore APP` lists snapshots (and takes
  back a recorded repository password) on a runner-owned target; `restore --snapshot`, and a
  listing that would resume an unfinished restore, stay refused and name the full
  `handoff --kyyard-target` command. KyMessages' `matrix-init` puts the deployed Compose files
  back after it runs, so a failed upgrade pull no longer leaves the new images for the next
  `start`. A Docker project run from another root is refused by apply even when the root was
  copied with its files, and by `backup`, `stop`, `start` and `restart`. On Kubernetes, a
  pre-upgrade copy whose stop is cancelled or times out starts the app again. pull-plan's
  refusal of another installation's plan now says to set that installation's id in the copy.
- KyPost and KyCalendar from the catalog pair for calendar invitations (#100). Their 0.6 releases
  pair through KyIdentity's suite directory on their own; the installer now sets
  `KYPOST_KYCALENDAR_ALLOW_PRIVATE` and `KYCALENDAR_KYPOST_ALLOW_PRIVATE` to `true` only when the
  plan routes the other app to a private address (an internal route, or `lan_dns`), and the
  handovers say how to check the pairing instead of "not configured". Upgrade impact: `apply`
  redeploys KyPost and KyCalendar once (their environment gained the option).
- An in-cluster NPM can take a LoadBalancer address from the cluster's Cilium LB IPAM or MetalLB
  (#99): `load_balancer_address` on the `npm` app makes `app-public` a LoadBalancer on 80 and 443
  at that address, so internal routes work on `client_port: 443` instead of `:30443`. Preflight
  refuses an address no announced pool holds, or another Service holds, and needs `list` on the
  pools and announcements; `handoff` grants the runner those `list` rules only for such a plan.
  Choose it before KyIdentity's setup: `client_port` cannot change afterwards
  ([edge guide](docs/edge.md#a-load-balancer-address-load_balancer_address)).
- `pull-plan --kyyard-target T` and `handoff --revoke --kyyard-target T` naming the target
  `handoff` recorded keep the recorded runner instead of falling back to the flag defaults (#159),
  which failed on a catalog KyYard (`0 running pods in namespace kyq-runner`). Another target than
  the recorded one is refused (`invalid_request`) unless `--runner-pod` or `--runner-container`
  names its runner, and `--runner-pod` now defaults to the catalog KyYard's pod on a Kubernetes
  target that holds it.
- The catalog moves every Ky app to its 0.6 line: KyCalendar, KyDNS, KyDrive, KyIdentity,
  KyMessage, KyNotes, KyPost, kyPulse, KyRecovery and KyYard 0.6.0, the suite redesign release.
  Each pin is the digest its release workflow attested. No app's 0.6.0 changes a migration (its
  migration files are the same git objects as at 0.5.0, and its release notes name none), so the
  seven apps that migrate data keep `data_since: 0.5.0`. KyCalendar's scheduling stays on its
  default, automatic: on when KyPost is paired with it through KyIdentity.

Upgrade impact: an upgrade from 0.5.0 or later to 0.6.0 takes no pre-upgrade copy or snapshot and
needs `--acknowledge-no-backup`; from 0.4.x the seven apps that migrate data are backed up first,
as before. KyPost devices that pair by QR code or pairing link now wait for approval on the page
that showed the code, and KyPost apps from before device approval fail to pair until updated.

- The catalog KyPost can be backed up (#151). It kept its data in named Docker volumes owned by
  the image's uid 1000, so `kyquickstart backup kypost` refused (`backup_unsupported`) and every
  KyPost upgrade needed `--acknowledge-no-backup`. It now runs as the target user, like KyDrive
  and KyDNS, on `./config`, `./private`, `./state`, `./logs` and `./ollama-models` bind
  directories, and a cold backup takes `config`, `private` and `state`. A manifest's new
  `from_volumes` drives a one-time copy: `kypost.volumes`, before `kypost.deploy`, copies each
  earlier volume into its empty bind directory with the app stopped (read-only, offline,
  read with only `CAP_DAC_READ_SEARCH`, written as the target user) and keeps the volume.

Upgrade impact for KyPost: the first `apply` stops KyPost, copies its volumes (the Ollama model
is about 2.9 GB) and restarts it on the copies; once it runs, remove the volumes the step names
with `docker volume rm`. `backup kypost` refuses until that apply. Handover commands drop
`--user kypost`. A native-mail receiver's `tls_dir` key must now be owned by the target user,
not uid 1000: `chown` it before applying, or preflight refuses.

- The handover no longer says that no catalog product can pair with KyRecovery (#93): with an
  internal KyRecovery each product pairs by hand after the ceremony, as KyRecovery's step says.
  A plan without one says it routes no KyRecovery, and KyDNS, whose private-recovery option stays
  off, says it does not pair. CI now proves the pairing on kind: kyPulse, paired through its
  backup API with a code minted by the handover's command, deposits to the catalog's internal
  KyRecovery at its private address, and KyRecovery lists the backup.
- A catalog move that changes no migration no longer forces a pre-upgrade backup (#150). A
  manifest's new `upgrade.data_since` names the oldest version whose data the pinned version
  uses unchanged; upgrading from it or later takes no copy and asks for `--acknowledge-no-backup`
  like any upgrade without one. `scripts/catalog-follow` keeps it: unchanged when the
  product's migration files or directories (its `sources` entry) hold the same git objects at
  the new tag as at the pinned one, moved to the new version when they differ or cannot be
  read. KyCalendar, KyDNS, KyDrive, KyNotes, kyPulse, KyRecovery and KyYard start at
  `data_since: 0.5.0`: none of their 0.5.0.x releases changes a migration.
- On Kubernetes the pre-upgrade copy first proves the storage can provide the new claims, in a
  pod that mounts them while the app still runs (#149). Storage that cannot fails the step
  with the cluster's warnings and the earlier copies taking space, and the app is not stopped.
  `kyquickstart prune-copies APP` lists an app's copies and, after a typed confirmation, deletes
  those from versions older than the installed one; it never touches the app's own claims.

Upgrade impact: an upgrade from 0.5.0 or later to the 0.5.0.x pins of those seven apps takes no
copy or snapshot and needs `--acknowledge-no-backup`; from 0.4.x it is backed up as before.
`prune-copies` and the storage check's details need `list`/`delete` on
`persistentvolumeclaims` and `list` on `events`, which preflight does not require; grant them
to use them. A line move (such as 0.5 to 0.6) is a human PR that sets each app's
`data_since` from its product's release notes.

- `upgrade`, and `apply`/`install` over a kept deployment, take a verified `kyquickstart backup`
  snapshot before a version that migrates data, then upgrade with no acknowledgement. The
  snapshot ID is recorded in `results/<app>.pre-upgrade-backup.json`; a failed snapshot
  leaves the old version running. On Kubernetes the backup is a copy of each claim into
  `pre-upgrade-<from>-vol-<name>`, compared with the original before it is marked verified;
  `restore` does not read these copies yet. Docker targets without a backup destination, named
  volumes and Kubernetes apps that run as root stay `backup_required`.
- `backup` names a repository password it created even when that run's snapshot failed.
  Before, the next run found the password and never pointed at it.
- The catalog moves every Ky app to its 0.5 line: KyCalendar, KyDNS, KyDrive, KyIdentity,
  KyMessage, KyNotes, kyPulse, KyRecovery and KyYard 0.5.0, KyPost 0.5.0.1. Each pin is the
  digest its release workflow attested.

Upgrade impact: an existing install whose app migrates data (KyCalendar, KyDNS, KyDrive, KyNotes,
kyPulse, KyRecovery, KyYard) is backed up first: a snapshot on a Docker target with a backup
destination, a copy of its claims on Kubernetes; it is refused (`backup_required`) elsewhere; KyIdentity, KyMessage and KyPost
upgrade with an acknowledgement. Apps with back-channel logout sign out single sign-on sessions
once. New KyPost device enrollments need KyPost for Android 0.5.1.1 or later.

- KyIdentity's handover and the guides no longer say KyAuth push approval does not deliver:
  KyIdentity 0.4.8 and later, which the catalog pins, use the Busnes.app push relays by
  default (#98).

Upgrade impact: none; text only.

- Refuse undersized SSH host/authentication keys, obsolete key exchange and 96-bit MACs.
  RSA authentication uses SHA-2 even through the SSH agent and user certificates.

Upgrade impact: legacy SSH targets/agents may need a key or software update before the
installer or suite runner can connect. Keep existing access while rotating keys and verify
the new host fingerprint before updating a pin; see [rotation guidance](SECURITY.md#ssh-policy-and-key-rotation).
No installer state, target data, key or credential is reset automatically.

- An in-cluster NPM can keep one edge address when it changes node: `kube_vip_address`
  publishes `app-public` as a kube-vip LoadBalancer whose address follows NPM's pod and is
  also where its requests leave the cluster ([edge guide](docs/edge.md#an-address-that-follows-npm-kube_vip_address)).

Upgrade impact: none unless set. It needs kube-vip in services mode on the cluster, and the
edge's `egress_address` must equal the address; then point connectors and LAN DNS at it.

- With an internal KyRecovery, `apply` can pair KyPost with it (`kypost.recovery-pair`, #93
  stage 2). It pairs only to the recovery key the owner confirms after the ceremony, set as
  `recovery_key_id` on the plan's `kyrecovery` app, and only with a KyRecovery that reports
  its key (`status --json`). It then makes KyPost's first deposit and checks KyRecovery holds
  it. The pairing code stays in memory. KyRecovery 0.4.3 cannot report its key, so for now the
  step only says to pair by hand.

Upgrade impact: apply runs one new read-only step with KyPost and an internal KyRecovery;
nothing is paired until `recovery_key_id` is set and KyRecovery has a release with `status`.
A suite runner older than this release refuses a plan with `recovery_key_id`: hand off the
new runner first.

## v0.4.2.7 — 2026-10-26

- Kubernetes network rules no longer include the IPv4-mapped exception that the API
  server refuses, allowing those plans to reach deployment rather than fail validation.
- Routes served on an HTTPS port other than 443 retain that port in their Host header.
  Review such routes and the URLs they advertise before reapplying an older plan.

Upgrade impact: relevant to Kubernetes applies and edges using a nonstandard client
port. This is an installer correction, not evidence of a completed production rollout.
[Published release](https://github.com/Busnes-app/KyQuickStart/releases/tag/v0.4.2.7)
([source](https://github.com/Busnes-app/KyQuickStart/tree/v0.4.2.7)).

## v0.4.2.6 — 2026-10-26

- Corrects the Kubernetes editor network path so Euro-Office can reach KyDrive through
  a tunnel edge, while retaining the editor's egress restrictions.

Upgrade impact: review installations combining the optional editor with a tunnel edge.
This does not qualify browser editing, backups or the editor's remaining provenance
and acceptance gates.
[Published release](https://github.com/Busnes-app/KyQuickStart/releases/tag/v0.4.2.6)
([source](https://github.com/Busnes-app/KyQuickStart/tree/v0.4.2.6)).

## v0.4.2.5 — 2026-10-25

- Supplies KyYard's agent image from its own pinned image when rendering the catalog
  workload, enabling agent enrolment without borrowing the suite runner's identity.

Upgrade impact: relevant to installer-managed KyYard deployments. Keep using a verified
release binary for the suite runner; a source build still has no released runner pin.
[Published release](https://github.com/Busnes-app/KyQuickStart/releases/tag/v0.4.2.5)
([source](https://github.com/Busnes-app/KyQuickStart/tree/v0.4.2.5)).

## v0.4.2.4 — 2026-10-25

- Sets the catalog products' private-recovery options when KyRecovery has an internal
  route, with the documented KyDNS exception, so an operator can pair supported products
  with that internal recovery service.
- Corrects the redaction fuzz model to check every byte rather than every rune.

Upgrade impact: recovery still needs the custodian ceremony and operator pairing; this
release does not automatically pair products or prove that backups were deposited.
Review private recovery routing and each product's supported policy before applying.
[Published release](https://github.com/Busnes-app/KyQuickStart/releases/tag/v0.4.2.4)
([source](https://github.com/Busnes-app/KyQuickStart/tree/v0.4.2.4)).

## v0.4.2.3 — 2026-10-25

- Advances the catalog KyIdentity pin from 0.4.7 to 0.4.8.1.
- Holds kyPulse at 0.4.3, replacing the 0.4.3.1 catalog pin in v0.4.2.2.

Upgrade impact: inspect the product versions already installed before applying this
catalog; do not assume a newer installer always selects a newer product image. Existing
product upgrade/downgrade and data-migration decisions remain explicit.
[Published release](https://github.com/Busnes-app/KyQuickStart/releases/tag/v0.4.2.3)
([source](https://github.com/Busnes-app/KyQuickStart/tree/v0.4.2.3)).

## v0.4.2.2 — 2026-10-25

- Docker catalog renderings trust forwarded headers only from the configured edge's
  exact address, using a /32 or /128 where the product takes a CIDR. A missing address
  leaves CIDR-taking products untrusted; products requiring a bare address still refuse it.
- Kubernetes private-volume initialization removes group/other access on every start
  and uses `OnRootMismatch` for fsGroup on non-root workloads. The KyYard runner's private state directory is
  included, preventing permissive remounts from making retained keys unusable.
- kyPulse's generated targets include Docker-only products through their status paths,
  rather than requiring every monitored product to have a Kubernetes readiness path.
- External routes can use the external-domain apex, needed by KyMessages. Plan review
  reports the route's client port, planned NPM installation and requested certificates;
  SMTP password-file checks run before connection, while review can precede file creation.
- Catalog pins move KyDNS and kyPulse to 0.4.3.1. Handover text distinguishes unfinished
  recovery pairing, KyAuth push-relay setup and KyPost/KyCalendar invitation integration.

Upgrade impact: review the edge address and rendered proxy trust before reapplying Docker
plans. A Kubernetes reapply can tighten permissions inside managed private volumes; keep
product data and secrets in place. This release does not configure automatic recovery
pairing or a KyAuth push relay. The original release body contained only a compare URL;
this summary is reconstructed from the two tagged source trees, not the commit log.
[Published release](https://github.com/Busnes-app/KyQuickStart/releases/tag/v0.4.2.2)
([source](https://github.com/Busnes-app/KyQuickStart/tree/v0.4.2.2)).

## v0.4.2.1 — 2026-10-25

- Redaction covers overlapping secrets without leaving a fragment in command output.
- Adds opt-in native KyPost mail with the Maddy receiving profile, alongside the
  existing webmail path.
- Adds repeatable release-build tooling, release-verification guidance, the coding
  standard and assurance case; CI follows the Ky apps through reviewed pull requests.

Upgrade impact: inspect the explicit native-mail selection before applying; it is not
an automatic conversion of an existing KyPost instance. Receiving remains subject to
KyPost's test-domain and production-MX gates, and Kubernetes backup/restore remains
unfinished. Repeatable-build tooling is not a claim that every published binary was
independently rebuilt here.
[Published release](https://github.com/Busnes-app/KyQuickStart/releases/tag/v0.4.2.1)
([source](https://github.com/Busnes-app/KyQuickStart/tree/v0.4.2.1)).

## v0.4.2 — 2026-10-24

- Uses Go 1.26.10 for the then-published standard-library security fixes.
- Adds daily four-part maintenance releases and follows verified Ky product image pins
  on their current major/minor lines before those releases.
- Resets Drive access before installer restore startup, refreshes the Ky catalog pins,
  and adds fuzzing for plan parsing, redaction and runner requests.

Upgrade impact: review the refreshed product pins and existing restore procedure before
applying or restoring. A daily release still pairs one installer version with one
verified runner image; it does not silently deploy the newer catalog to an existing stack.
[Published release](https://github.com/Busnes-app/KyQuickStart/releases/tag/v0.4.2)
([source](https://github.com/Busnes-app/KyQuickStart/tree/v0.4.2)).

## v0.4.1 — 2026-10-23

- Provides the first release binaries after v0.4.0's binary-job self-check failed.
- Adds the optional Euro-Office/KyDrive Kubernetes editor path, the Docker
  cloudflare-direct edge, and explicit internal/external routes.
- Corrects issues found in the lab and makes `verify-edge` check every route; updates
  kubectl, fish-shell and DNS operating guidance.

Upgrade impact: use this or a later suitable release instead of expecting v0.4.0
binaries. Review route exposure and placement limits before using the new edge/editor
paths; the unfinished acceptance gates remain separate from the installer build.
[Published release](https://github.com/Busnes-app/KyQuickStart/releases/tag/v0.4.1)
([source](https://github.com/Busnes-app/KyQuickStart/tree/v0.4.1)).

## v0.4.0 — 2026-10-22

First published KyQuickStart release: the deterministic execution layer for
KyQuickStart-AI and an unattended CLI for the Ky catalog, Docker hosts over SSH and
Kubernetes. It includes planning, configuration, lifecycle commands and Docker
backup/restore, plus a versioned suite runner image for KyYard.

Upgrade impact and availability: **no release binaries were uploaded** because the
binary job failed its workflow self-check. The runner image was published and attested;
use v0.4.1 or a later suitable installer release. Kubernetes backup/restore and the
acceptance gaps documented at that tag were unfinished. The macOS binaries of later
releases are not code-signed or notarized.
[Published release](https://github.com/Busnes-app/KyQuickStart/releases/tag/v0.4.0)
([source](https://github.com/Busnes-app/KyQuickStart/tree/v0.4.0)).

## Maintaining these notes

For every intended release, including maintenance releases, provide the major changes
and upgrade impact in its release body; this archive backfills the versions above. Generated PR summaries may supply
that body when useful; raw git logs and a compare URL alone are insufficient. Name
changed dependencies or catalog pins when they affect the update decision. Preserve
known limits and failed/missing artifacts; do not infer deployment qualification.

`make notes-check` exercises the publication guard. The daily publisher generates and
checks the notes before creating a release; the artifact workflow checks the published
body before publishing an image or binaries. A missing summary stops the workflow.
Supply authored notes in a manually published release when generation finds none;
normal tag/version and exact-commit CI checks still apply. This guard catches missing
summaries, not factual errors: maintainers must review accuracy and upgrade impact.
