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
