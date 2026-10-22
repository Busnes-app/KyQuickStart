# Redirect: KyQuickStart as the execution layer for KyQuickStart-AI

This is the authoritative record of the 2026-10-22 redirect. Where another document disagrees,
this one wins.

## Decision (owner, 2026-10-22)

- [KyQuickStart-AI](https://github.com/Busnes-app/KyQuickStart-AI) is the primary installation
  path. The AI plans, converses with the owner and diagnoses failures.
- KyQuickStart is its deterministic execution layer. It enforces validated operations,
  ownership, credentials and verifiable results, and it stays usable unattended from the
  command line (`apply` on a saved `stack.yaml`).
- Docker hosts over SSH (Compose) and Kubernetes clusters are both fully supported targets.
  Products keep their documented placement restrictions (below).
- Expansion is frozen: no new catalog apps (Ky or third-party) and no new standalone
  orchestration features. Work is limited to compatibility, safety fixes, verification and the
  AI integration. This supersedes the earlier all-product adapter and roadmap plans.
- Amendments (owner, 2026-10-22): KyDrive offers Euro-Office on a cluster (`euro-office`, the one
  catalog addition under the freeze), and a `cloudflare-direct` edge routes a Cloudflare Tunnel
  straight to eligible apps without NPM ([edge](edge.md)).

## What KyQuickStart does now

Plan, review, preflight and apply the catalog's ten Ky products and Nginx Proxy Manager on
Docker hosts over SSH and on Kubernetes. It reconciles the NPM edge (`lan`, frp or Cloudflare),
runs each product's native `apply-setup` with KyIdentity setup, amendments and SCIM, and prints
the owner's handover. After install it manages apps one at a time, backs up and restores Docker
apps with restic, and hands the plan to KyYard's suite runner. Saved `stack.yaml` files and
target markers from earlier builds stay valid.

## Dispositions

**Keep**

| Area | Evidence | Callers |
| --- | --- | --- |
| Catalog: 10 Ky products + `npm` (`internal/catalog/apps/`) | CI e2e at 01799b2 (run 39680983040) installs each | AI, CLI, runner |
| Docker (`dockerhost`) and Kubernetes (`kube`) drivers and their safety controls (locks, plan markers, pinned host keys, restricted pods) | `make e2e` `TestApply`, `TestKubernetesApply`, `TestDockerKyCatalog` | AI, CLI, runner |
| Edge: NPM, `lan`, frp, cloudflared | `TestKubernetesNPMEdge`; `internal/edge` container tests | AI, CLI |
| `stack`, `engine`, `remote` and `suite` error codes | Every command depends on them | AI reads codes |
| `plan`, `review`, `preflight`, `apply`, `install`, `uninstall`, `unmanage`, `upgrade`, `status`, `verify-edge` | [guided setup](guided-setup.md) | AI, CLI |
| Docker `backup`/`restore` (restic) | `TestApply`, `TestKubernetesRestore` | AI, CLI |
| Runner, `handoff`, `pull-plan` | KyYard v0.4.3 ships the client; K80 runner pod ([pilot](pilot/README.md)) | KyYard |
| `integrate-identity`, `integrate-product`, `integrate-stack` | The only path for already-running products | AI, CLI |
| KyIdentity setup, amendments, SCIM | Pilot runs 2026-10-18; e2e | apply |
| `release-image.yml` | Release binaries and runner image | release |
| Guided interactive `plan` | Unattended CLI users | CLI |

**Simplify.** Ongoing control is a set of thin verbs on the existing drivers; see
[Lifecycle commands](#lifecycle-commands).

**Retire** (branch `redirect/retire`, commit 29182ec)

| Item | Evidence | Callers |
| --- | --- | --- |
| Offboarding bridge: `cmd/kyoffboard`, `internal/offboard`, `kyoffboard-image.yml` and its CI job | No importers outside it; the workflow never ran; no tags or releases; no pilot use. Whether a ghcr package exists is unproven (the token lacks `read:packages`) | None. Offboarding goes through KyIdentity (SCIM or suite webhook) and each product's directory handling |
| Third-party adapters `internal/adapters` (BookStack, Forgejo, Immich, LibreChat) and the Forgejo e2e make block | No catalog entry uses them | None |
| Immich `integrate-product` adapter | Needs a patched Immich, and only stock third-party apps may ship | `integrate-product --product immich` and `integrate-stack` selections naming Immich are now refused as unsupported |
| Vaultwarden, Forgejo and BookStack as installable apps | Dropped by the owner, 2026-10-22; their admission notes stay in the catalog spec as history | KyQuickStart-AI no longer offers them; existing installations are untouched |
| BookStack catalog entry (draft PR #67, branch `factory/010-bookstack-catalog`) | Closed unmerged by the owner: a catalog addition that needed the bridge | None; KyQuickStart-AI can still test-install BookStack |
| Roadmap and expansion plans in `docs/superpowers/plans/` (roadmap, phase 1, phase 2, phase 4 edge, all-product adapters, adapters handoff, deployment plan); `docs/offboarding.md`; the 2026-10-19 setup-gap report | Superseded by this record; git history keeps them | Docs only |

**Preserve temporarily**

- The admission rules in the
  [third-party catalog spec](superpowers/specs/2026-10-06-third-party-catalog-design.md#admission-rules).
  KyQuickStart-AI uses them for labelled test installs.
- Branches `factory/004` and `factory/007` are untouched.

**Unfinished** (the requirement stays; the work is not done)

- Kubernetes backup and restore (`backup`/`restore` cover Docker apps only).
- Trusted-proxy settings per product (see the edge table).
- A Docker-only edge. There is no fresh NPM on Docker, and a Docker NPM behind `docker-proxy`
  sees the gateway ([edge](edge.md#lan-clients-provider-lan)).
- Other edge provisioning: DNS-01 on a Docker edge or through a provider other than Cloudflare,
  creating Cloudflare tunnels and public DNS records, provisioning frps, remotely managed
  Cloudflare token tunnels, and isolating a connector on Kubernetes.
- The KyDNS edge on Docker hosts that hide client addresses.
- WebSocket upgrade and LiveKit media through an edge.
- CalDAV clients.
- Euro-Office: browser editing through the edge, KyDrive-to-editor commands, backup and
  upgrade, and its provenance gate: the image's build provenance is observed, not verified
  ([guided setup](guided-setup.md)).
- `cloudflare-direct` on Kubernetes; KyNotes behind it (a two-phase connector); an explicit
  per-route opt-in or Cloudflare Access for admin-only apps; a live tunnel through every
  eligible product.
- A KyPost setting for private mail hosts (`SANDBOX_PRIVATE_HOSTS`).

## Compatibility consequences

- The `kyoffboard` binary and image are gone. Nothing released depended on them.
- `integrate-product --product immich` and `integrate-stack` selections naming Immich are
  refused as unsupported. Configure those instances by hand.
- A saved `stack.yaml` and target markers keep working; catalog versions are unchanged.
- `stack.yaml` gains an optional `stopped` field, written by `kyquickstart stop`. Builds older
  than this redirect refuse a plan that carries it, so `handoff` refuses while an app is stopped.
- New error code `app_stopped` (HTTP 422 from the runner).
- Remote SSH negotiates the pinned host-key type, or ed25519 first on first contact, so a
  fingerprint from `ssh-keyscan -t ed25519` matches on servers that also hold ECDSA keys.
- Third-party apps are installed only by KyQuickStart-AI, as labelled test installs outside the
  catalog. Vaultwarden, Forgejo and BookStack are no longer offered, so the installer offers no
  password manager.

## Placement

| Product | Docker | Kubernetes |
| --- | --- | --- |
| KyIdentity, KyRecovery, kyPulse, KyNotes, KyCalendar, KyYard | Yes | Yes |
| KyDrive, KyPost, KyMessages | Yes | Refused |
| KyDNS | Yes; its route must be on the catalog NPM on a cluster | Refused |
| NPM (`npm`) | Refused (use an existing NPM) | Yes |
| Euro-Office (`euro-office`, KyDrive's editor) | Refused | Yes (baseline) |

## Edge compatibility

Classes:

- **Verified**: demonstrated through a supported edge, with the cited evidence.
- **Fixable**: there is a concrete, bounded fix.
- **Removal candidate**: test evidence shows the product cannot work through the edge.
- **Unverified**: not demonstrated either way.

There are **no removal candidates**: no test shows an incompatibility.

- **Tested edges.** Only the `lan` edge with the catalog NPM on Kubernetes has been tested with
  products: `TestKubernetesNPMEdge`, `TestKubernetesKyYard` and `TestKubernetesKyMessage` in CI
  run 39680983040, plus the [K80 pilot](pilot/README.md).
- **cloudflare-direct** (Docker only) is tested offline only (`TestCloudflaredDirectContainer`)
  and is Unverified for every product. Eligible: KyIdentity, KyCalendar, KyPost, KyDrive.
  Refused: KyNotes, KyMessages, KyDNS, KyRecovery, kyPulse, KyYard ([edge](edge.md)).
- **frp and Cloudflare** have been tested with the demo app only ([edge](edge.md#test-scope)).
  For every product they are Unverified.
- **Forwarded headers.** No product passes `verify-edge`. Each either ignores forwarded headers
  by design or sees a Docker gateway instead of the edge.
- **Trust fixes.**
  - On Docker: trust `{{edge_address}}` where the KyDNS-style gate proves it.
  - On Kubernetes: trust the pod network. A NetworkPolicy already admits only the edge's pods,
    but whether the cluster's network plugin enforces it is unproven.

| Product | lan edge (sign-in through it) | Forwarded headers | Protocols and boundaries | Class |
| --- | --- | --- | --- | --- |
| KyIdentity | Verified: CI and pilot 2026-10-18; the issuer carries `:30543` | Unset: the NPM pod is every client | Admin API refused to the everyday account (pilot) | Verified; trust Fixable |
| KyRecovery | Verified: CI and pilot (role admin, everyday `access_denied`) | Unset | Ceremony at `/admin/ceremony` on the public route | Verified; trust Fixable |
| kyPulse | Verified: CI and pilot; targets `ok` | Unset | Probes need the pod network in `allow_cidrs` | Verified; trust Fixable |
| KyYard | Verified on Kubernetes (`TestKubernetesKyYard` asserts untrusted) | Loopback only on Kubernetes; `{{edge_address}}` on Docker | Docker through an edge is untested | Verified (Kubernetes); Unverified (Docker) |
| KyNotes | Verified: CI `notesSignOn` | Loopback default | – | Verified |
| KyCalendar | Verified: CI `calendarSignOn` | Unset | CalDAV clients untested | Verified; CalDAV Unverified |
| KyPost | Verified: CI `postSignOn` (asserts untrusted) | Empty | No mail listener; IMAP/SMTP outbound only; private mail hosts refused | Verified; mail-host setting Fixable |
| KyDrive | Verified: CI and pilot (hlwcdocker02 behind K80; SCIM through the edge) | Unset. WebDAV behind a proxy needs it (KyDrive README) | The published port is reachable on the LAN; firewall rule open | Verified; trust Fixable; Euro-Office `/healthcheck` and signed callback verified (`TestKubernetesEuroOffice`), editing Unverified |
| KyMessages | Verified: CI (six routes, path blocks, MAS sign-in, encrypted sync, LiveKit `/rtc/validate` over HTTP) | `{{edge_address}}`; untrusted behind the CI Docker NAT | WebSocket upgrade enabled (`internal/edge/npm.go`) but never exercised; media 7881/tcp and 7882/udp go straight to `media_address`, no TURN | Verified (HTTP); WebSocket and media Unverified |
| KyDNS | Fails closed in CI: KyDNS sees gateway 172.22.0.1, not the edge 172.18.0.2 | `egress_address` only; a gateway value is refused by `kydns.gate` | DNS over UDP and TCP 53 verified on the forward address; direct requests get 403 | Unverified (edge) |

**KyDNS behaviour.** On a Docker host that hides client addresses, KyDNS refuses the edge, and
the `kydns.edge` step fails apply. On such a host its `allow_query` also sees every client as
the gateway ([guided setup](guided-setup.md#catalog-kydns-on-docker)). The CI test is a
single-host setup where kind reaches the host's published port, so it says nothing about a
host that keeps client addresses. The deciding test is one pilot apply with KyDNS on a separate
Docker host behind the K80 NPM.

## Lifecycle commands

Thin verbs on the existing drivers ([guided setup](guided-setup.md#managing-apps)). `stop`,
`start` and `restart` take the target lock and refuse apps kyquickstart did not deploy; `stop`
and `start` change the plan like `install` (refused on a runner-owned target), `restart`
deploys like `upgrade`. `logs` and `status` take no lock and change nothing.

| Command | Docker | Kubernetes |
| --- | --- | --- |
| `stop APP` | `compose stop` | replicas 0 (suspend), waits for the pod to go |
| `start APP` | `compose up -d`, health wait | replicas 1 (resume), readiness wait |
| `restart APP` | `compose restart`, health wait | pod-template annotation, readiness wait |
| `logs APP [--tail N] [--service S]` | `compose logs` | pod logs; needs `get pods/log`, which preflight does not require |
| `status [--json]` | live `running`, `stopped`, `missing` or `unknown` | same |

`stop` records `stopped: true` for the app in `stack.yaml`. While it is set, `apply`,
`upgrade`, `restart` and `backup` refuse `app_stopped` instead of starting the app again;
`start` and a finished `restore` clear it. `logs` redacts the secrets the app's catalog entry
generates; installer-written credentials outside that list (a restic password, a transferred
client secret) and anything a product logs itself are printed as they are.

## Verification of the redirect (2026-10-22)

- **Execution layer:** `make ci` and `make e2e` on this branch. `TestApply` (SSH) and
  `TestKubernetesApply` (kind) run stop, status, the `app_stopped` refusal, start, restart,
  redacted logs and uninstall, with an unrelated workload that must survive. Product sign-in
  through the `lan` edge, the admin/everyday split, upgrades and Docker restore stay covered by
  `TestKubernetesNPMEdge`, `TestKubernetesRestore` and `TestKubernetesKyMessage`.
- **AI-led path:** one real KyQuickStart-AI session (Claude Code, non-interactive, hands-off
  rules passed as flags) through `kyqs-ai` against a disposable Debian sshd host (ECDSA,
  ED25519 and RSA host keys) and a kind cluster, with the demo app `hello` on each. It ran
  review, preflight with the launcher's pinned key, apply, rerun (all skipped), status, logs,
  stop, the refused apply, start, restart and uninstall on both targets: 215 s, 31 kyquickstart
  calls, no user interventions, no manual repairs. Afterwards, checked independently: data and
  secrets kept (the Docker app directory; the Kubernetes PVC and Secret), the unrelated
  container and Deployment still running, no generated secret anywhere in the run folder or
  transcript. An apply interrupted mid-way (Docker `compose up`, Kubernetes health wait)
  resumed on rerun with the earlier steps skipped and the token unchanged.
- **Not demonstrated by the AI-led path:** a real product with sign-in through an edge (it
  needs a disposable DNS and certificate fixture that exists only inside the Go e2e harness),
  backup and restore (the demo app has no backup block), and anything on Kubernetes backup,
  which is unfinished.
