# Setup gap validation — networking, KyYard, KyDNS and catalog

Source review on 2026-10-19 against fetched upstream worktrees. This report records
the baseline before fixes; links pin that baseline so later changes do not invalidate
the evidence. No builds, tests or live deployment were run by this research task.

## Decisions supplied by the owner

- KyPost owns `kypost-net`; other participating products join it.
- Support containerized Nginx Proxy Manager with a pinned proxy address.
- Expand Docker support to every catalog Ky product, including KyYard's resident runner.
- Support production Euro-Office on Docker as well as Kubernetes. Euro-Office's
  implementation and provenance review are outside this report.

## Findings and smallest fixes

### Shared network: one code owner already, conflicting operating instructions

KyPost defines the named network with IPAM and no `external: true`; it is the
Compose owner. KyIdentity and KyNotes already declare the same network external.
There is no remaining three-owner Compose conflict in these reviewed sources.
However, KyPost's documentation says external applies to the proxy only, then
describes creating an unlabeled network manually and optionally overriding KyPost
to external. KyIdentity also documents manual creation followed by a KyPost
external override. These instructions allow different owners and depend on
Compose-version-specific handling of unowned network labels.

Smallest fix: keep KyPost's network definition and the other products' external
definitions, explicitly name KyPost as owner in their setup docs, and replace
adoption/label-workaround instructions with an owner-created-network startup path.
Do not remove or recreate an existing network automatically. A network-only
bootstrap for an Identity-first or Notes-only installation must preserve KyPost
ownership and reserve low pinned addresses from dynamic allocation; it needs an
explicit supported procedure rather than treating an external override as equivalent.

Sources:

