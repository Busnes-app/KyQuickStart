# Guided setup test

The binary now provides plain terminal questions, saved answers, install/skip choices,
target selection, existing-installation references, and edge configuration. It is ready
to test the guided flow and deploy a disposable diagnostic app. The full production
suite is still unavailable: its production catalog and complete product integrations
have not shipped. Source adapters exist for the 10 active suite server products; use product
builds that include their setup interfaces. Recording an existing product does not complete integration.

## Start tomorrow

From the repository directory, run:

```sh
./kyquickstart plan --demo --state ./morning-test
```

Or run `./kyquickstart` without arguments in a terminal to start setup. Choose `yes`
when offered the demo catalog. No arguments outside a terminal print usage.

The wizard asks for:

1. Docker hosts over SSH, or Kubernetes targets through an existing kubeconfig.
2. An install/skip choice and placement for every available product.
3. Existing Ky products: product name, target and HTTPS origin.
   Outside demo mode, supported products also offer a separate setup choice (default no),
   container name and protected container-local bundle path; Kubernetes needs namespace/pod.
4. Optional existing NPM and Cloudflare/frp connection settings, or `lan` for clients that
   reach NPM directly (its allowed client prefixes and client port).
5. Review and confirmation of the plan.
6. `finish`, `check` or `deploy`. `finish` is always the default and changes no servers.

The first computer-name question accepts DNS names such as `K80.urlxl.us`, including
uppercase letters. It derives a safe installation label (`k80-urlxl-us`); Docker's SSH
address defaults to the name you entered. Kubernetes still connects through its kubeconfig.
A Kubernetes target may name a StorageClass for app volumes (`storage_class`); blank needs a
default class. Preflight checks the named class exists. A volume's class cannot change later:
apply refuses an existing volume of another class.

For the simplest first test: use one Docker test host, choose `install` for `hello`,
choose `none` for its public connection, and choose `finish`. You can record existing
Ky services without modifying them. The demo uses a separate target directory by default:
`/opt/kyquickstart-demo`. The SSH user needs permission to create it and use Docker.
Docker and its Compose plugin must already be installed; use SSH keys loaded in an agent.
Docker target preflight also checks a readable `/dev/urandom` and the standard private-file
and encoding tools used for target-local credential generation. Secrets are created once on
the Docker target, then read into memory for verification/redaction; they are never included
in generation command arguments or input, or saved in installation state. Failed random
reads/encoding do not publish a partial credential. Existing credentials are preserved.
The disposable diagnostic container is capped at 64 MiB, one CPU and 32 processes.
Kubernetes generates missing keys inside a temporary pod on the selected cluster. The
operator needs pod get/create/delete and pods/exec create alongside the normal Secret
permissions; preflight checks them. Plans show the required pinned helper image.