- [KyPost Compose, network definition](https://github.com/Busnes-app/kypost-server/blob/de53d118d448b42a450a4e6c22e198ac1bfef968/docker-compose.yml#L250).
- [KyPost networking instructions](https://github.com/Busnes-app/kypost-server/blob/de53d118d448b42a450a4e6c22e198ac1bfef968/docs/Reverse_Proxy_Networking.md#L61).
- [KyIdentity external network](https://github.com/Busnes-app/KyIdentity-server/blob/0060ca2e9134a3555e911624982974bfd6786566/docker-compose.yml#L138) and [startup alternatives](https://github.com/Busnes-app/KyIdentity-server/blob/0060ca2e9134a3555e911624982974bfd6786566/README.md#L105).
- [KyNotes external network](https://github.com/Busnes-app/kynotes-server/blob/55743d61eb127cd65b13532f891ddaa4b6fd4d13/docker-compose.yml#L103).

### Container proxy: supported pattern exists; KyYard packaging remains manual

KyPost already documents a proxy on `kypost-net`, using container DNS and an exact
pinned trusted `/32`. KyYard already documents a working dedicated-network
container proxy pattern, but requires an operator-created Compose override.
Its base and compatibility overlay use Docker's default `bridge`; the latter
does not enable a custom proxy network. The README explicitly warns that the
manual pattern disconnects the PostgreSQL overlay and drops its loopback publish.
This is a packaging gap, not absence of trusted-proxy support in the server.

Smallest fix: ship a named container-proxy overlay with an explicit external
network, retain exact peer trust and WebSocket forwarding, and document compatible
PostgreSQL/loopback behavior. Validate merged Compose variants rather than
silently resetting the database's network. Do not trust an entire shared subnet
or a recycled dynamic address. Standalone container-proxy support is distinct
from KyQuickStart deploying a fresh NPM: its current catalog NPM is Kubernetes-only.

Sources:

- [KyPost pinned proxy](https://github.com/Busnes-app/kypost-server/blob/de53d118d448b42a450a4e6c22e198ac1bfef968/docs/Reverse_Proxy_Networking.md#L61).
- [KyYard existing manual container pattern and limitations](https://github.com/Busnes-app/KyYard-Server/blob/0d2ab5438f55e8792af968ed069dc33792e75292/README.md#L149).
- [KyYard compatibility overlay](https://github.com/Busnes-app/KyYard-Server/blob/0d2ab5438f55e8792af968ed069dc33792e75292/docker-compose.proxy-network.yml).
- [KyQuickStart supported edge paths](https://github.com/Busnes-app/KyQuickStart/blob/c25e426269536701a09a16936b6227358bd1570c/docs/edge.md#L9).

### KyYard role: reported missing server field is disproved

`GET /api/auth/me` serializes its authenticated `store.User` as `user`. That type
already exports `Role` as JSON `role`. The frontend reads `a.user` and passes
`user?.role` to Settings. No runtime field fix is justified against this baseline.
The existing login/`me` test checks username but does not directly assert the role
in an untyped wire response; adding that assertion would protect the actual contract.

Sources:

- [Authenticated response](https://github.com/Busnes-app/KyYard-Server/blob/0d2ab5438f55e8792af968ed069dc33792e75292/internal/api/auth_handlers.go#L292).
- [JSON role field](https://github.com/Busnes-app/KyYard-Server/blob/0d2ab5438f55e8792af968ed069dc33792e75292/internal/store/models.go#L10).
- [Frontend consumption](https://github.com/Busnes-app/KyYard-Server/blob/0d2ab5438f55e8792af968ed069dc33792e75292/web/src/App.tsx#L33).
- [Existing response test](https://github.com/Busnes-app/KyYard-Server/blob/0d2ab5438f55e8792af968ed069dc33792e75292/internal/api/api_test.go#L113).

### KyDNS contradiction: README intro disagrees with actual network creation

The Docker section first says KyDNS joins a pre-owned LAN network and Compose
never creates or deletes it. Later paragraphs, `.env.example`, Compose and
`setup-env.sh` describe creation with detected interface/subnet/gateway. The
actual Compose file has driver/IPAM settings and **no** `external: true`.
The script really does append missing parent/subnet/gateway keys. Therefore the
smallest documentation-only fix corrects the introductory ownership paragraph;
changing to an external-only network would be a separate behavior change.
The claims about adopting existing network labels should be bounded to the
Compose versions actually qualified rather than presented as universal behavior.

There is also a concrete command typo: `docs/INSTALLER_SETUP.md` says `admin reset`,
but the CLI accepts only `admin reset-password` and later documentation uses that
correct spelling. Correct the earlier occurrence.

Sources:

- [Contradictory README Docker section](https://github.com/Busnes-app/kydns-server/blob/7faa1480c9ba012fa633f25ed84b73300c388a64/README.md#L688).
- [Actual network definition](https://github.com/Busnes-app/kydns-server/blob/7faa1480c9ba012fa633f25ed84b73300c388a64/docker-compose.yml#L63).
- [Setup detection](https://github.com/Busnes-app/kydns-server/blob/7faa1480c9ba012fa633f25ed84b73300c388a64/scripts/setup-env.sh#L30).
- [Command typo](https://github.com/Busnes-app/kydns-server/blob/7faa1480c9ba012fa633f25ed84b73300c388a64/docs/INSTALLER_SETUP.md#L41) and [accepted CLI command](https://github.com/Busnes-app/kydns-server/blob/7faa1480c9ba012fa633f25ed84b73300c388a64/cmd/kydns/main.go#L82).

### Catalog: KyDNS exists; several other products remain Kubernetes-only

The production catalog already includes Docker Compose templates for KyDNS,
KyDrive and KyPost. KyIdentity, KyNotes, KyCalendar, KyRecovery, kyPulse, KyYard
and NPM have Kubernetes manifests and no Compose template. The loader intentionally
accepts this for non-third-party apps; the CLI refuses a Docker placement without
a Compose template. Tests explicitly assert nil Compose for these entries.
Consequently “KyDNS needs a per-app template” is stale, while broad Docker
catalog coverage remains incomplete.

Smallest fix for Docker coverage is product-specific Compose templates reusing
the pinned manifest images and existing setup paths, with appropriate writable
paths, process identity, exact proxy trust, backup mounts and resource bounds.
Adding files alone does not qualify setup, restore, or KyYard's resident runner:
`yardPlan` separately enforces Kubernetes placement. Preserve that gate until its
Docker runner lifecycle has an implementation and acceptance coverage.

Sources:

- [Existing KyDNS template](https://github.com/Busnes-app/KyQuickStart/blob/c25e426269536701a09a16936b6227358bd1570c/internal/catalog/apps/kydns/compose.yaml.tmpl).
- [Catalog loader's Kubernetes-only branch](https://github.com/Busnes-app/KyQuickStart/blob/c25e426269536701a09a16936b6227358bd1570c/internal/catalog/catalog.go#L236).
- [Docker placement refusal](https://github.com/Busnes-app/KyQuickStart/blob/c25e426269536701a09a16936b6227358bd1570c/internal/cli/cli.go#L357).
- [KyYard's separate runner placement gate](https://github.com/Busnes-app/KyQuickStart/blob/c25e426269536701a09a16936b6227358bd1570c/internal/cli/yard.go#L27).

## Verification for the implementation owner

These are proposed checks, not passing results:

- Render merged product Compose configurations for base, proxy, build and database
  overlay combinations. Inspect external-network ownership, fixed addresses and
  exact trusted peers; no live network deletion is required.
- KyYard: extend its existing auth/`me` wire assertion, then run the repository's
  required Go and web checks if executable code changes.
- KyDNS documentation-only changes: verify wording against Compose and the setup
  script; no new runtime tests are necessary for spelling/ownership prose.
- KyQuickStart template changes: exercise `internal/catalog` and affected CLI
  tests, including Docker selection, route substitution, backup mount validation
  and source-owned runtime permissions; then `make build`, `make ci` and relevant
  disposable e2e acceptance under the root contract's resource limits.

The root agent serializes all builds/tests and owns final DOX updates for the
selected durable deployment decisions. This research-only addition does not alter
runtime contracts, so no AGENTS hierarchy/index change is required here.

MySlop posting was attempted by the orchestrator and returned HTTP 401. This file
is evidence, not a substitute for the required shared-board handoff.

## Implemented disposition

The sections above preserve the investigated baseline. The `fix/setup-gap-fixes`
worktrees implement these outcomes; no production installation was changed.

| Issue | Disposition |
| --- | --- |
| Shared network ownership | KyPost retains ownership. Its `scripts/ensure-network.sh` bootstraps only the network, verifies matching ownership/subnet on rerun, and preserves foreign networks. Standalone consumers join externally. Installer deployments retain their existing per-app bridges and routed host ports. |
| Identity setup fields | Public/confidential client type and HTTPS back-channel logout URI are accepted, persisted and included in ownership/drift checks. Public clients generate no OAuth secret and cannot use confidential credential export. |
| Identity hardening | Private key-file reads, HTTPS-only private callbacks, guarded relay requests and new-bootstrap password enforcement are implemented. Offline rotation verifies and publishes a separate copy, retaining recovery-code verification and both forms of setup receipt. Existing accounts remain unchanged by owner decision. |
| Euro-Office | KyDrive ships a production Docker overlay and runbook alongside Kubernetes support. Both runtimes retain the production provenance and editing/recovery gates. |
| Container proxy | KyYard ships a fixed-address NPM overlay on KyPost's external network, retaining database DNS and verified TLS. Bare proxy addresses become exactly one IPv4 or IPv6 address. |
| KyYard role | The report was disproved: the API already serializes `role`. Its real response test now explicitly asserts the administrator role. |
| CLI refusal explanation | Shared `applysetup.Refusal` crosses the local socket with a bounded public-safe reason. Unclassified errors remain redacted. A library release and consumer dependency updates are still required for deployed products. |
| KyDNS contradiction | Introductory network-ownership prose now matches Compose's actual LAN-network creation; the reset command spelling is corrected. The LAN network is distinct from the suite bridge. |
| Catalog Docker coverage | Six additional Ky templates complete all nine catalog Ky products. KyYard has a separate pinned runner with private state/socket mounts and no Docker socket. Docker issuer checks, amendments, setup selection and restore replay reuse native product authority. |

Owning contracts were updated for the selected network owner, both editor runtimes,
all-product Docker support and new-bootstrap-only password replacement. KyDNS's
root contract and existing API role semantics are unchanged because those findings
needed documentation or regression coverage, not a runtime contract change.

## Final local verification — 2026-10-20

All builds and test suites ran serially under the workstation's documented resource
limits, using persistent caches and disk-backed scratch.

- KyQuickStart: `make build`, `make ci`, vulnerability scan, actionlint and offline
  zizmor passed. The complete `make e2e` passed in 1,737 seconds, including six real
  pinned Docker product images, the isolated Docker KyYard runner, SSH/native Identity
  setup, Kubernetes apply/NPM/restore/Yard, edge checks and real Forgejo offboarding.
  Routed fixtures proved owner/everyday separation and removal replay after restore.
- KyIdentity: build, vet and race-enabled package coverage passed, including fresh
  API, OAuth, sync and command runs after their changes. Rotation acceptance exercised
  two successive rotations, recovery-code redemption, retained TOTP/recovery token
  and setup receipt inspection after activation at the original path. All 189 web
  tests and the production build passed. A real disposable browser flow completed
  bootstrap password replacement and then reached the administrator API; desktop
  and phone captures are in `KyIdentity-server/UI-VERIFICATION.md`.
- ky-primitives: root build/vet/fresh race suite passed; the nested offsite module's
  build/vet/fresh race suite and vulnerability scans for both modules passed.
- KyYard: tidy/vet and all backend race packages passed; 554 frontend tests, build,
  audit and the live smoke check passed. The owned smoke container has explicit CPU,
  memory, swap and PID limits, with memory-limit readback verified. Proxy Compose
  rendering passed with and without PostgreSQL for IPv4 and IPv6.
- KyDrive: 76 frontend tests/build and all backend race packages passed, with Restic
  available for backup tests. Vet, module verification, built-process smoke and
  all three deployment-renderer tests passed. The separate real Euro-Office Compose
  check passed unsafe-JWT refusal, health, absence of the JWT from container env
  and persistence through container recreation.
- KyPost: the real Docker network regression and shell syntax/shellcheck passed:
  network-only creation, stable rerun and foreign-network preservation.
- KyDNS: the documentation corrections were checked against Compose, setup script
  and accepted CLI spelling. No runtime change was needed.

The first KyYard and KyDrive sweeps exposed test fixture socket paths longer than
Linux's 107-byte Unix-socket bound under the required scratch directory. Their
shared API fixtures now use compact private temporary directory names under that
same scratch root; the affected API suites were rerun successfully. Production
socket validation was not weakened. Other passing backend package results were
reused only while their code and dependencies remained unchanged.

DOX closeout updated the owning deployment, setup and operator contracts. The API
child contracts were intentionally unchanged for test-fixture-only edits; no new
runtime route or authorization contract was introduced by those fixture changes.
All changed repositories passed whitespace checks. Owned end-to-end containers
and the browser server were cleaned up; pre-existing fixtures were preserved.

Not qualified here: macOS CI, a live PostgreSQL/TLS deployment, production
Euro-Office source/build provenance, complete Drive editing/collaboration/recovery
acceptance, and live mail delivery. Library releases, consumer dependency updates
and newly attested product image pins remain shipping steps. These source changes
were not deployed to production.

The required MySlop handoff could not be posted: a final authenticated folders
request still returned HTTP 401 with the configured token.