The helper uses [Docker's official BusyBox packaging](https://github.com/docker-library/busybox)
with the stable `1.37.0-musl` [official image](https://hub.docker.com/_/busybox), pinned to
linux/amd64 manifest `sha256:dc88b80842580654294472735332ec7c7789a4010ec9a68b69f9b88c19abede6`.
The Docker registry tag/index was resolved before pinning; the exact image and command are
exercised in a restricted disposable container and kind. Updates must resolve/review an
immutable official manifest and repeat those checks; no mutable tag is deployed.
The pod runs as uid/gid65532 with no service-account token, dropped capabilities, read-only
root filesystem, 32MiB/100m CPU limits, a 1MiB memory scratch volume and a 180-second lifetime.
No values enter its arguments or container logs; only bounded exec output enters workstation
memory. UID-bound deletion completes before publication. A failed generator or cleanup
publishes no new Secret. Resource-version conflicts preserve concurrent updates. Reruns reuse
existing values and never replace an empty/invalid existing key; inspection reports failure.
This supplies target-local randomness, not automatic product setup bundles or secret transfer.

Review or check the saved plan:

```sh
./kyquickstart review --state ./morning-test
./kyquickstart preflight --state ./morning-test
./kyquickstart apply --state ./morning-test
```

Preflight may ask you to compare the SSH host-key fingerprint with one obtained independently
from the target. Confirm only after that comparison. Changed pinned keys are refused.
Apply changes the selected targets; it never integrates or adopts recorded existing products.

The Docker demo listens only on the target's loopback port 18080. For a remote host, open
an SSH tunnel from your workstation (replace the login and server):

```sh
ssh -N -L 18080:127.0.0.1:18080 kyq@your-test-server
```

Then open `http://127.0.0.1:18080` in your browser. It displays request diagnostics; it has
no suite login or application data. The port must be free on the target and workstation.
For Kubernetes the demo is internal to namespace `kyq-hello`; forward its Deployment port
with your kubeconfig: `kubectl -n kyq-hello port-forward deployment/app 18080:8080`.

## Resume and editing

Answers are saved after each question in `plan-answers.json` (mode 0600). Re-run the same
`plan --state` command and press Enter to keep displayed answers or type replacements.
EOF/Ctrl-D and Ctrl-C leave the published plan intact. `stack.yaml` is published only after
validation and a `yes` confirmation. A declined edit changes saved answers, not the active plan.
The `plan.lock` file prevents two editors from publishing concurrently. If a process was killed
without cleanup, remove that file only after verifying no editor is running.

For unattended review: `./kyquickstart plan --noninteractive --state ./morning-test`.
Deployment commands also run unattended from `stack.yaml`; supply a verified host-key
fingerprint with `--trust-host-key` on first connection. Use a separate state directory
for demo and production installations. Plans containing multiple routes for one app can
be run but must currently be edited directly rather than through the wizard.

## Managing apps

After the first `apply`, these commands change one app at a time. Each takes `--state` and
`--trust-host-key`, holds the target lock and is safe to rerun after it was interrupted:
the plan is saved only after every step passed.

```sh
./kyquickstart install hello --target nas --state ./morning-test
./kyquickstart upgrade --state ./morning-test        # every installed app
./kyquickstart status --json --state ./morning-test
./kyquickstart unmanage hello --state ./morning-test
./kyquickstart uninstall hello --state ./morning-test
```

- `install` adds the app and any missing dependencies to the plan on that target.
- `uninstall` refuses while another installed app depends on it. It disables the app's NPM
  routes, removes its containers (`docker compose down`, never `-v`) or its Deployment and
  Service, and keeps the app directory, data volumes, claims and secrets on the target.
- `unmanage` removes `ky.managed-by` and `ky.release-set` and records the hand-over in
  `<state>/handover.md`. Docker cannot relabel a container, so existing containers are
  recreated once; on Kubernetes the pod-template change replaces the pods once. The installer
  then refuses to install over the app.
- `upgrade`, and `apply`/`install` over a kept deployment, compare the deployed version
  with the catalog: older than `min_from`, skipping a `no_skip` version, a downgrade or an
  unknown version is refused (`upgrade_refused`); a version that migrates data is refused
  until verified pre-upgrade backups exist (`backup_required`); any other upgrade needs
  `--acknowledge-no-backup` (`ack_required`), recorded in `results/<app>.upgrade.json`.
  Apps upgrade one at a time in dependency order and stop at the first failure.

`stack.yaml` carries a `revision`, raised on every plan write. Every locked run records it
on each target (`<root>/.plan`, or annotations on the `kyquickstart` namespace). A target
recording a newer revision is refused (`plan_stale`): another workstation or the suite
runner changed it, so copy its plan back with `kyquickstart pull-plan` first. Markers also
record the plan's `installation_id`; a target another plan manages is refused
(`owned_by_other_plan`) until `kyquickstart apply --take-over-target T` claims it for this plan. Deployments record their catalog version (`kyquickstart/version`);
Docker installs from earlier builds are recreated once by their next `apply` to record it.

## Hand-off to KyYard

After install, KyYard's Suite page can install, upgrade, uninstall and unmanage apps through the
suite runner: `kyquickstart serve` in its own container beside KyYard, reached only over a
private Unix socket. The runner holds its own SSH key and kubeconfigs; KyYard holds none.

1. Run the runner beside KyYard. On a Docker host use
   [`kyyard-runner.compose.yaml`](kyyard-runner.compose.yaml): the two share only the socket
   directory, both as uid 10001; the runner has its own state volume, no ports, a read-only
   root, no capabilities and `no-new-privileges`. Set `KY_SUITE_RUNNER_SOCKET` in KyYard.
   On a Kubernetes target use [`kyyard-runner.k8s.yaml`](kyyard-runner.k8s.yaml) after
   editing its image digest, storage class and NetworkPolicy addresses. For now it runs the
   runner alone in `kyq-runner`; KyYard joins that pod with its catalog entry.
2. KyYard's target must be in `stack.yaml`. On a Docker target its SSH user must be able to run
   `docker exec`; on a Kubernetes target the workstation's kubeconfig must allow list pods and
   create `pods/exec` in the runner's namespace, which `handoff` checks before changing
   anything. From the workstation:

   ```sh
   ./kyquickstart handoff --kyyard-target nas --state ./morning-test
   ./kyquickstart handoff --kyyard-target k80 --runner-pod kyq-runner/app=runner --state ./morning-test
   ```

   This adds a `restrict`ed runner key to each Docker target's `authorized_keys` (replacing
   any earlier runner's line, keeping the rest), gives each Kubernetes target a
   `kyquickstart-runner` ServiceAccount with exactly the installer's permissions, copies the
   plan, pinned host keys and step results into the runner, and records the runner as the
   plan's owner on every target. A rerun reconciles; `--runner-container` names a container
   other than `kyquickstart-runner`, and `--runner-pod NAMESPACE/LABEL_SELECTOR` must match
   exactly one Running pod with a container named `runner`. A runner pod reaches its own
   cluster at `kubernetes.default.svc`; every other Kubernetes API server at the address in
   the workstation's kubeconfig, so that address must work from where the runner runs.
3. From now on the CLI refuses `plan`, `install`, `uninstall` and `unmanage`
   (`owned_by_runner`): use the Suite page. `apply`, `upgrade`, `status` and `integrate-*`
   still run from the runner's plan; copy it first:

   ```sh
   ./kyquickstart pull-plan --state ./morning-test   # the runner handoff used; keeps stack.yaml.bak
   ```

To take the plan back, pull it, then run `./kyquickstart handoff --revoke --state
./morning-test`. That removes the runner's key lines, ServiceAccount and token and records the
CLI as owner; the runner can no longer reach any target.

## Existing installations

The `existing` section records where each Ky product lives. It is separate from `apps`,
which selects new workloads to deploy. A product cannot be listed in both sections.
Existing references do not satisfy catalog dependencies or grant authority to change
existing resources. SSO, owner administrator access, recovery pairing and product settings
remain pending until the product's setup adapter can configure and verify them.

Explicit `integrations` selections now save the inputs for the implemented adapters:

```yaml
integrations:
  - product: kypulse
    target: nas
    container: pulse
    bundle: /data/installer/setup.json
```

The product must already be declared on that target in `existing` or selected in `apps`.
Only one product may configure a container. For Kubernetes, also supply `namespace` and
`pod`, and use the container's name inside that pod. These are references to protected
target-local files; never put credential values in `stack.yaml`. Typed preparation metadata
contains only settings and protected file references.
Prepare externally supplied bundles according to each product's installer documentation.
KyNotes optionally prepares its native bundle from typed settings saved under the integration:

```yaml
integrations:
  - product: kynotes
    target: nas
    container: notes
    bundle: /data/installer/setup.json
    notes:
      version: 1
      backup:
        dir: /data/backups
        keep: 7
        deposit_interval: 24h
```

`integrate-stack` sends these settings directly to native `apply-setup --stage` stdin,
requires `bundle-preparation`, checks the retained bundle, then runs ordinary setup and
verification. The native path must be `<product data directory>/installer/setup.json`.
It creates a private directory and file without replacing an existing bundle. Different
retained content or unsafe storage is an error; existing input and product data stay intact.
`integrate-stack --check` compares both preparation and running configuration without
creating files. `integrate-product --prepare` uses only matching saved integration settings.
Plan/review discloses preparation, and guided resume retains saved metadata; new interactive
metadata questions and automatic identity/client/connector graph construction remain pending.

Optional `notes.sso` fields are `issuer_url`, `client_id`, `client_secret_file`,
`redirect_uri` (the native `/api/v1/auth/oidc/callback`) and `directory_hmac_secret_file`.
`notes.admins` entries name `issuer`, `subject` and `username`. Optional
`notes.backup.recovery` names `url` and `pairing_code_file`. These are metadata and protected
target-file references, never inline credentials. Credential files must already exist on
the target for setup; preparation neither generates nor distributes them. Global directory
administrator status is insufficient: retain the product's exact administrator assignment
and dedicated account-purpose gates. Other products except KyIdentity still require supplied native bundles.
Cross-target credential delivery and the complete guided stack remain unfinished.

KyIdentity optionally accepts typed secret-free `identity` settings:

```yaml
integrations:
  - product: kyidentity
    target: nas
    container: identity
    bundle: /data/installer/271333e2-c221-4601-b249-563b3d828530/setup.json
    identity:
      schema: kyidentity.setup/1
      setup_id: 271333e2-c221-4601-b249-563b3d828530
      admin:
        username: owner-admin
        email: admin@example.com
      everyday:
        username: owner
        email: owner@example.com
      clients:
        - id: notes
          name: KyNotes
          redirect_uris: [https://notes.example.com/api/v1/auth/oidc/callback]
          administrator_role: kynotes.admin
          everyday_role: kynotes.user
          suite_webhook:
            url: https://notes.example.com/api/v1/sync/events
```

The UUID path must be inside the running product's data directory. Native preparation
requires an initialized database and advertises `bundle-preparation`, `target-credentials`
and `protected-setup-files`. It generates owner/everyday passwords, each confidential
client secret and suite signing secret in private target files, binds immutable intent
and credential digests, commits an audited native ownership receipt, and publishes the
bundle last. Resume retains them. Missing or
changed credentials after completion are errors; deleting filesystem completion markers
never permits replacement, even before account application. `--check` creates nothing. The existing product encryption
key is required for connectors, and SCIM `token_file` is an already-issued protected
reference outside the preparation directory. No inline password or generated-file path
field is accepted in typed saved metadata. The product's `docs/INSTALLER-SETUP.md` owns
native limits, files, interrupted-publication behavior and operational requirements.

Optional client fields include `post_logout_uris`, `groups` and `scim` (`url`, `token_file`,
`groups`). `identity.groups` entries name `name`, optional `description` and `members`,
which may contain only the everyday username. The native protocol and exact role names
must match the destination product. Plan/review discloses generation and guided resume
retains typed metadata. Use `integrate-identity --prepare` only with an exactly matching
saved selection, or run `integrate-stack`. Preparation does not distribute credentials,
prove downstream grants, or produce a secure owner handover. The explicit Notes transfer
below adds distribution; downstream acceptance and owner handover remain required work.

### Transfer a prepared KyIdentity client to Notes

Add `identity_client: notes` to the KyNotes integration, matching a client in the selected
typed KyIdentity integration above. Keep the Notes `admins` list empty: the installer
reads the canonical dedicated owner's subject/username from native KyIdentity ownership
instead of guessing or saving a subject. For example:

```yaml
    - product: kynotes
      target: notes-host
      container: notes
      bundle: /data/installer/setup.json
      identity_client: notes
      notes:
        version: 1
        sso:
          issuer_url: https://identity.example.com
          client_id: notes
          redirect_uri: https://notes.example.com/api/v1/auth/oidc/callback
          client_secret_file: /data/installer/client.secret
          directory_hmac_secret_file: /data/installer/directory.secret
```

Both products/targets must be declared and explicitly selected for integration. The source
client needs exact `kynotes.admin`/`kynotes.user` roles, the destination callback, and the
selected suite directory endpoint. Use only the reserved credential paths above; omit the
directory path only when the source has no directory connector. Referenced KyIdentity URL
must match the Notes issuer. The full saved graph is checked before connections.

The source must advertise `client-credential-export`, and Notes `client-credential-import`;
older binaries are refused before destination preparation. `integrate-stack` applies or
checks Identity first. Notes then connects to its selected source/destination, holds its
destination lock on apply, reads the scoped native export privately in memory, stages the
resolved owner metadata, imports the same bytes and inspects the result. A second export
must match before ordinary product setup/check. No credential enters workstation state,
argv, logs or reports. Failure retains completed target work for native resume; it never
rotates source secrets or overwrites foreign files. The source must already be applied when
using standalone `integrate-product --prepare` for Notes.

Guided resume retains this opt-in and asks for the client ID or `none` when typed Notes SSO
metadata is present; review names the transfer. `none` uses existing protected credentials
and explicit owner metadata. Fresh metadata composition remains unfinished. Local tests
qualify graph validation, private wire scope, owner resolution and native receipt ordering.
With both `KYQ_TEST_IDENTITY_BINARY` and `KYQ_TEST_NOTES_BINARY`, `TestApply` also runs real
source/destination SSH connections, authenticated native commands, private import and TLS
SSO configuration. It compares the canonical owner and credential bytes, inspects/resumes,
rejects changed credentials and excludes an unrelated edge with no NPM credentials. The
owned containers map the certificate's test hostname locally; native literal-loopback URL
refusal stays intact. The fixture also reads bootstrap passwords only from its owned source
product data and signs both installer-created accounts in through native CSRF/password,
Notes PKCE authorization and confidential token exchange. It observes native discovery and
JWKS traffic without recording request bodies, queries or credentials. Owner administration
succeeds while ordinary Notes access is refused; the everyday identity gets the inverse.
Setup still inspects/resumes after sign-on, and final workstation state and reports contain
neither client secrets nor bootstrap passwords. This is HTTP protocol acceptance; browser
UI, owner MFA and full lifecycle qualification remain open.

`TestKubernetesApply` also runs the transfer against native products
in a restricted disposable kind namespace, with named HTTPS services, an owned CA and
test-only TLS sidecars. It compares native owner/credential bindings, inspects/resumes,
rejects changed destination credentials without repair and checks workstation state for
credential values. Run with `CGO_ENABLED=0` (already set by `make e2e`): the sidecars use
the statically compiled test executable. This fixture does not qualify production edge
topology or NetworkPolicy enforcement. Kubernetes sign-on acceptance remains open.

KyNotes must advertise `protected-setup-files`. Its native reader accepts only private
regular bundle/credential files owned by the container user, with clean absolute paths
and protected root/user-owned parents (sticky temporary directories allowed). Symlinks
are refused in every component. Default Kubernetes Secret projections need private
user-owned preparation before setup; do not weaken file validation to use shared mounts.

Run the saved selections explicitly:

```sh
./kyquickstart integrate-stack --state ./installation --check
./kyquickstart integrate-stack --state ./installation
```

`--check` fails when setup is missing or pending; it does not create configuration.
Apply runs KyIdentity first, then other selections in saved order, through the same
locking, capability, activation and verification path as `integrate-product`. It stops
on the first failure or cancellation and retains completed product changes; rerun to
resume through native ownership checks. Unattended output is one JSON report per completed
product with `product`, `target` and the sanitized native `report`. Supply independently
verified `--trust-host-key NAME=SHA256:<fingerprint>` on first SSH connection or compare the
interactive prompt. Reports prove the native setup step, not complete suite readiness.
The planning actions and `apply` do not execute this separate configuration command.

KyIdentity's current setup bundle also creates owned initial directory groups, app-group
assignments and optional native SCIM group delivery. Initial ordinary members may be the
owned everyday identity; dedicated administrators stay separate. Use its
`docs/INSTALLER-SETUP.md` for the schema and limits. Group claims and delivery to a fixture
do not establish a downstream product's permissions or complete group lifecycle.

## Current limits

The demo validates the guided path, plan persistence and driver deployment. It does not
provision the installation owner's administrator or everyday accounts, create backups,
or produce a credential handover. The diagnostic app does not implement the product G11
status response and cannot pass `verify-edge` as a suite product.

Edge prompts support existing NPM/certificates plus an existing Cloudflare tunnel or frp
server. Connector credentials stay in protected target files, never in the wizard. See
[edge setup](edge.md) for those requirements. Fresh NPM, DNS/certificate issuance, new
tunnels/VPS provisioning and Kubernetes connector deployment remain unavailable.

## Catalog KyIdentity on Kubernetes

The catalog's `kyidentity` app installs KyIdentity 0.4.2 on a Kubernetes target from its
attested release image (`internal/catalog/apps/kyidentity/manifest.yaml` records the digest).
It needs an edge route: its issuer is `https://<route hostname>`, plus `:<client_port>` behind a
`lan` edge whose `client_port` is not 443, and a plan without exactly one route for it is refused. Select its setup with an `integrations` entry that names no
container, namespace or pod; `apply` finds the app's own pod.

```yaml
apps:
  - name: kyidentity
    target: k80
edges:
  - target: k80
    routes:
      - app: kyidentity
        hostname: identity.k80.example.com
        forward_host: app.kyq-kyidentity.svc.cluster.local
        forward_port: 5867
        certificate: k80
integrations:
  - product: kyidentity
    target: k80
    bundle: /data/installer/<setup UUID>/setup.json
    identity:
      schema: kyidentity.setup/1
      setup_id: <setup UUID>
      admin: {username: owner-admin, email: owner-admin@example.com}
      everyday: {username: owner, email: owner@example.com}
      clients: []
```

`apply` then runs `kyidentity.secrets`, `.deploy`, `.health`, `.setup`, one `.amend.<app>`
per setup amendment, and `.retire-bootstrap` (below), and last, after the edges, one
`.scim.<app>` per app whose catalog `oidc` names a SCIM token (KyDrive). The setup step is
the typed preparation above (`apply-setup --stage`, credentials generated in the pod's data
directory) followed by `apply-setup`, each read back with `--check`; a rerun skips it, and
changed intent fails with the product's refusal. `integrate-stack` skips installed apps.

- The pod runs as uid 100 under Pod Security `restricted`. Its claim is mounted through a
  0700 `private` subdirectory that an init container creates, because KyIdentity refuses a
  data directory other users can write and claim roots often allow that.
- KyIdentity generates its signing, session and encryption keys in `/data` on first start. With
  no administrator yet it also creates `kyq-bootstrap` and writes its password to
  `/data/first-run-password.txt` (0600), deleted after the first sign-in. That account is
  separate from the owner's: setup never adopts an account it did not create.
- A setup's intent is immutable. A fresh install puts the clients of catalog KyRecovery,
  kyPulse and KyDrive (next sections) into the first setup. An app installed after it joins through a
  setup amendment (Busnes-app/KyIdentity-server#110), which needs KyIdentity 0.4.3 or later
  (capability `setup-amendments`): when the running KyIdentity's retained setup was prepared
  without an app's client, `apply` records `identity_amendment: <UUID>` on that app's
  integration and publishes it with the plan once the run passes (a plan change, refused on a
  runner-owned target). The UUID is derived from the first setup's and the client's, so a run
  that stops first reuses it. `kyidentity.amend.<app>` then stages, applies and checks the
  amendment at `/data/installer/<amendment UUID>/setup.json` after `kyidentity.setup`: the
  client alone, bound to the first setup's owner accounts. The app's credential export reads
  the amendment. KyIdentity 0.4.2, the catalog image today, has no amendments: there
  `kyidentity.setup` fails, names #110, the missing client and 0.4.3, and every app that signs
  in must be in the plan before the first `apply`.
- The issuer cannot change once setup has run: every client and token names it. Before any
  step, `apply` compares the issuer KyIdentity's Deployment runs with to the plan's; when they
  differ and the pod's read-only `apply-setup --check` passes (or the pod cannot be asked), it
  refuses with `issuer_changed` and changes nothing. Every added client's registered URLs,
  amended ones included, are compared the same way: setup never changes a registered client,
  only KyIdentity's own administration does. Changing the route hostname or `client_port`
  therefore needs a fresh KyIdentity.
- `KYIDENTITY_ALLOW_PRIVATE_CALLBACKS=true`: its SCIM and directory connectors deliver to the
  suite's apps through their routes, which resolve to the edge's LAN or pod address, and
  KyIdentity refuses private destinations without it. It opens RFC 1918, ULA and
  carrier-grade NAT (`100.64.0.0/10`) only; loopback, link-local and cloud metadata addresses
  stay refused, and device-supplied push endpoints never get it.
- `kyidentity.retire-bootstrap` follows the setup and its amendments. With KyIdentity 0.4.3 or
  later (capability `disable-user`) it runs `kyidentity disable-user --username kyq-bootstrap
  --require-active-admin <owner admin>`, which revokes the account's sessions and deletes
  `first-run-password.txt`; KyIdentity refuses unless the owner's administrator is another
  active administrator with no end date. A rerun answers unchanged and skips.
- Not yet on KyIdentity 0.4.2: it has no `disable-user`, so the step changes nothing and
  `apply` prints that `kyq-bootstrap` stays an active administrator. Disable it in the admin
  UI after the owner's first sign-in.
- Not yet: `TRUSTED_PROXY_CIDRS` is unset, so KyIdentity sees the edge pod as every client;
  Docker placement (no Compose file); SCIM `active:false` proof (gate G2); backups (the
  `data` volume is declared, nothing schedules it).

## Catalog KyRecovery on Kubernetes

The catalog's `kyrecovery` app installs KyRecovery 0.4.2 from its attested release image. It
depends on the catalog `kyidentity` in the same plan and needs one edge route. Its integration
names its KyIdentity client and a bundle path whose directory is its stable setup UUID:

```yaml
apps:
  - name: kyrecovery
    target: k80
edges:
  - target: k80
    routes:
      - app: kyrecovery
        hostname: recovery.k80.example.com
        forward_host: app.kyq-kyrecovery.svc.cluster.local
        forward_port: 8095
        certificate: k80
integrations:
  - product: kyrecovery
    target: k80
    bundle: /app/data/installer/<another setup UUID>/setup.json
    identity_client: kyrecovery
```

`apply` derives the client from the catalog's `oidc` block and the route and adds it to
KyIdentity's setup intent: `https://<host>/api/auth/callback`, administrator role
`kyrecovery.admin` for the owner's admin account only (KyRecovery refuses everyday
identities), and a signed suite directory at `https://<host>/api/installer/directory`. The
saved plan is unchanged. A hand-written client with that ID must match it exactly. When
KyRecovery is installed after KyIdentity's setup, its client comes in a setup amendment
instead and `apply` adds `identity_amendment` to this integration (KyIdentity 0.4.3 or later).

`kyrecovery.setup` runs after its health check. It exports the client's credentials from
KyIdentity in memory, writes the client secret, the directory signing secret and the bundle
as 0600 files in the bundle's directory through the pod's shell (KyRecovery 0.4.2 has no
staging or import command), and runs `apply-setup` with the owner's admin account as the
exact administrator, then its live `--check`. An existing file is never replaced: if it
differs, for example after KyIdentity was reinstalled, the step fails and keeps it. The
secrets never reach the workstation, its output or its state.

- The pod runs as the image's `kyrecovery` user (uid 10001) under Pod Security `restricted`,
  on a private data directory (`kyrecovery.db`, `secret.key`, `capsules/`; back them up
  together). `KYRECOVERY_COOKIE_SECURE` is `true`: it is always behind the TLS edge.
- **Human step: the custodian ceremony.** Every `apply` that installs KyRecovery ends by
  printing it: sign in through KyIdentity as the owner's admin account, open `/admin/ceremony`,
  choose k of n and print the cards. Until then KyRecovery refuses every pairing with 409, so
  `apply` mints no pairing codes and nothing should before the ceremony.
- KyRecovery 0.4.2 prints its local `admin` break-glass password once, to its first pod's log
  (`kubectl -n kyq-kyrecovery logs deployment/app`), and it is lost when that pod is replaced.
- Pairing a product (`kyrecovery pair generate --service <name>` in the pod, then the
  product's claim) comes after the ceremony and with each product's own backup setup; no
  catalog product pairs yet. The backup status and restore-start screens are KyRecovery's own;
  restic status needs `KYRECOVERY_RESTIC_DIR` and the rest-server, which is not installed yet.
- Not yet: `KYRECOVERY_TRUSTED_PROXIES` is unset, so its rate limits see the edge pod as every
  client; Docker placement; the pod reaches KyIdentity at its public issuer URL, so cluster DNS
  and the edge must answer that name from inside the cluster.

## Catalog kyPulse on Kubernetes

The catalog's `kypulse` app installs kyPulse 0.4.2 from its attested release image. Like
KyRecovery it depends on the catalog `kyidentity`, needs one edge route, and its integration
names its client and a bundle whose directory is its own setup UUID:

```yaml
apps:
  - name: kypulse
    target: k80
edges:
  - target: k80
    routes:
      - app: kypulse
        hostname: pulse.k80.example.com
        forward_host: app.kyq-kypulse.svc.cluster.local
        forward_port: 8080
        certificate: k80
integrations:
  - product: kypulse
    target: k80
    bundle: /app/data/installer/<a third setup UUID>/setup.json
    identity_client: kypulse
```

Its client is derived the same way: `https://<host>/api/sso/kysignon/callback`, administrator
role `kypulse.admin` for the owner's admin account, and the signed directory at
`https://<host>/api/sso/kysignon/sync`. Installed after KyIdentity's setup, it joins through a
setup amendment on KyIdentity 0.4.3 or later; on 0.4.2 it must be in the first setup (#110).

`kypulse.setup` runs after its health check through the same steps as KyRecovery's, with the
existing kyPulse adapter: it requires the `monitoring-targets`, `log-sources` and
`offboarding-alerts` capabilities, applies, waits for kyPulse's graceful restart and its live
`--check`. Its bundle carries kyPulse's `monitoring` extension: one target per other app the plan
installs that has a readiness path and one route, at `https://<route URL host><readiness path>`,
so kyPulse watches each app as a user reaches it, edge included. NPM has no route and is not
watched; kyPulse never watches itself. Apply prints both lists. Targets follow the plan: the
bundle is replaced when they change (the secrets never are), kyPulse adds new targets, keeps one
the bundle no longer names, and refuses an edited one, a foreign one of the same name and any
other installation's.

- The pod runs as the image's `kypulse` user (uid 10001) under Pod Security `restricted`, with
  `KYPULSE_ENV=production` and `KYPULSE_HOST=0.0.0.0` (it listens on loopback otherwise),
  `KYPULSE_APP_URL` from the route and SSO auto-provisioning off: only the owner's admin
  account, which setup creates, signs in. `/app/data` holds the database and `encryption.key`;
  `audit.key` sits on its own claim at `/app/secrets`, as upstream keeps it. Back both up.
- The owner's admin account must not be named `admin`: kyPulse's first start creates a local
  `admin` and setup never adopts an existing name. That account's password is printed once,
  to its first pod's log (`kubectl -n kyq-kypulse logs deployment/app`); apply's handover says so.
- The pod probes each target at its public URL, so cluster DNS must resolve the route hostnames
  and the edge must admit the pod network (on K80, `10.0.0.0/8` in `allow_cidrs`).
- Not yet, needs a human or a product change: no everyday viewer (KyIdentity's export names only
  the owner's admin account, and KyIdentity refuses the everyday one for this client); no log
  sources (none of the catalog apps sends logs yet: a source needs a sender holding its token,
  such as `kypulse-send` or the offboarding bridge); the alert webhook is set in its UI;
  KyRecovery pairing and backups wait on the ceremony; `KYPULSE_TRUSTED_PROXIES` is unset;
  Docker placement. kyPulse 0.4.2 verifies directory events as an HMAC of the body alone while
  KyIdentity 0.4.2 signs them with `syncauth` v1, so directory offboarding events to kyPulse are
  expected to be refused until kyPulse adopts `syncauth` (read from both sources, not run).

## Catalog KyDrive on Docker

The catalog's `kydrive` app installs KyDrive 0.4.2 from its attested release image on a Docker
target; it has no Kubernetes deployment, and placing it on a cluster is refused. It depends on
the catalog `kyidentity` in the same plan, which may run on another target. Its one route may
sit on the cluster's `lan` edge: NPM reaches the Docker host over the LAN.

```yaml
targets:
  - name: docker02
    host: 192.168.1.91
    user: yoshi
    root: /home/yoshi/kyq
apps:
  - name: kydrive
    target: docker02
edges:
  - target: k80
    routes:
      - app: kydrive
        hostname: drive.k80.example.com
        forward_host: 192.168.1.91      # an IP of docker02: KyDrive publishes only there
        forward_port: 18443
        certificate: k80
        status_path: /healthz
integrations:
  - product: kydrive
    target: docker02
    bundle: /data/installer/<a setup UUID>/setup.json
    identity_client: kydrive
```

Its client is `https://<route URL host>/api/sso/kysignon/callback` with roles `kydrive.admin`
(the owner's admin account) and `kydrive.user` (the everyday account), and no signed
directory: KyDrive refuses one and takes people and groups by SCIM. On KyIdentity 0.4.2 it
must be in the first setup; installed later, it joins through a setup amendment on 0.4.3.

`kydrive.setup` runs after its health check. It exports the client from KyIdentity on
KyIdentity's target, holds it in memory, and sends the client secret and the bundle over SSH
stdin to `docker exec -i` in KyDrive's one running installer-managed container (Compose project
`kyq-kydrive`), whose shell writes them as 0600 files in the bundle's directory and never
replaces one. Nothing reaches the workstation's disk, output or state. The bundle names the
client secret file and KyDrive's own SCIM token, `/run/secrets/scim_token`, which its setup
requires. Then `apply-setup` with the owner's admin account as administrator, KyDrive's
graceful restart and its live `--check`.

- The Compose file is KyDrive's own, adapted: it runs as the target user
  (`${KYQ_UID}:${KYQ_GID}`, which every installer Compose command sets), who owns its secrets
  and directories; read-only root, all capabilities dropped, `no-new-privileges`, 1 GiB, and
  20 minutes to stop. `<root>/kydrive/data` (database, `encryption.key`, files) is mounted at
  `/data` and `<root>/kydrive/backups` (its sealed capsules) at `/backups`, both 0700 and created
  by the installer. Data is not at `/app/data`: KyDrive's setup socket refuses a parent
  directory (`/app`, the image's uid 1000) owned by another user.
- `session_secret` and `scim_token` are generated on the target in `<root>/kydrive/secrets`.
  KyDrive reads its session secret only from the environment, so its entrypoint reads the file
  and exports it to the server process: never in Compose or `docker inspect`, readable in the
  process environment by the same uid only.
- `KY_APP_URL` is `https://<route URL host>`. The port is published only on
  `forward_host:forward_port`, which must be an IP address of the Docker target; the catalog
  NPM's egress policy admits exactly that address and port.
- **Firewall.** Binding to the LAN address does not limit who may connect: Docker's published
  ports bypass the host's INPUT rules. Admit only the edge's source in Docker's `DOCKER-USER`
  chain. K80's NPM pods leave the cluster through their node's address (SNAT), so on a single
  node that is `192.168.1.81`:
  `iptables -I DOCKER-USER -p tcp -m conntrack --ctorigdst 192.168.1.91 --ctorigdstport 18443 ! -s 192.168.1.81 -j DROP`.
  The installer neither sets nor tests it; persist it the way the host keeps its rules, and
  widen it to every node if NPM can move.
- Apply's handover names KyDrive's local `admin` break-glass password,
  `<root>/kydrive/data/initial-admin-password` on the target: sign in once (its login form has
  a CAPTCHA; SSO does not), change it and delete the file.
- `kyidentity.scim.kydrive` adds KyIdentity's SCIM connector to KyDrive's client, so
  KyIdentity provisions the owner's admin and everyday accounts, and everyone later assigned
  to KyDrive, at `https://<route URL host>/scim/v2` (KyDrive refuses an account it has not
  been given: "User account not provisioned"). It reads KyDrive's token from
  `<root>/kydrive/secrets/scim_token` over SSH (0600, owned by the target user, nothing
  symlinked), places it as `/data/installer/scim-kydrive.token` (0600, uid 100) in
  KyIdentity's pod, never replacing one, and runs `apply-setup` on
  `/data/installer/scim-kydrive.json`: a pass of the setup that registered the client
  (KyIdentity's first, or its amendment) with the same setup ID and client intent, the paths
  its preparation generated, and `scim` added. KyIdentity takes a connector on such a later
  pass, outside its immutable preparation directories, so the retained intent keeps no
  `scim` and an applied setup or amendment gains the connector without a reset. A second
  amendment naming the client is refused. The step runs after `kydrive.setup`, which creates
  the owner's admin in KyDrive (KyDrive refuses to adopt an account KyIdentity pushed first),
  and after the edges, so the first delivery finds the route; KyIdentity retries a failed one
  from 30 seconds, doubling. KyIdentity's pod must resolve the route's hostname and trust its
  certificate through the system roots.
- Not yet: the Euro-Office editor (`KYDRIVE_EDITOR_URL` unset); document backups
  (`KYDRIVE_BULK_BACKUP_REPOSITORY` unset: they need an independent restic repository, and the
  `backups` directory, on the same disk as `data`, is not one); recovery pairing;
  `KY_TRUSTED_PROXIES` (unset, so KyDrive sees the edge as every client and `verify-edge`
  fails for its route); kyPulse does not watch it (no readiness path on Docker).

## Existing KyIdentity setup

The first real product adapter is `integrate-identity`. It runs the selected
container's `apply-setup` command through SSH/Docker or Kubernetes exec. The
KyIdentity build must include the interface described in
KyIdentity's `docs/INSTALLER-SETUP.md`; existing released images
without that interface are refused. This does not upgrade or adopt the workload.

Prepare the protected bundle and credential files inside the product container
using that contract, or use the typed native preparation selection above. With a Docker target already in `stack.yaml`:

```sh
./kyquickstart integrate-identity --state ./installation \
  --target nas --container kyidentity --bundle /data/installer/bundle.json
```

For Kubernetes, also identify the existing pod and its container:

```sh
./kyquickstart integrate-identity --state ./installation \
  --target cluster --namespace kyidentity --pod REPLACE-WITH-POD-NAME \
  --container kyidentity --bundle /data/installer/bundle.json
```

Add `--check` for stored-configuration verification without product changes.
Applying holds the installer target lock and verifies the resulting configuration.
The container-local interface owns account/client changes; the installer never
edits the identity database or takes ownership of the existing workload.

This configures dedicated administrator and everyday identities and confidential
OIDC clients with explicit app-role assignments. Each client's optional
`administratorRole` and `everydayRole` name the product's roles. With only an
administrator role, everyday access is refused; with no role fields, access remains
everyday-only. Different identities never share the same declared role. Native
KyIdentity code exchange issues the app's roles without the global directory role.
An optional per-client `scim` object references the exact receiver base URL and a protected
container-local `tokenFile`. The product encrypts that credential with its existing key
and attaches the connector to the same app's assignments and roles; adding it after account
provisioning preserves the client identity. Inspection refuses drift and restored holds.
Alternatively, `suiteWebhook` references the exact Ky product directory endpoint and a
protected target-local `signingSecretFile`. The signed connector joins the same OIDC app
atomically, encrypts its signing secret, and refuses protocol/credential/endpoint drift.
Choose one protocol per client; signed suite connectors do not send group resources.
The selected build must advertise `scim-connectors` and `suite-webhook-connectors`.
See the product setup runbook for the bundle and sequencing; this command does not deploy
a receiver or transfer its signing secret to another target.
This does not complete downstream product administrator
grants, groups, resident bridge deployment, sign-on tests, downstream offboarding or a printed
credential handover. Existing references in the guided plan remain references;
this command is a separate explicit action. The production catalog stays closed
until the full readiness gates pass.

For a disposable cross-repository integration test, build KyIdentity with
`CGO_ENABLED=0` and set `KYQ_TEST_IDENTITY_BINARY` to the absolute binary path
when running `make e2e`. The fixture starts its own unexposed container, creates
accounts, a client, distinct app roles and an encrypted app-bound SCIM connector through SSH,
checks configuration and reruns setup. The receiver URL is deliberately unreachable; this
fixture proves configuration and transport, not downstream delivery. Its
credentials and database are temporary; it does not connect to live products.

## KyDrive and KyNotes adapters

Use `integrate-product` with the product's own protected container-local bundle:

```sh
./kyquickstart integrate-product --product kydrive --state ./installation \
  --target nas --container kydrive --bundle /app/data/installer/bundle.json
./kyquickstart integrate-product --product kynotes --state ./installation \
  --target nas --container kynotes --bundle /data/installer/bundle.json --check
```

The same Kubernetes `--namespace`, `--pod` and `--container` selection applies.
KyDrive uses its `docs/INSTALLER-SETUP.md` envelope and requires its protected
SCIM token configured before setup. New SSO settings activate after an explicit
graceful restart; the adapter verifies the running configuration afterward. The
workload controller must restart the product (`always`/`unless-stopped` on Docker).
Existing users and conflicting settings are preserved. Recovery pairing through
the KyDrive bundle remains unavailable and is refused before writes.

KyNotes retains its native `version: 1` bundle from `docs/INSTALLER.md`. Its SSO
store updates live, while backup directory/retention remain startup inputs. The
adapter invokes the product's new read-only inspection after apply. KyIdentity
must also issue the `kynotes.admin` application role; a local administrator grant
alone does not prove signed administrator access. Full dedicated identity migration
and suite acceptance remain part of the ongoing adapter work.

These commands configure only the explicitly selected product and do not adopt its
workload. Production images must contain the corresponding interfaces. The new shared
KyDrive boundary still needs a ky-primitives release; local development uses an explicit
Go workspace. Set `KYQ_TEST_DRIVE_BINARY` for the disposable SSH/Docker activation fixture.

KyPost native configuration can be checked or applied through `integrate-product
--product kypost` with the same explicit target/container/bundle flags. Docker execution
uses the image's `kypost` runtime identity. Its version-1 bundle supports private credential
references; consult the product's `docs/INSTALLER_SETUP.md`. Configuration success retains
pending DNS, relay, receiving, identity-role and recovery qualifications in the report;
no mailbox ownership or administrator assignment is inferred.

kyPulse common-envelope configuration is available through `integrate-product --product kypulse`.
Use its protected OIDC and directory-webhook secret references. New administrator/viewer
identities are owned separately, and apply waits for graceful restart and present-only live
inspection. Its native `monitoring` extension enrolls explicitly owned health targets and
target-bound log sources from separate private token files; offboarding sources can carry
the native failure-alert grant. The adapter checks these capabilities before mutation.
Product `docs/INSTALLER_SETUP.md` documents the bundle and remaining recovery, KyYard,
protected token distribution and suite-composition gates. Existing foreign targets/sources
are preserved and require explicit resolution rather than automatic adoption.
The product's SQLite capsules include active installer source credentials and check them
against the restored database. Putting credentials back on product and bridge targets and
reconciling bridge receipts remain complete-suite restore qualifications.

KyDNS common-envelope setup is available through `integrate-product --product kydns`.
Its administrator-only interface grants exact verified KyIdentity subjects, activates
live and inspects without restarting. Do not assign an everyday identity to this client.
Signed directory offboarding revokes sessions and deletes all identity-bound API-token
descendants while preserving independent legacy recovery credentials. Target-held
credentials are included in sealed backups. Installer backup enrollment and complete
central client/role/directory composition remain pending.

KyForge and KyMark are canceled projects (2026-10-10); the installer no longer offers them.

KyRecovery common-envelope setup is available through `integrate-product --product kyrecovery`.
Docker setup runs as `kyrecovery`; `KYRECOVERY_DATA_DIR` must match the server data directory.
It grants exact administrator subjects, activates live, commits the native keyed SQL audit
with authority, and checks without a restart. Everyday assignments are refused. Signed
directory offboarding revokes sessions while retaining capsules and product pairings.
Setup never performs the custodian ceremony or imports keys. Named-service pairing-code
orchestration and central identity composition remain acceptance gates.

KyMessages console setup (`--product kymessage`, `/app/kymessages`) uses protected common
inputs and one graceful activation. Its dedicated owner binding does not configure the
Matrix stack: MAS assignments, domains/media prerequisites and full chat acceptance remain
separate gates. Product contract: [KyMessage `docs/INSTALLER_SETUP.md`](https://github.com/Busnes-app/KyMessage-Server/blob/master/docs/INSTALLER_SETUP.md).

KyCalendar setup (`--product kycalendar`, `/app/kycalendar`) uses the protected common
contract, exact issuer-bound subjects and `kycalendar.admin`. Configuration activates through
a graceful restart and is checked afterward. Dedicated administrators cannot use calendars,
app passwords or CalDAV. Installer-owned sign-in stays read-only in the product UI. Recovery
enrollment, central group/client composition, existing mixed-use migration and real-device
interop remain separate gates. Contract: [KyCalendar `docs/INSTALLER_SETUP.md`](https://github.com/Busnes-app/KyCalendar/blob/main/docs/INSTALLER_SETUP.md).
The optional `KYQ_TEST_CALENDAR_BINARY` fixture exercises actual SSH create/check/resume.

KyYard setup (`--product kyyard`, `/app/kyyard-server`) uses the protected common
contract, exact issuer/subject assignments and `kyyard.admin`. Its private socket activates
configuration immediately; no restart is requested. Signed directory updates revoke native
sessions, and installer-owned workloads are protected by the runtime adapters. Platform
administration grants no implicit organization access. Tenant/endpoint composition, shared
upgrade orchestration, recovery enrollment and release provenance remain separate gates.
The optional `KYQ_TEST_YARD_BINARY` fixture exercises actual SSH create/check/resume.


## Explicit community Immich setup

`integrate-product --product immich` and saved `integrate-stack` selections support the
pinned community Immich module's initial setup on Docker targets. Register the instance in
`existing`, then explicitly select its container and protected `immich.setup/1` bundle path.
Stock Immich is refused at capability inspection before setup. Kubernetes targets are refused
before a batch changes any product. The production catalog remains gated; this path configures
an explicitly selected module instance and does not install or adopt a stock service.

The installer holds the target lock for apply, verifies native installation/account ownership
through read-only checks, and prints fixed resource labels and native UUIDs. Product secrets,
raw account subjects and arbitrary product output stay out of the report and workstation state.
An inspection with planned resources is incomplete and fails; re-running apply resumes exact
owned state. The module refuses foreign accounts/providers, migration and revoked accounts.
See `../internal/adapters/immich/module/README.md` for the protected container-local bundle.
Disposable qualification also uses an actual KyIdentity binary: owned client/role setup,
Assigned users only, native PKCE sign-in and administrator API access are verified. The
complete identity lifecycle, dedicated administrator ordinary-workflow
separation, bulk backup/restore and production deployment remain acceptance gates.


Owned Immich directory updates now retain a separate native profile/revision receipt.
The original installation bundle can still be checked after legitimate name/email updates;
it verifies the current owned state and preserves it. Role changes are reported honestly,
and installer verification continues to require owner administration and everyday denial.
Foreign or stale native changes require reconciliation. Protected resident directory mode
uses these interfaces for registered owned accounts; controlled native SCIM qualification
covers profile/repeat/role updates and irreversible revocation. A separate actual KyIdentity
worker fixture proves registered-account profile/role and assignment-loss delivery with
native access denial and retained photos. With a separate protected per-app creation opt-in,
the fixture also proves new-assignment worker creation, ordinary subject-bound PKCE sign-in,
global-disablement session/key denial and reopened creation/initialization/revocation proofs.
Installer-managed OAuth requires a provisioned account with an ownership receipt; login
does not create an account or adopt one by email. Native link/unlink changes are refused.
Enable the protected per-app creation policy for ongoing new-account provisioning.
The optional `KYQ_TEST_IMMICH_MULTI=1` producer/create fixture additionally verifies one
actual global disablement across native Immich and Forgejo, retained content, independent
app assignment loss and reopened per-product proofs. Full-suite connector coverage, native-writer coordination, full connector audit/restore
and production admission remain open. Older unreceipted overlays are refused.
