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

### Without the wizard

Unattended callers, KyQuickStart-AI among them, write `stack.yaml` themselves and run
`review`, `preflight` and `apply` with `--state DIR`. The demo catalog holds one app, so each
target needs its own state directory. A Docker target over SSH:

```yaml
demo: true
version: 1
targets:
  - name: lab
    host: 192.0.2.10
    port: 22
    user: kyops
    root: /srv/ky/kyquickstart
apps:
  - name: hello
    target: lab
```

A Kubernetes target names its kubeconfig by path (never its contents):

```yaml
demo: true
version: 1
targets:
  - name: cluster
    kubeconfig: /home/me/.kube/kyquickstart.yaml
apps:
  - name: hello
    target: cluster
```

The first `preflight` of a Docker target needs `--trust-host-key lab=SHA256:<fingerprint>`.
`apply` adds `revision` and `installation_id`, except that a plan installing KyMessages needs
`installation_id` before `review`: its refusal prints a fresh one to add. Outside demo mode, drop `demo: true` and add
the catalog apps, `edges` and `integrations` described in the sections below.

The first computer-name question accepts DNS names such as `K80.example.com`, including
uppercase letters. It derives a safe installation label (`k80-example-com`); Docker's SSH
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
./kyquickstart logs hello --tail 50 --state ./morning-test
./kyquickstart stop hello --state ./morning-test     # Kubernetes: suspend (0 replicas)
./kyquickstart start hello --state ./morning-test
./kyquickstart restart hello --state ./morning-test
./kyquickstart unmanage hello --state ./morning-test
./kyquickstart uninstall hello --state ./morning-test
```

- `install` adds the app and any missing dependencies to the plan on that target.
- `stop`, `start` and `restart` change nothing else: data, secrets and routes stay. `stop`
  records `stopped: true` in the plan, so `apply`, `upgrade`, `restart` and `backup` refuse
  (`app_stopped`) rather than start it again; `start` clears it. Stop an app this way before
  `restore`; a finished restore starts it and clears the flag. `logs` changes nothing, takes
  no lock and redacts only the secrets the app's catalog entry generates. `status` reads each app's live
  state (`running`, `stopped`, `missing`, `unknown`).
- `uninstall` refuses while another installed app depends on it. It drops the app, its routes
  and its `integrations` entry from the plan, disables the app's NPM routes, removes its containers (`docker compose down`, never `-v`) or its Deployment and
  Service, and keeps the app directory, data volumes, claims and secrets on the target.
- `unmanage` removes `ky.managed-by` and `ky.release-set` and records the hand-over in
  `<state>/handover.md`. Docker cannot relabel a container, so existing containers are
  recreated once, unstarted, and only the services that were running (or restarting) are
  started again; `<root>/<app>/.unmanage-running` records both sets until then, so a rerun
  after an interruption finishes with them; on
  Kubernetes the pod-template change replaces the pods once. The installer then refuses to
  install over the app.
- `upgrade`, and `apply`/`install` over a kept deployment, compare the deployed version
  with the catalog: older than `min_from`, skipping a `no_skip` version, a downgrade or an
  unknown version is refused (`upgrade_refused`); a version that migrates data is refused
  until verified pre-upgrade backups exist (`backup_required`; `kyquickstart backup` does not
  lift it yet); any other upgrade needs
  `--acknowledge-no-backup` (`ack_required`), recorded in `results/<app>.upgrade.json` with
  anything the release asks you to check afterwards (the manifest's `upgrade.note`).
  Apps upgrade one at a time in dependency order and stop at the first failure.

`stack.yaml` carries a `revision`, raised on every plan write. Every locked run records it
on each target (`<root>/.plan`, or annotations on the `kyquickstart` namespace). That namespace
is reserved: kyquickstart creates it, and `preflight` and `apply` refuse one it did not create,
so keep the workstation's ServiceAccount and anything else out of it. A target
recording a newer revision is refused (`plan_stale`): another workstation or the suite
runner changed it, so copy its plan back with `kyquickstart pull-plan` first. Markers also
record the plan's `installation_id`; a target another plan manages is refused
(`owned_by_other_plan`) until `kyquickstart apply --take-over-target T` claims it for this plan. Deployments record their catalog version (`kyquickstart/version`);
Docker installs from earlier builds are recreated once by their next `apply` to record it.

## Backups and restore on Docker

`kyquickstart backup APP` takes an encrypted, append-only restic snapshot of a Docker app to
an off-host rest-server; `kyquickstart restore APP` brings the app back from one, with the same
accounts and data, after its whole target directory is lost. Nothing runs on a schedule and
nothing is pruned. Kubernetes apps (KyIdentity, KyNotes, KyCalendar, the edge) are refused, and
`upgrade` still gates data migrations on `backup_required`.

**KyDrive restore gate.** Snapshot restore requires a released KyDrive image advertising
`restore-reset`. The catalog pins 0.4.3, which advertises it; an image without it (0.4.2 and
earlier) is refused before replacing data, while backups and snapshot listing remain available.
The installer never substitutes an unpublished image. With a capable image, it runs the
product's offline grant reset before startup: browser sessions, app passwords, device links,
service tokens and sharing links must be recreated. Accounts, files and workspace ACLs remain.
The probe has no data mounts; reset has only the restored data directory, runs as its target
owner and has no network or published ports.

**The rest-server is yours.** Run [rest-server](https://github.com/restic/rest-server) 0.14.0 on
another machine than the app's target (the installer never deploys it), with TLS and
`--append-only --private-repos`, and one htpasswd user per Docker target. Append-only means the
target can add snapshots but not delete them; `forget` and `prune` need the server's own
administrator credential, which stays off the target. The installer prescribes no firewall rule.

**On the target**, as its SSH user, write the rest-server password for that user to a file,
mode 0600, and, for a private CA, its certificate. `plan` asks for the destination after the
target's directory (blank for none); `review` lists it:

```yaml
targets:
  - name: nas
    host: nas.example.lan
    user: kyq
    backup:
      repository: rest:https://backup.example.lan:8000/nas   # <user> is the one path segment
      credential_file: /home/kyq/.config/kyquickstart/rest-password
      ca_file: /home/kyq/.config/kyquickstart/rest-ca.pem   # optional; system trust otherwise
```

`review`, `plan --noninteractive` and `apply` refuse, before connecting, any scheme but
`rest:https`, credentials, a query or fragment in the URL, a missing user segment, relative
paths, and a `backup` block on a Kubernetes target. Preflight refuses a credential or CA file
that is a symlink, not a regular file or not owned by the SSH user, and a credential file that
is not 0600. Each app's repository is `<repository>/<app>`.

**What a snapshot holds.** The app's catalog `backup` block decides: its bind directories minus
`skip`, its `capsule_secrets` (encrypted by restic, never sealed to KyRecovery here) and, for an
app with a dump `command`, the dump, taken while the app runs (`docker compose exec -T`; an
empty or failed dump takes no snapshot). An app without a dump command is backed up cold:
`docker compose stop`, restic, then `docker compose start` and a health wait, also when the
snapshot failed. KyDrive is backed up cold (its SQLite has no dump command): `data`, `backups`,
`session_secret` and `scim_token`, a stop of seconds to minutes. Apps that keep backup data in
named Docker volumes (KyPost) are refused with `backup_unsupported`.

**How restic runs.** On the target, as the SSH user: `docker run --rm` of the pinned
`restic/restic:0.19.1` (linux/amd64 digest; its only provenance is an unsigned BuildKit SLSA
attestation) with a read-only root, no capabilities, `no-new-privileges`, the default bridge
network, no cache, memory, CPU and PID bounds, and read-only binds of only what the step needs.
The rest-server password is read from its file inside the container, so no password is in an
argv, in the container's `Config.Env`, in a log, in output or in the state directory. The bridge
must reach the rest-server; the installer does not open a firewall for it.

**The repository password.** The first `backup` generates `restic_password` on the target, in
the app's `secrets` directory (0600, never in a snapshot), runs `restic init` only when the
repository does not exist, and prints the command that reads the password. Read it once into
the owner's printed record: without it a lost target cannot be restored. The handover prints
the same command for every Docker app whose target has a destination:

```sh
./kyquickstart backup kydrive --state ./morning-test
ssh -p 22 kyq@nas.example.lan cat /opt/kyquickstart/kydrive/secrets/restic_password
```

**Restore.** `restore APP` lists the app's snapshots (ID, time, host) and changes nothing.
`restore APP --snapshot ID` (8 or 64 lowercase hex) restores one; `--replace` and `--yes` are
refused without `--snapshot`:

1. It refuses (`restore_refused`, nothing changed) a running app (`kyquickstart stop APP` first) and an app with any non-empty
   bind directory or secret file unless `--replace`, which moves them to
   `<root>/<app>/.replaced-<UTC time>/` and never deletes them; neither refusal contacts the
   repository. It then refuses a snapshot not tagged for this app and installation. If
   `restic_password` is gone, it reads the recorded one from stdin (without echo on a
   terminal; piped, on its own line, followed by the app name, or alone with `--yes`) and saves
   it only once the files are restored.
2. It shows the snapshot's time, host and paths and wants the app name typed back, or `--yes`.
3. It restores into a private staging directory in the app's directory and moves the volumes
   and secrets into place (secrets 0600), refusing, before moving anything, any entry, dump or
   directory above them (`kyq`, `volumes`, `secrets`) that the snapshot holds as a symlink or
   not as a plain directory or file: a snapshot written from a compromised target cannot point
   the app at another host path or another app's data.
   It deploys through apply's own steps, starting `restore_order` in order (with a dump
   command: only `service` first, then the dump into `restore_command`, then the rest), and
   waits for the app's health.
4. **People from KyIdentity.** For an app with an `oidc` block it first refuses unless the
   plan's KyIdentity deployment record (`kyquickstart/version`) is v0.4.4 or later ("restore
   needs KyIdentity v0.4.4 or later, which replays removals on resync"), and refuses an
   `existing` KyIdentity, whose version is unknown, or one on a Docker target. As soon as the
   app is healthy (or, when its `backup` names `replay_after`, as soon as that `restore_order`
   service is: the services after it start only once the replay is delivered) it runs
   `kyidentity resync-system --client <client> --wait 10m` in
   KyIdentity's pod: KyIdentity re-sends the connector's state, deactivations included (a
   deletion comes back as a retained, inactive account), and restore goes on only when the
   connector's whole queue is `delivered`. Anything else, or any failure after the app started
   and before that, is `restore_incomplete` (or the step's error) with the app **stopped**: fix
   the connector and rerun `restore`, which starts the app and replays again.
5. It runs the app's setup read-only check and reports the app restored.

For Drive, an atomically published `preparing` marker fences startup before any current
files move aside or restored files become visible. An interrupted publication is not a
completed restore: rerun the named snapshot with `--replace` and confirm again to preserve
partial files and repeat extraction. Completed-file resumes stop any running Drive before
resetting grants again. Empty or malformed markers fail closed; ordinary apply cannot mint
replacement secrets over an incomplete restore.

A rerun of `restore` after any step died resumes from `<root>/<app>/.restore-pending`, or
refuses naming what to do (`--replace` when files were already moved into place, removing a
stale lock by hand). While that file exists, every other command that touches the app
(`apply`, `install`, `upgrade`, `backup`, `unmanage`) refuses: "a restore of <app> is
unfinished and its removal replay not delivered; finish it with kyquickstart restore <app>".

Residuals:

- A removed person's restored sessions stay valid from the app's start until the replayed
  deactivation arrives, a few seconds in the end-to-end test. The bound is the health wait
  (`health.timeout_seconds`) plus the replay's delivery (up to 10 minutes), and "delivered"
  means the app accepted the replay, not that it acted on it. A failed replay stops the app.
- If restore itself is killed or loses SSH after the app starts, the app keeps running until
  you rerun `kyquickstart restore <app>`; until then a person removed after the snapshot can
  still sign in.
- The gate trusts the installer's deployment record, not the running binary.
- Whoever controls a live target can read that app's backups.
- Not tested: a rest-server behind another proxy, repositories above the end-to-end test's
  size, hosts without Docker's userland proxy.

**After losing the target.** Keep the state directory (`stack.yaml`, `known_hosts`) and the
recorded password. Prepare the new host as before (SSH user, the credential and CA files),
then, for KyDrive:

```sh
./kyquickstart restore kydrive --state ./morning-test                 # list; paste the password when asked
./kyquickstart restore kydrive --snapshot 1a2b3c4d --state ./morning-test   # password, then type kydrive
```

Piped instead: `printf '%s\nkydrive\n' "$PW" | ./kyquickstart restore kydrive --snapshot 1a2b3c4d`.

## Hand-off to KyYard

After install, KyYard's Suite page can install, upgrade, uninstall and unmanage apps through the
suite runner: `kyquickstart serve` in its own container beside KyYard, reached only over a
private Unix socket. The runner holds its own SSH key and kubeconfigs; KyYard holds none.

1. Run the runner beside KyYard. On a Docker host use
   [`kyyard-runner.compose.yaml`](kyyard-runner.compose.yaml): the two share only the socket
   directory, both as uid 10001; the runner has its own state volume, no ports, a read-only
   root, no capabilities and `no-new-privileges`. Set `KY_SUITE_RUNNER_SOCKET` in KyYard.
   On a Kubernetes target the catalog KyYard runs the runner in its own pod ("Catalog KyYard
   on Kubernetes"); for a KyYard the installer does not deploy, use
   [`kyyard-runner.k8s.yaml`](kyyard-runner.k8s.yaml) after editing its image digest, storage
   class and NetworkPolicy addresses.
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
   still run from the runner's plan, except that `apply` and `upgrade` refuse while they would
   redeploy the catalog KyYard the runner runs in; copy the plan first:

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
[edge setup](edge.md) for the supported paths.

## Catalog KyIdentity on Kubernetes

Kubernetes targets also need `kubectl` on the workstation, at the cluster's minor version:
kyquickstart itself does not use it, but the human steps it prints do (the Cloudflare and
KyDNS token Secrets, NPM's admin port-forward, reading break-glass and owner passwords).
`preflight` warns when a plan with a Kubernetes target finds no `kubectl` on `PATH`.

The catalog's `kyidentity` app installs KyIdentity 0.4.7 on a Kubernetes target from its
attested release image (`internal/catalog/apps/kyidentity/manifest.yaml` records the digest).
It needs an edge route: its issuer is `https://<route hostname>`, plus `:<client_port>` on an
internal route whose edge's `client_port` is not 443, and a plan without exactly one route for it is refused. Select its setup with an `integrations` entry that names no
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
  the amendment. An existing KyIdentity 0.4.2 has no amendments: there `kyidentity.setup`
  fails, names #110, the missing client and 0.4.3, and every app that signs in must be in the
  plan before the first `apply`.
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
- On a KyIdentity 0.4.2, which has no `disable-user`, the step changes nothing and
  `apply` prints that `kyq-bootstrap` stays an active administrator. Disable it in the admin
  UI after the owner's first sign-in.
- Not yet: `TRUSTED_PROXY_CIDRS` is unset, so KyIdentity sees the edge pod as every client;
  SCIM `active:false` proof (gate G2); backups (the
  `data` volume is declared; `kyquickstart backup` covers Docker apps only).
- 0.4.4's resync replays removals and it ships `resync-system`, which `kyquickstart restore`
  runs after restoring an app whose people come from KyIdentity. An upgrade from 0.4.3 needs
  only `--acknowledge-no-backup`: 0.4.6 adds one defaulted column, 0.4.7 changes no schema, and
  neither rewrites people, access or content.
- 0.4.6 sends a removal (disable, unassignment, deletion) made before KyIdentity first
  delivered the person to an app; 0.4.5 and earlier dropped it and kept no record, so no
  resync can replay it. **After upgrading from 0.4.5 or earlier**, check each app's accounts
  for people you disabled or unassigned right after giving them access, and disable them there.
- 0.4.6 makes anyone whose password an administrator set (in the admin UI or API) choose a new
  one at their next KyIdentity sign-in; until then no app signs them in. The owner's accounts
  from setup are not affected. The bootstrap administrator is flagged too, but `apply` has
  already disabled `kyq-bootstrap` (above), so nobody signs in with it.
- 0.4.7 never gives up on a removal: one to an app that is down retries (at most 30 minutes
  apart) until the app is back, and the provisioning alert opens while one is stuck.

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
  client; the pod reaches KyIdentity at its public issuer URL, so cluster DNS
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
  such as `kypulse-send`); the alert webhook is set in its UI;
  KyRecovery pairing and backups wait on the ceremony; `KYPULSE_TRUSTED_PROXIES` is unset.
  kyPulse 0.4.2 verifies directory events as an HMAC of the body alone while
  KyIdentity 0.4.2 signs them with `syncauth` v1, so directory offboarding events to kyPulse are
  expected to be refused until kyPulse adopts `syncauth` (read from both sources, not run).

## Catalog KyNotes on Kubernetes

The catalog's `kynotes` app installs KyNotes 0.4.2 from its attested release image
(`ghcr.io/busnes-app/kynotes-server:0.4.2@sha256:85f4…6ad9`, built by
`Busnes-app/KyNotes-server` `release-image.yml@refs/tags/v0.4.2` from commit `f597e997`). It
depends on the catalog `kyidentity` and needs one edge route. Docker and Kubernetes are supported; see [Docker catalog products](#catalog-ky-products-on-docker). Its
integration names its client and KyNotes' one fixed bundle path, and nothing else: apply derives
the settings.

```yaml
apps:
  - name: kynotes
    target: k80
edges:
  - target: k80
    routes:
      - app: kynotes
        hostname: notes.k80.example.com
        forward_host: app.kyq-kynotes.svc.cluster.local
        forward_port: 8080
        certificate: k80
integrations:
  - product: kynotes
    target: k80
    bundle: /data/installer/setup.json
    identity_client: kynotes
```

Its client is derived from the catalog: `https://<host>/api/v1/auth/oidc/callback`,
administrator role `kynotes.admin` for the owner's admin account, everyday role `kynotes.user`,
and KyIdentity's signed directory webhook at `https://<host>/api/v1/sync/events` (KyNotes has no
SCIM endpoint). Installed with KyIdentity, the client is in KyIdentity's first setup; installed
later, it joins through a setup amendment (`kyidentity.amend.kynotes`, KyIdentity 0.4.3 or
later).

What apply does:

- `kynotes.deploy` and `kynotes.health`: a restricted pod as distroless `nonroot` (uid 65532),
  read-only root, `/data` and `/backups` on private claim subdirectories, and a 17-minute
  termination grace so a stop drains a running `apply-setup`. KyNotes generates its own pairing
  and salt keys under `/data/secrets` on first start; no break-glass password is set
  (`BOOTSTRAP_ADMIN_PASS` stays unset).
- `kynotes.setup`: the image has no shell, so the client crosses natively. KyIdentity's
  `--export-client` frame is read into memory, the secret-free bundle (issuer, client, callback,
  the owner's admin subject from that export, backup dir and keep as the pod's environment fixes
  them) is staged with `apply-setup --stage` at `/data/installer/setup.json`, the frame is passed
  to `--import-client` on stdin (KyNotes writes `client.secret` and `directory.secret` beside the
  bundle, 0600, never replacing one), KyIdentity is re-exported to confirm nothing changed, then
  `apply-setup --file` runs. Every step is confirmed by its `--check`; a rerun skips.
- Route ordering: KyNotes' setup probes the issuer through KyIdentity's route, and its first-run
  web setup stays open until setup creates the administrator. So `kynotes.setup` runs after the
  edge steps that route KyIdentity, and KyNotes' own route is added only after it passes. A
  failed setup leaves no route to KyNotes.

Before you apply: KyNotes' setup probes KyIdentity's issuer, and its sign-ins exchange tokens,
at KyIdentity's public URL from the KyNotes pod. So cluster DNS must resolve the route
hostnames, the edge must admit the pod network (on K80, `10.0.0.0/8` in `allow_cidrs`), and the
pod must trust the edge's certificate (a public CA does; the kind test adds its throwaway CA).
Otherwise `kynotes.setup` fails, apply adds no KyNotes route and names the issuer it could not
reach; fix it and rerun apply.

Handover: apply prints KyNotes' URL, the admin account (which administers only; it cannot open
notes) and the everyday account (created by KyNotes at its first sign-in). KyIdentity's
`kynotes.user` assignment is the gate: assign it to anyone else who should sign in. Pairing with KyRecovery stays a human step: on KyNotes' Backups screen, a one-time
code from KyRecovery's administrator or the suite public key pinned by hand. KyNotes makes no
backups until then.

Limits:

- `TRUSTED_PROXY_CIDRS` is left at KyNotes' default (loopback only): forwarded headers from the
  edge are ignored, so no client can spoof its address into the rate limiter. KyNotes then
  rate-limits by the edge pod's address.
- Not yet, or unproven: sign-out is local (KyIdentity's setup registers no
  back-channel logout); a directory event (offboarding) reaching KyNotes is not proven live;
  KyRecovery pairing, `KYNOTES_BLOB_TARGET` and restic/K8up blob backups are not configured.

## Catalog KyCalendar on Kubernetes

The catalog's `kycalendar` app installs KyCalendar 0.4.2 from its attested release image
(`ghcr.io/busnes-app/kycalendar:0.4.2@sha256:7ed4…f75b`, built by `Busnes-app/KyCalendar`
`release-image.yml@refs/tags/v0.4.2` from commit `5b7b3c60`). It depends on the catalog
`kyidentity` and needs one edge route. Docker and Kubernetes are supported; see [Docker catalog products](#catalog-ky-products-on-docker). Its integration names its client
and a bundle whose directory is its own setup UUID:

```yaml
apps:
  - name: kycalendar
    target: k80
edges:
  - target: k80
    routes:
      - app: kycalendar
        hostname: calendar.k80.example.com
        forward_host: app.kyq-kycalendar.svc.cluster.local
        forward_port: 8080
        certificate: k80
integrations:
  - product: kycalendar
    target: k80
    bundle: /app/data/installer/<its own setup UUID>/setup.json
    identity_client: kycalendar
```

Its client is derived from the catalog: `https://<host>/api/sso/kysignon/callback`,
administrator role `kycalendar.admin` for the owner's admin account, everyday role
`kycalendar.user`, and KyIdentity's signed directory webhook at
`https://<host>/api/sso/kysignon/sync` (KyCalendar refuses SCIM). Installed with KyIdentity, the
client is in KyIdentity's first setup; installed later, it joins through a setup amendment
(`kyidentity.amend.kycalendar`, KyIdentity 0.4.3 or later).

Owner usernames: KyCalendar's setup accepts only `^[A-Za-z0-9_.-]{3,64}$` (3 to 64 letters,
digits, `.`, `_` or `-`; no spaces or `@`) and never adopts an existing account, and its first
start creates a local `admin`. `plan`, `review` and `apply` therefore refuse, before connecting,
a plan installing KyCalendar whose KyIdentity admin or everyday username breaks that rule or is
`admin` in any case. The message names which of the two to change.

What apply does:

- `kycalendar.deploy` and `kycalendar.health`: a restricted pod as the image's user (uid 10001),
  read-only root, `/app/data` and `/app/backups` on private claim subdirectories, and a 20-minute
  termination grace (5 s HTTP drain plus a backup deposit already on the wire). It has no health
  endpoint; readiness is `GET /api/installer/network`. SSO is on, automatic provisioning off,
  `KY_APP_URL` comes from the route, and `KY_TRUSTED_PROXIES` is unset, so forwarded headers
  from the edge are ignored and no client can spoof its address.
- `kycalendar.setup`, after its health and before the edges, like kyPulse's (KyCalendar does
  not probe the issuer at setup and has no open first-run web setup): KyIdentity's
  `--export-client` frame is read into memory; KyIdentity's read-only `apply-setup --check` of
  its first setup gives the everyday account's subject (the one reused `user` object with the
  plan's everyday username; a missing, duplicate or created entry is refused). The client and
  directory secrets, then the bundle, are placed as 0600 files through the pod's shell, never
  replacing one: a changed secret or subject is refused, not overwritten. The bundle names the
  owner's admin account (subject from the export) in `admins` and the everyday account in
  `everyday`. Then `apply-setup` runs, KyCalendar restarts gracefully, its live `--check` passes,
  and KyIdentity is re-exported to confirm nothing changed. A rerun skips.

KyCalendar admits only those two identities: it creates no account at sign-in, and the
directory webhook updates only accounts its setup created. More users need a later setup
amendment, which apply does not yet make.

Before signing in: KyCalendar fetches KyIdentity's discovery at the first sign-in and exchanges
tokens at KyIdentity's public URL from its pod. So cluster DNS must resolve the route
hostnames, the edge must admit the pod network (on K80, `10.0.0.0/8` in `allow_cidrs`), and the
pod must trust the edge's certificate (a public CA does; the kind test adds its throwaway CA).

Handover: apply prints KyCalendar's URL, the admin account (its console only: it cannot open
calendars, app passwords or CalDAV) and the everyday account (calendars; it cannot reach
administration). Outstanding human steps it names:

- The local break-glass `admin`: its one-time password is in `/app/data/initial-admin-password`
  (0600). The handover prints the commands with the target's kubeconfig (and context, if set):
  `kubectl --kubeconfig <target kubeconfig> -n kyq-kycalendar exec deployment/app -- cat
  /app/data/initial-admin-password`. Sign in with it once and change it; only then delete the
  file (the same command with `rm`), since it is the only copy. Apply never reads it and never
  sets `KY_ADMIN_PASSWORD`.
- Pairing with KyRecovery: on the Backup & recovery screen, a one-time code from KyRecovery's
  administrator, or the suite public key pinned by hand. KyCalendar makes no backups until then.

Limits:

- Not yet, or unproven: everyday accounts beyond the owner's; sign-out is
  local (no back-channel logout); a directory event (offboarding) reaching KyCalendar is not
  proven live; real CalDAV clients with app passwords are not tested (the kind test proves the
  web/API session only); KyRecovery pairing, PostgreSQL mode, MCP access and ICS subscriptions
  are not configured.

## Catalog KyYard on Kubernetes

The catalog's `kyyard` app installs KyYard 0.4.3 from its attested release image
(`ghcr.io/busnes-app/kyyard:0.4.3@sha256:94c1…0756`, built by `Busnes-app/KyYard-server`
`release-image.yml@refs/tags/v0.4.3` from commit `0bf5e1cb`), with the suite runner
(`kyquickstart serve`) beside it in the same pod. It depends on the catalog `kyidentity` and
needs one edge route. Docker is supported too, with a separate private runner and no Docker socket; see [Docker catalog products](#catalog-ky-products-on-docker).

```yaml
apps:
  - name: kyyard
    target: k80
edges:
  - target: k80
    routes:
      - app: kyyard
        hostname: yard.k80.example.com
        forward_host: app.kyq-kyyard.svc.cluster.local
        forward_port: 9273
        certificate: k80
integrations:
  - product: kyyard
    target: k80
    bundle: /data/installer/<its own setup UUID>/setup.json
    identity_client: kyyard
```

**Release binaries only.** The runner image must be the attested `kyquickstart-runner` image
released with the binary, pinned by digest. A release binary carries it: the release workflow
stamps `main.runnerImage=ghcr.io/busnes-app/kyquickstart-runner:X.Y.Z@sha256:…` with the digest it
just published and verified (`scripts/release-binaries.sh`), and `kyquickstart version
--runner-image` prints it ([Install a release](../README.md#install-a-release)). A source build
pins none (`runnerImage` in `cmd/kyquickstart` is empty; `version --runner-image` fails), so
`plan`, `review`, `apply`, `install` and `upgrade` refuse, before connecting, any plan that
deploys KyYard: "cannot install KyYard: source builds pin no runner image; use a release binary".
A pin that is not a digest is refused too. The kind test proves the entry with a runner built
from source and pinned by its kind-local digest, not a released image.

Sign-in: an admin-only client (fleet administration has no everyday workflow):
`https://<host>/api/sso/kyidentity/callback`, administrator role `kyyard.admin`, no everyday
role, and KyIdentity's signed directory webhook at `https://<host>/api/installer/directory`.
KyIdentity assigns only the owner's admin account and refuses everyone else at its per-app gate
(`error=access_denied`), so KyYard creates no account for them. KyYard refuses SCIM, so there is
no `kyidentity.scim.kyyard`. Installed with KyIdentity, the client is in KyIdentity's first
setup; installed later, it joins through `kyidentity.amend.kyyard` (KyIdentity 0.4.3 or later).
KyYard's setup accepts only usernames matching `^[a-z0-9._-]{3,64}$` and has its own local
`admin`, so a plan whose KyIdentity admin username breaks that rule or is `admin` in any case is
refused before connecting.

What apply does:

- `kyyard.deploy` and `kyyard.health`: one restricted pod in `kyq-kyyard` with two containers,
  both uid 10001, read-only root, no capabilities, no privilege escalation, and no
  ServiceAccount token. `app` is KyYard: `/data` on a private 10Gi claim subdirectory,
  readiness `GET /health/ready`, a 20-minute termination grace (5 s drain plus a backup deposit
  on the wire), SQLite with keys it generates into `/data`, no local Docker connection
  (`KY_DOCKER_SOCKET` empty), SSO and SCIM off (setup refuses either), no `KY_ADMIN_PASSWORD`
  or `KY_AGENT_IMAGE`, and `KY_TRUSTED_PROXIES` only the pod's loopback: KyYard refuses
  to start over HTTPS without a trusted proxy, and no proxy connects over loopback, so the
  edge's forwarded headers are ignored and no client can spoof its address. `runner` runs `kyquickstart
  serve --state /state/runner --socket /run/kyquickstart/sock/runner.sock --pod-volumes`
  (requests 50m/64Mi, limit 512Mi) with only its own 1Gi `runner-state` claim at `/state` and
  the `runner-socket` emptyDir at `/run/kyquickstart`. KyYard mounts only that volume's `sock`
  directory, at `/run/kyquickstart/sock` (`KY_SUITE_RUNNER_SOCKET`), which the init container
  creates private first: KyYard checks every ancestor of its socket and refuses the kubelet's
  world-writable volume root. It mounts nothing of the runner's state. The socket is 0600 in a 0700
  directory: any process in the pod as uid 10001 can drive the runner, so a compromised KyYard
  holds the runner's authority, as the suite-runner design accepts. `runner-state` is not
  backed up; a fresh `handoff` recreates its key and token.
- `kyyard.setup`, after its health and before the edges (it does not probe the issuer):
  KyIdentity's client export is read into memory, the client and directory secrets and then
  the bundle are placed as 0600 files under `/data/installer/<uuid>/` through KyYard's shell,
  never replacing one, `apply-setup` activates without a restart, and KyIdentity is re-exported
  to confirm nothing changed. The bundle lists the owner's admin account alone. A rerun skips.

Handover: KyYard's URL, the admin account (administration only), that everyday accounts get no
KyYard access, and the outstanding human steps:

- The local break-glass `admin`: `kubectl --kubeconfig <target kubeconfig> -n kyq-kyyard exec
  deployment/app -c app -- cat /data/initial-admin-password`. Sign in once and change it; only
  then delete the file (the same command with `rm`).
- The hand-off, printed ready to run: `kyquickstart handoff --state '<state>' --kyyard-target
  <target> --runner-pod kyq-kyyard/app.kubernetes.io/name=kyyard`.
- Pairing KyYard with KyRecovery from its backup screen.

The runner never changes its own pod: it applies the plan with its own build's catalog, so
redeploying KyYard could kill a running operation or downgrade it. Under the runner, KyYard's
workload is one note step (`kyyard.runner`); `install`, `uninstall`, `unmanage` and `upgrade`
naming KyYard are refused (`runner_host`), and an upgrade of every app skips it. From the
workstation, `apply` and `upgrade` that would redeploy KyYard are refused while the runner owns
the plan (`owned_by_runner`). To upgrade KyYard: `handoff --revoke`, `apply`, then `handoff`
again. Edge routes still reconcile.

Limits:

- KyYard's Suite page reads the runner through the socket (the kind test reads
  `/api/suite/status` as the admin), but starting an operation there requires an account with a
  local password (`local_password_required`). The KyIdentity administrator setup creates has
  none: start operations as the local `admin` account, after changing its one-time password.
  The workstation CLI refuses them while the runner owns the plan; to use it instead, run
  `handoff --revoke`, the command, then `handoff` again.
- The pod's egress is unrestricted: KyYard needs KyIdentity and registries, the runner the API
  server and the targets' SSH; no policy narrows it yet.
- With the edge untrusted every client shares the edge's rate-limit bucket.
- Unproven or not configured: a directory event (offboarding) reaching KyYard; back-channel
  logout; everyday accounts and organizations; retiring the break-glass account; KyRecovery
  pairing, private recovery and PostgreSQL; enrolling hosts or clusters with `kyyard-agent`.

## Catalog KyDrive on Docker

The catalog's `kydrive` app installs KyDrive 0.4.3 from its attested release image on a Docker
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
- **Firewall: not solved.** Binding to the LAN address does not limit who may connect, so any
  LAN client can reach the published port directly and skip the edge's allow-list (sign-in
  still applies). Do not copy a `DOCKER-USER` source rule blindly: on the K80 pilot's Docker
  host (Docker 29 with firewalld and the userland proxy), a rule admitting only k81's address
  dropped k81 as well and took the route down until it was removed. Restricting the port needs
  a rule tested on that host's firewall backend, from the edge node and from another LAN host.
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
- Backups: with a destination on its target, `kyquickstart backup kydrive` snapshots `data`,
  `backups` and both secrets cold (see "Backups and restore on Docker"); restore needs KyIdentity
  v0.4.4 or later and a released Drive image advertising `restore-reset` (the current 0.4.2
  catalog pin is refused). KyDrive's own `KYDRIVE_BULK_BACKUP_REPOSITORY` stays unset: the installer's
  snapshot is the independent copy, and the `backups` directory, on the same disk as `data`, is not.
- With `euro-office` in the plan, see "Catalog Euro-Office on Kubernetes".
- Not yet: recovery pairing;
  `KY_TRUSTED_PROXIES` (unset, so KyDrive sees the edge as every client and `verify-edge`
  fails for its route); kyPulse does not watch it (no readiness path on Docker).

## Catalog Euro-Office on Kubernetes

The catalog's `euro-office` app is KyDrive's optional document editor: Euro-Office
DocumentServer 9.3.4 (release `v9.3.4-hotfix.1`) on a Kubernetes target, beside the catalog
KyDrive on a Docker target. `plan` offers it right after KyDrive, and only when KyDrive is
installed. `plan` and `review` refuse it without `kydrive` in the plan, on a Docker target, or
without exactly one edge route, which must sit on its cluster's edge.

```yaml
apps:
  - name: kydrive
    target: docker02
  - name: euro-office
    target: k80
edges:
  - target: k80
    routes:
      - app: euro-office
        hostname: office.k80.example.com
        forward_host: app.kyq-euro-office.svc.cluster.local
        forward_port: 80
        certificate: k80
```

It runs on Kubernetes only. The owner asked for the cluster, and the catalog's Docker templates
run every product as the target user with no capabilities, which this editor cannot do.

- **The connection.** KyDrive's Compose file gains `KYDRIVE_EDITOR_URL: https://<office route
  URL host>` and a Compose secret, `editor_secret`. Its entrypoint exports that secret as
  `KYDRIVE_EDITOR_SECRET` beside the session secret, because KyDrive 0.4.3 reads both only from
  the environment. Neither secret appears in Compose or in `docker inspect`. Without
  `euro-office`, KyDrive's Compose file and every step input are unchanged, byte for byte.
- **The shared secret.** `kydrive.secrets` generates `editor_secret` on KyDrive's target with
  its other secrets. `euro-office.secrets` runs after KyDrive's steps. It reads that file over
  SSH into memory (0600, owned by the target user, no symlinks), writes it as `jwt_secret` in
  the editor's Secret `kyq-euro-office/kyq-secrets`, and generates `secure_link_secret` in the
  cluster. Nothing reaches the workstation's disk, output or state. Neither copy is ever
  replaced. If the two differ later, the step fails and names the mismatch; copy one over the
  other by hand.
- **The pod.** Namespace `kyq-euro-office` runs under Pod Security `baseline`, the second
  exception after NPM. The entrypoint runs as root and starts the bundled PostgreSQL,
  RabbitMQ, Redis, nginx and supervisord, which drop to their own users. All capabilities are
  dropped, then six are added back: `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETGID`, `SETUID` and
  `KILL`. Privilege escalation is off, seccomp is `RuntimeDefault`, and the root filesystem is
  writable. `net.ipv4.ip_unprivileged_port_start=0` lets nginx bind port 80 without
  `NET_BIND_SERVICE`. The pod gets 2 CPUs (500m requested) and 4 GiB of memory, requested and
  limited; it used 2.8 GiB at start. It has 120 seconds to stop.
- **How the capabilities were measured.** The pinned image ran under Docker with
  `--cap-drop ALL` and `no-new-privileges`. With none added, `chown` of its `local.json`
  failed. Without `DAC_OVERRIDE`, `sed` could not write into ds-owned files. Without `FOWNER`,
  `chmod /var/run/postgresql` failed. Without `SETUID` or `SETGID`, it exited before nginx
  started. Without `KILL`, supervisord could not stop the ds-owned services ("Operation not
  permitted"). With all six it was healthy in about 45 seconds and stopped cleanly in 4
  seconds. `TestKubernetesEuroOffice` checks the same set in kind.
- **The secrets' files.** The Secret is mounted read-only at
  `/var/www/euro-office/Data/.private`, where the entrypoint reads `jwt_secret` and
  `secure_link_secret`, so it generates and writes neither. The pod spec carries neither
  value. The entrypoint itself writes the JWT secret into its `local.json` inside the container.
- **Claims.** `database` (`/var/lib/postgresql`, 2 GiB) holds edits made since a document's
  last save. `cache` (`/var/lib/euro-office`, 10 GiB) is the document cache. An empty claim would
  hide what the image ships at those paths, so on first start the init container
  `seed-volumes` copies it in. That container uses the editor's image and security context,
  keeps owners and modes, and writes `.kyq-seeded` last, so an interrupted copy runs again.
  RabbitMQ, Redis and the `Data` directory stay in the container and reset when the pod is
  recreated.
- **Network.** Ingress comes only from the edge's pods, on port 80. Egress reaches only
  cluster DNS and the edge's HTTPS container port (443). `ALLOW_PRIVATE_IP_ADDRESS=true` is set
  because KyDrive's route resolves to a private address. The editor fetches the URLs KyDrive's
  signed requests name, but also URLs an editing user supplies, such as an image inserted by
  URL; the egress policy is what confines both.
  - **Enforcement is checked, not assumed.** `euro-office.deploy` writes the policy, then
    starts `kyq-egress-check-*` in the namespace: BusyBox (the credential generator's pinned
    image), restricted, no service-account token, removed by UID, at most 120 seconds. Under
    the same policy it must resolve `kubernetes.default.svc.cluster.local` through cluster
    DNS and must fail to connect to that API server address on 443. Otherwise the step fails
    with `egress_unenforced` (code `step_failed`) and no Deployment is written. A plugin that
    ignores NetworkPolicy egress, such as plain flannel, is refused. An open result is retried
    for about 8 seconds while the policy settles.
  - **Residual.** Any KyDrive user who can edit a document can make the editor GET any
    hostname this NPM routes, over HTTPS, as a client inside the cluster's pod network (which a
    `lan` edge's `allow_cidrs` must admit for the editor to reach KyDrive). That reaches every
    app routed by that edge, past any Cloudflare-side control on those hostnames. The editor's
    request filter works on addresses only, and every routed hostname shares the edge's
    address, so it cannot narrow this per hostname.
  - From the editor's pod, KyDrive's hostname must resolve to an address that reaches this
    edge's HTTPS port, such as a node's NodePort; any other path is blocked. The editor must
    trust the edge's certificate through its image's roots, which works for Let's Encrypt but
    not for a private CA. `ALLOW_META_IP_ADDRESS` and `USE_UNAUTHORIZED_STORAGE` stay `false`.
- **Route path blocks.** NPM answers 404 for `/info`, `/internal`, `/ai-proxy`, `/admin`,
  `/example` and `/welcome` (any case), and for `/info`, `/internal` and `/ai-proxy` behind a
  `/<version>-<hash>/` prefix. The pinned image's nginx (`/etc/nginx/includes/ds-*.conf`,
  inspected 2026-10-22) routes these besides the editor's own paths:
  - `/info` and `/internal` are already limited to 127.0.0.1 there;
  - the admin panel and example app are not started, but nginx still routes to them;
  - `/welcome/` is a static product page;
  - `/ai-proxy` relays to AI services.

  Everything else stays routed: the editor's assets, `/cache/files` (secure links), the
  co-editing socket, `/healthcheck`, and the command and conversion services KyDrive calls.
- **Readiness.** The probe is `GET /healthcheck` on port 80, and only its status code counts.
  The e2e test also checks the body, `true`, through the edge.
- **Provenance: correspondence observed, not verified** (checked 2026-10-22).
  - The registry index of tag `v9.3.4-hotfix.1` lists the pinned amd64 digest `beca380d…`.
  - Its BuildKit SLSA provenance names builder run 33166218952 of `Euro-Office/DocumentServer`,
    a GitHub-hosted push of `refs/tags/v9.3.4-hotfix.1`. GitHub's API reports that run
    successful at commit `4ba0c0f202ae`, which the tag object `4fc31e7` points to.
  - That provenance is unsigned, and the registry stores it beside the image. GitHub holds no
    attestation for the digest (`gh attestation verify` answers 404). The tag's signature is
    unverified, and the ref is unprotected.
  - Nobody has rebuilt the image independently.
  - The workspace rule requires source/build provenance verification before a production
    release, so that gate is still open (`KyDrive-server/docs/EURO-PROVENANCE.md`).
- **Uninstall.** `uninstall euro-office` (and `unmanage`) first redeploys KyDrive without
  the editor's URL and secret, so KyDrive never points at an editor that is gone. That is
  refused with `app_stopped` while KyDrive is stopped, since the redeploy would start it.
  `editor_secret` stays on KyDrive's target, and the editor's Secret stays in the cluster.
- **Upgrades.** `migrates_data: true`, so any version change needs a backup, and Kubernetes
  backups do not exist yet. No upgrade path exists.
- **Verification.** `TestKubernetesEuroOffice` needs the 7 GB image and a 5 GiB kind node, so
  it runs only with `KYQ_TEST_EURO_OFFICE=1` and stays out of `make e2e`. It runs NPM,
  KyIdentity and `euro-office` in kind and KyDrive on an sshd Docker target, then checks:
  - the baseline pod and its capabilities;
  - the seeded claims;
  - the editor's `jwt_secret` equals KyDrive's `editor_secret`;
  - `/healthcheck` through the edge;
  - KyDrive accepts a callback signed with that secret (200) and refuses one signed with
    another (401);
  - the deploy step's egress check passed and left no pod;
  - the route's path blocks answer 404, and the editor's `api.js` still loads;
  - from the editor's pod, KyDrive answers through the edge's HTTPS port, and neither the
    edge's HTTP port nor the API server answers;
  - a skipped rerun;
  - `restart euro-office`;
  - `uninstall euro-office`, after which KyDrive is redeployed without `KYDRIVE_EDITOR_*`
    and answers its editor callback with 503;
  - no copy of the secret in output or state.
- **Not yet.**
  - Opening, editing and saving a document through the edge in a browser. The KyDrive pilot
    showed that path outside the installer.
  - KyDrive-to-editor commands (revocation `drop`, thumbnails). They need KyDrive's container
    to resolve the office host and trust its certificate.
  - NPM's timeouts and body limit for this route, which are NPM's defaults.
  - Backups.

## Catalog KyDNS on Docker

The catalog's `kydns` app installs KyDNS 0.4.2 from its attested release image
(`ghcr.io/busnes-app/kydns-server:0.4.2@sha256:ceb8…405b`, the index built by
`Busnes-app/KyDNS-server` `release.yml@refs/tags/v0.4.2` from commit `ca8d7723`). It depends on
the catalog `kyidentity`, which may run on another target, and needs one edge route.

**Docker only.** `plan`, `review` and `apply` refuse it on a Kubernetes target before
connecting, because:

- LAN resolvers need port 53 itself. On Kubernetes that takes `hostPort` or `hostNetwork`,
  which Pod Security `restricted` and `baseline` forbid, or a LoadBalancer the installer does
  not manage; a NodePort (30000-32767) is useless to resolvers.
- Its admin listener trusts exact proxy addresses only, and an NPM pod's address changes when
  the pod restarts.
- Its image is distroless with no shell and no credential import, so its setup files cannot be
  placed inside a pod. On Docker its data is a bind directory the target user owns, so apply
  places them from the host.

```yaml
apps:
  - name: kydns
    target: docker02
edges:
  - target: k80
    provider: lan
    egress_address: 192.168.1.81    # the edge's own address: the k80 node running NPM
    routes:
      - app: kydns
        hostname: dns.k80.example.com
        forward_host: 192.168.1.91    # an IP of docker02: KyDNS publishes only there
        forward_port: 18053
        certificate: k80
        status_path: /healthz
integrations:
  - product: kydns
    target: docker02
    bundle: /data/installer/<its own setup UUID>/setup.json
    identity_client: kydns
```

`egress_address` is required: without it `plan`, `review` and `apply` refuse KyDNS and name the
field and the edge (`edge.md`). `forward_host` must be an IP of the Docker target. The bundle
must be `/data/installer/<UUID>/setup.json`; anything else is refused before any write.

Its client is derived from the catalog: `https://<host>/auth/sso/callback`, administrator role
`kydns.admin` for the owner's admin account, **no everyday role** (KyIdentity refuses everyone
else KyDNS's client), and KyIdentity's signed directory webhook at
`https://<host>/api/v1/installer/directory`. KyDNS refuses SCIM, an everyday account and
installer backup enrollment. Installed with KyIdentity, the client is in its first setup;
installed later, it joins through a setup amendment (`kyidentity.amend.kydns`, KyIdentity 0.4.3
or later).

What apply does:

- `kydns.deploy` and `kydns.health`: KyDNS's Compose file, adapted. It runs as the target user
  (`${KYQ_UID}:${KYQ_GID}`), read-only root, all capabilities dropped, `no-new-privileges`, and
  one namespaced sysctl, `net.ipv4.ip_unprivileged_port_start=0`, so it can bind 53 without
  root (it changes the container's network namespace, not the host's). 256 MiB, one CPU.
  `<root>/kydns/data` (0700) is mounted at `/data` (`KYDNS_DATA_DIR`); the image's
  `/var/lib/kydns` volume is a tmpfs, so Docker leaves no anonymous volume. It publishes
  `forward_host:forward_port` for its admin UI (8053 inside) and `forward_host:53` UDP and TCP,
  never `0.0.0.0`: that avoids systemd-resolved's `127.0.0.53` stub. `KYDNS_TRUSTED_PROXIES` is
  exactly `egress_address`; `KYDNS_BACKUP_ALLOW_PRIVATE_RECOVERY` is `false`. There is no
  Compose healthcheck (the image has no client for one); health means the container runs.
- `kydns.setup`, after its health and before the edges (so its route appears only once setup
  has closed the first-run `/setup`): KyIdentity's `--export-client` frame is read into memory,
  then the client secret, the directory secret and the bundle are written over SSH, as the
  target user, to `<root>/kydns/data/installer/<UUID>/` (0700 directory, 0600 files): the
  payload on stdin, never in argv; an existing file is compared, never replaced; a symlinked
  directory on the way is refused. The bundle names the container paths
  (`/data/installer/<UUID>/…`) and the owner's admin account in `admins`. Apply waits up to a
  minute for KyDNS's setup socket, then runs `apply-setup` through `docker exec` in its one
  running managed container (Compose project `kyq-kydns`). Setup is live: no restart. KyIdentity
  is re-exported to confirm nothing changed. A rerun skips.
- `kydns.gate`, right after setup and before the edges: the workstation, which is not the edge,
  sends `GET http://<forward_host>:<forward_port>/api/v1/installer/network` with
  `X-Forwarded-Proto: https`, and the step passes only on 403. Anything else fails apply,
  naming `egress_address`: KyDNS trusts a peer other than the edge. If the workstation cannot
  reach that port, the step fails too (nothing was proven, nothing changed): KyDNS is installed
  and set up, and rerunning apply from a LAN host that reaches the port finishes. A KyYard
  runner pod on the node running NPM would be seen as the edge and fail it.
- `kydns.edge`, after the edges: from the catalog NPM's pod, where its proxied requests leave
  from, `curl` asks the same URL with `X-Forwarded-Proto: https`, and the step passes only on
  200 with `proxyHeadersTrusted: true` and scheme `https`. Otherwise apply fails, naming the
  edge and `egress_address`: KyDNS's admin UI would answer 403 through the route (NPM moved
  node, a wrong address, or a Docker host that hides client addresses). Both checks only read.
  KyDNS's route must be on the catalog NPM on a cluster; any other edge is refused before any
  step runs.

With no local password, KyDNS's setup creates a disabled local admin slot, which closes its
token-gated first-run `/setup` (it redirects to `/login`). The `setup-token` it wrote on first
start is dead from then on; it stays on the target only.

Handover: apply prints KyDNS's URL, the admin account (the one account it admits), the DNS
address `<forward_host>:53`, then these outstanding human steps, then its notes:

- Point clients at `forward_host` for DNS: the router's DHCP DNS server, or each client by
  hand. Apply changes no resolver, DHCP server or the edge's `lan_dns`.
- Set the local recovery administrator's password on the target:
  `docker exec -it kyq-kydns-app-1 /usr/local/bin/kydns admin reset-password` (it asks twice;
  `-t` keeps it off the screen). Use it only to recover, at `/login`.
- Pair with KyRecovery on the Backups screen, signed in as that local administrator: a managed
  (KyIdentity) administrator cannot enroll KyDNS's first recovery key.

The notes name the firewall facts: ports 53 and `forward_port` are open on `forward_host`; the
admin UI trusts forwarded requests only from `egress_address` (apply checked the edge is trusted
and the workstation refused); DNS answers the private ranges KyDNS sees (its default `allow_query`). Both rest
on the Docker host showing KyDNS real client addresses, so it tells the operator to restrict
both ports to the LAN and the edge at the host or network firewall.

Limits and unproven:

- **Docker hosts that hide client addresses: not supported.** KyDNS's transport gate and
  `allow_query` see the peer address Docker hands the container. On this workstation (Docker
  29, iptables backend, userland proxy on) every connection to a published port arrives from
  the `kyq-kydns_default` network's gateway, whatever its source. There KyDNS refuses the edge
  (403 through the route) and `kydns.edge` fails apply: fail closed. Do not set
  `egress_address` to that gateway: every peer would pass the gate with a forged
  `X-Forwarded-Proto`, and every DNS client would look private. `kydns.gate` refuses that
  setting. Whether hosts with the userland proxy off, firewalld or nftables keep client
  addresses is unproven, so the edge-trusted path (sign-in through the edge) is unproven too.
  No `DOCKER-USER` rule is prescribed (see KyDrive's section).
- A service already bound on `forward_host:53` makes `kydns.deploy` fail with Docker's error;
  there is no plan-time probe.
- `egress_address` is not derived from where NPM runs: if NPM moves node, set it again.
- Not proven live: a directory event (offboarding) reaching KyDNS; K80 placement.
- Not configured: replicas, DHCP, blocklists and upstreams (KyDNS's defaults), back-channel
  logout, installer backup enrollment.

## Catalog KyPost on Docker

The catalog's `kypost` app installs KyPost, by default a webmail client for mail accounts the
owner already has (native mail on the owner's own domains is an opt-in, chosen at install:
[KyPost native mail](#kypost-native-mail)), from its attested release image (`ghcr.io/busnes-app/kypost-server:0.4.2@sha256:23b5…2353`,
built by `Busnes-app/KyPost-Server` `release-image.yml@refs/tags/v0.4.2` from commit
`621b4d18`). Docker only: a plan placing it on a cluster is refused by `plan`, `review` and
`apply` before any connection. It depends on the catalog `kyidentity`, which may run on another
target, and its one route may sit on the cluster's `lan` edge, as KyDrive's does.

```yaml
apps:
  - name: kypost
    target: docker02
edges:
  - target: k80
    routes:
      - app: kypost
        hostname: post.k80.example.com
        forward_host: 192.168.1.91      # an IP of docker02: KyPost publishes only there
        forward_port: 18866
        certificate: k80
        status_path: /api/installer/network
integrations:
  - product: kypost
    target: docker02
    bundle: /kypost/private/installer/<a setup UUID>/setup.json
    identity_client: kypost
```

Its client is `https://<route URL host>/api/auth/oidc/callback` with roles `kypost.admin` (the
owner's admin account) and `kypost.user` (the everyday account), and no directory or SCIM.
Installed with KyIdentity, the client is in KyIdentity's first setup; installed later, it joins
through a setup amendment (`kyidentity.amend.kypost`, KyIdentity 0.4.3 or later).

What apply does:

- `kypost.deploy` and `kypost.health`: KyPost's own Compose service without its root
  entrypoint. The stock entrypoint starts as root to chown its volumes and needs CHOWN, SETUID,
  SETGID and DAC_OVERRIDE; the catalog runs the same bootstrap, migration and supervisor
  (`kypost-server --mode bootstrap-admin && kypost-server migrate-native && exec supervisord`)
  as `kypost` (uid 1000) with every capability dropped, `no-new-privileges`, a read-only root,
  8 GiB, 4 CPUs, 512 PIDs and 36 minutes to stop (API and daemon each drain up to 1000 s, one
  after the other). Its named volumes `config`, `private`, `state`, `logs` and `ollama-models`
  start from the image's kypost-owned 0700 directories. Two tmpfs mounts are needed and no more:
  `/tmp` (supervisord refuses to start without a temporary directory) and `/home/kypost` (Ollama
  writes its key under `~/.ollama`). Health is its `/api/health`, with a 180 s start allowance.
- `SERVER_BASE_URL` is `https://<route URL host>`: its callback, and Secure session cookies.
  Native mail, receiving and the receiver are off. The catalog sets no `PAIRING_SECRET`,
  `POW_SECRET`, `BOOTSTRAP_ADMIN_*`, `TRUSTED_PROXY_CIDRS`, TLS or SMTP settings: KyPost
  generates its own keys in `/kypost/private` at first start, and its break-glass password goes
  to a file, never into `docker inspect`.
- `kypost.setup`, after its health and before the edges (KyPost's setup makes no call to
  KyIdentity; discovery happens at sign-in): KyIdentity's `--export-client` frame is read into
  memory, the client secret and then KyPost's native bundle are placed as 0600 files, as
  `kypost`, in the bundle's directory through `docker exec` over SSH, never replacing one. The
  bundle is `{"version":1,"sso":{"enabled":true,"issuerUrl":…,"clientId":"kypost","autoProvision":true},"clientSecretFile":…}`.
  Then `apply-setup` runs as `kypost`, SSO applies live (KyPost re-reads it per request), and
  KyIdentity is re-exported to confirm nothing changed. KyPost's report always lists its
  external gates as pending: relay, receiving, everyday assignment, backup/restore and
  HTTPS/proxy. A rerun skips.

Who signs in: KyPost creates an account at first sign-in for anyone KyIdentity lets through,
binding it to KyIdentity's issuer and subject, never to a username (a taken name gets a
suffix). KyIdentity's assignment to the `kypost` client is the gate: assign `kypost.user` to
anyone else who should sign in. `kypost.admin` in the token's roles makes an administrator.

Mail ports: KyPost 0.4.2 has no IMAP, POP3, SMTP or submission listener; it reads and sends
through each user's existing provider, outbound. So apply publishes only HTTP 5866, on the
forward address, and nothing for mail. Inbound port 25 exists only with KyPost's receiving
overlay (an operator-supplied Maddy binary, receiving TLS, MX/DNS, a domain challenge and a
relay), which upstream calls a qualification profile and has not shipped for production; apply
installs none of it, and no `domains`, relay or native mail. KyPost refuses a user's mail server
on a private (LAN) address unless its `SANDBOX_PRIVATE_HOSTS` names it. The catalog leaves that
empty and this release has no plan setting for it (a hand edit of the deployed Compose file is
unsupported and lost whenever apply redeploys it), so mail servers must be reachable at a public address.

Handover: apply prints KyPost's URL, the admin account (for administration only; in this
release it can also add mail accounts, so keep mail to the everyday account), the everyday
account, the assignment rule above, the mail-port facts and the first-start model download
(below). Outstanding human steps it names:

- The local break-glass `admin`: a KyPost account separate from KyIdentity's admin, for when
  KyIdentity is down; owners signing in through KyIdentity never need it. It signs in with
  KyPost's own password sign-in and is created with a must-change password in
  `/kypost/config/first-run-password.txt` (0600, owned by kypost). On the Docker target:
  `docker exec --user kypost kyq-kypost-app-1 cat /kypost/config/first-run-password.txt`. Sign in
  once and change it; only then delete the file (the same command with `rm`). Apply never reads
  it.
- Offboarding: webmail KyPost has no directory, so disabling or unassigning someone in KyIdentity stops
  new sign-ins but not a live KyPost session. Sessions live in memory: `docker restart
  kyq-kypost-app-1` on the target ends every one (the kind test proves it).
- Pairing with KyRecovery, after its custodian ceremony, with a fresh one-time code from
  KyRecovery's administrator.

Limits and residuals:

- No directory for webmail: KyPost verifies the signed webhook only against its own pairing key
  or SSO client secret, while KyIdentity's first setup signs with a secret it generates. Handing
  KyIdentity KyPost's pairing key would also give it KyPost's pickup and device-pairing
  authority, so the catalog gives KyPost no `directory_path`. Native mail adds the directory on
  a later pass signed with the client secret instead (below). Back-channel logout is not
  registered either.
- Forwarded headers are untrusted (`TRUSTED_PROXY_CIDRS` empty), so no client can spoof its
  address, but KyPost sees the edge as every client: everyone shares one rate-limit bucket, and
  `verify-edge` fails for its route. The Docker NAT on a host can also hide the edge's own
  address, so an exact trusted proxy cannot be proven there.
- Ollama pulls its model (`nemotron-3-nano:4b`, about 2.9 GB) by tag at first start and retries
  until it has it. Weights are data, not code, and the image's hash-pinned embedding sorter
  labels mail meanwhile. KyPost also checks GitHub for new releases.
- The port is published only on the forward address, but, as for KyDrive, that does not limit
  who on the LAN may connect: see the firewall note under KyDrive.
- Unproven: separation of the admin account from mail (an upstream gate in this mode), and real
  IMAP/SMTP mail exchange; the kind test proves sign-in and sessions only. Not configured unless
  chosen: native mail and receiving (below). Never configured: KyRecovery pairing, bulk restic backups, mobile or desktop pairing, push
  relays, CAPTCHA and Kubernetes placement. kyPulse does not watch it (no readiness path on
  Docker).

## KyPost native mail

Opt-in, and fixed at KyPost's first install. With a `mail` block on the `kypost` app, KyPost
stores mail itself in one mailbox per person on the owner's own domains, instead of reading
existing IMAP accounts. This follows KyPost's operator guide `docs/guides/NATIVE_MAIL.md`
(sections 1-9) and its installer contract `docs/INSTALLER_SETUP.md`, both of KyPost v0.4.3. Scope: native mailboxes, their domains and
the bundled Maddy receiver on TCP 25 with Rspamd (KyPost's profile B). Not offered: the
Cloudflare Email Routing profile (A), any outgoing relay (the operator's own, set in KyPost's
UI), DNS changes and Kubernetes.

**KyPost's own limits, which apply here unchanged.** The Maddy profile is a controlled
qualification profile for a dedicated test domain; KyPost keeps public production MX gated
(tombstone pruning, capacity recovery, hard volume quotas, power-loss and packaging/licensing
checks). Use a dedicated test domain first, keep the current MX and DNS recorded for rollback,
and change a production domain's MX only under your own approved cutover plan. Restore-hold
release is off by default, and outgoing messages in the `uncertain` or `submitting` state have
no retry tool. KyQuickStart sets no `KYPOST_BULK_BACKUP_REPOSITORY`: KyPost's sealed backups
hold mailboxes only up to 64 MiB each and 256 MiB in all, so real mail volumes outgrow them.

```yaml
apps:
  - name: kypost
    target: docker02
    mail:
      domains: [mail-test.example.com]     # 1 to 32; lowercase ASCII, as KyPost accepts
      receiver:                            # leave out on the first apply (below)
        hostname: mx.mail-test.example.com # the MX target, named by the certificate
        smtp_address: 203.0.113.5          # an IPv4 address of docker02; host TCP 25 publishes here
        maddy_binary: /opt/kypost-receiving/maddy
        tls_dir: /srv/kypost-receiving/tls
        publish_port_25: true              # required: the confirmation that 25 is public
integrations:
  - product: kypost                        # required with mail: domains and directory go here
    target: docker02
    bundle: /kypost/private/installer/<a setup UUID>/setup.json
    identity_client: kypost
```

`plan` asks the same: "Mail mode: imap/native" (default `imap`) on KyPost's Docker target, the
domains, "Deploy the receiver now?" (default `no`) and, for the receiver, its hostname, address,
binary, TLS directory and a separate yes to publishing TCP port 25; `review` lists the choice,
marking port 25 as public.

Refused before anything changes:

- Converting an installed KyPost either way. A Docker target whose `kypost/compose.yaml` shows
  webmail refuses a plan with `mail` (KyPost converts no IMAP account; native mail needs a fresh
  KyPost with new KyIdentity identities, so uninstalling is not enough: its volumes stay). One
  that shows native mail refuses a plan without it (native users would lose their mailboxes).
  Without a Compose file, an existing `kyq-kypost_state` volume (an uninstall leaves it) of no
  recorded mode refuses native mail too. KyPost's guide supports enabling native mail on an
  existing deployment, but its installer contract asks for new identities and a new deployment;
  KyQuickStart takes the stricter reading.
- A `mail` block on any other app, on a Kubernetes target (KyPost is Docker only; the receiver
  needs the host's port 25 and its files) or without the `kypost` integration.
- A receiver without `publish_port_25: true`, with a wildcard, loopback or IPv6 address, or with
  a path holding anything but letters, digits, `.`, `_`, `-` and `/`.
- The receiver before its spool exists: on a first install, or while
  `/kypost/state/receiving` is missing in the running container.

What apply does, besides KyPost's ordinary steps:

- `kypost.deploy` renders KyPost's native configuration: `KYPOST_NATIVE_MAIL=true`, receiving
  and receiver off, and no mail port. With `receiver`, it adds KyPost v0.4.3's
  `docker-compose.receiving.yml` and `docker-compose.rspamd.yml` as they ship: all three native
  flags, `KYPOST_RECEIVING_HOSTNAME`, the Maddy binary and TLS directory mounted read-only at
  KyPost's fixed paths, `smtp_address:25` published to the container's 2525, and Rspamd
  (`rspamd/rspamd:4.2.2@sha256:3352…756de`, KyPost's pin) in KyPost's network namespace as uid
  11333, read-only, no capabilities, 512 MiB, with no published port. Its configuration is
  KyPost's `scripts/rspamd/rspamd.conf` of v0.4.3 byte for byte, shipped in the catalog
  (`internal/catalog/apps/kypost/files/rspamd/rspamd.conf`) and written mode 0644 into the app's
  private `rspamd` directory. Nothing else in KyPost's container changes: still `kypost`, read-only,
  no capabilities.
- `kypost.setup` adds `"domains"` to KyPost's version-1 bundle (no secret: the client secret
  stays in its file beside it). The bundle may be replaced as domains are added; KyPost keeps
  every domain and its challenge, and a domain left out of the plan is not retired (retire it
  in KyPost's UI). apply prints KyPost's reported TXT records after the handover, each checked to
  be its domain's `_kypost-mail` challenge, and KyPost's pending list.
- `kyidentity.directory.kypost`, after the edges: KyIdentity's signed suite directory to
  `https://<KyPost route>/api/sync/webhook`, as a later pass of the setup that registered
  KyPost's client (`<installer>/directory-kypost.json` in KyIdentity's container). It is signed
  with that client's own secret file, which KyPost accepts as the sync secret: no new secret is
  made and KyPost's pairing key never leaves it. From then on, a person assigned to KyPost with
  exactly one primary email on a verified domain gets a mailbox, and removing someone in
  KyIdentity ends their KyPost sessions (their mailbox stays).

Preflight, with the receiver, on the Docker target (read-only): Linux x86_64; `smtp_address` is
one of the host's addresses; `maddy_binary` is a regular executable, not a symlink, writable by
neither group nor others, with KyPost's pinned SHA-256 `6ea4b951…d5e2`; `tls_dir` is a real
directory holding `fullchain.pem` (readable by uid 1000) and `privkey.pem` (owned by uid 1000,
mode 0600 or 0400). The certificate's name and dates are KyPost's own check when the receiver
starts.

### Operator flow

1. Plan with `mail.domains` and no receiver, then `apply`. Port 25 stays closed.
2. Publish each TXT record apply printed, exactly, and keep it published while the domain is in
   use (without it KyPost stops that domain's mailboxes, receiving and sending). As the owner's
   admin, Admin → Server → Mail domain → Verify.
3. Configure the outgoing relay in Admin → Server → Mail domain → 2. Configure outgoing delivery
   (an implicit-TLS SMTP account you own, usually port 465; KyPost refuses STARTTLS and
   plaintext), tick Relay sending domains, then Check saved relay. It checks TLS and login only.
   Publish SPF, DKIM, MAIL FROM and DMARC as your relay provider specifies; KyPost signs no DKIM.
4. Mailboxes: the owner's admin (`kypost.admin`) gets none, by KyPost's design. Give the owner's
   everyday identity, and every mail user, exactly one primary email in KyIdentity on a
   verified domain (plain ASCII). Confirm the mailbox in Admin → Server → Mail addresses.
5. On the target, get Maddy exactly as KyPost's guide says, outside any checkout:

   ```sh
   d=$(mktemp -d)
   curl -fL https://github.com/foxcpp/maddy/releases/download/v0.9.5/maddy-0.9.5-x86_64-linux-musl.tar.zst -o "$d/maddy.tar.zst"
   echo "665cb3ffc43dcad0b904bb2792f31cd1025e484b0bacaf4ece06e2bd0091d5e2  $d/maddy.tar.zst" | sha256sum -c -
   tar --zstd -xf "$d/maddy.tar.zst" -C "$d"
   echo "6ea4b951f15b91fd81d98957e4d4bad7a0cec6d6e1d66b011c765cc9a14e05db  $d/maddy-0.9.5-x86_64-linux-musl/maddy" | sha256sum -c -
   sudo install -d -m 755 /opt/kypost-receiving
   sudo install -m 755 "$d/maddy-0.9.5-x86_64-linux-musl/maddy" /opt/kypost-receiving/maddy
   ```

   The hash pins content; the upstream signature is not verified, and Maddy's GPLv3 obligations
   are yours to review. KyQuickStart downloads nothing. Put the receiving hostname's certificate
   in `tls_dir` as `fullchain.pem` and `privkey.pem`, the key owned by uid 1000 with mode 0600.
6. Create the receiving spool, once (it refuses an existing one; never delete it to retry):

   ```sh
   docker exec --user kypost -e KYPOST_NATIVE_RECEIVING=true kyq-kypost-app-1 kypost-server receiving init
   ```

7. Add `receiver` to the plan (or rerun `plan`) and `apply` again. Publish the printed MX and A
   records (IPv4 only: add no AAAA), open TCP 25 to `smtp_address` with no TCP proxy in front
   (Rspamd's SPF checks and automatic sender blocks need the sender's real address; check that
   `/kypost/logs/receiver.log` shows outside addresses), and check from outside: STARTTLS and the
   certificate on port 25, ordinary and PGP mail with an attachment, an unknown address refused.
   Rspamd refuses scores of 15 and above before acceptance, which can refuse legitimate mail; a
   scanner outage defers senders with 451.
8. Run KyPost's configuration report and resolve every failed check, then make its
   `externalChecks` observations yourself (HTTPS and forwarded address, receiver TLS and
   reachability, actual incoming and outgoing mail, a restore on another host):

   ```sh
   docker exec --user kypost kyq-kypost-app-1 kypost-server setup-status
   ```

After renewing the certificate (copy the files in, then `mv`) or adding a domain, restart the
receiver: `docker exec kyq-kypost-app-1 supervisorctl -c /etc/supervisord.conf restart receiver`.
Removing `receiver` from the plan closes port 25 and stops Maddy and Rspamd; restore your
previous MX first. Mail Maddy accepted waits in the spool until the receiver returns.

Not tested end to end: the unit tests cover the renderings, refusals, bundle, report checks,
directory pass and preflight checks; no CI or e2e run has received or sent real mail through
this profile.

## Catalog KyMessages on Docker

The catalog's `kymessage` app installs the whole KyMessages product on one Docker host: the
console (`app`), Postgres, Synapse, MAS (Matrix Authentication Service), Element and LiveKit,
plus the `/.well-known/matrix/*` delegation. Either all of it installs or apply refuses before
changing anything. Docker only: a plan placing it on a cluster is refused (Kubernetes runs one
container per app).

```yaml
apps:
  - name: kymessage
    target: docker02
    media_address: 192.168.1.225     # call media; one LAN IPv4 address of docker02
edges:
  - target: k80
    routes:                           # one edge, one forward_host, one port per service
      - {app: kymessage, hostname: message.example.com, forward_host: 192.168.1.225, forward_port: 18080, certificate_id: 3}
      - {app: kymessage, role: server_name, hostname: example.com, forward_host: 192.168.1.225, forward_port: 18080, certificate_id: 3}
      - {app: kymessage, role: matrix, hostname: matrix.example.com, forward_host: 192.168.1.225, forward_port: 18008, certificate_id: 3}
      - {app: kymessage, role: auth, hostname: auth.example.com, forward_host: 192.168.1.225, forward_port: 18081, certificate_id: 3}
      - {app: kymessage, role: chat, hostname: chat.example.com, forward_host: 192.168.1.225, forward_port: 18082, certificate_id: 3}
      - {app: kymessage, role: rtc, hostname: rtc.example.com, forward_host: 192.168.1.225, forward_port: 17880, certificate_id: 3}
integrations:
  - product: kymessage
    target: docker02
    bundle: /app/data/installer/<setup UUID>/setup.json
    identity_client: kymessage
    chat_client: kymessage-chat       # exactly <app>-chat; plan writes it
```

- **The server name is permanent.** `plan` asks one domain first: it becomes the Matrix server
  name (every account is `@<name>:<domain>` forever) and the other five hostnames default
  under it. Once Synapse is set up, a plan changing the `server_name` hostname is refused
  (`server_name_changed`); the other hostnames follow `issuer_changed`.
- **Six hostnames, one certificate.** Each needs a DNS record to the edge, and the NPM
  certificate must cover the apex and the subdomains: a wildcard `*.example.com` alone misses
  `example.com`, the server name. The primary route (no `role`) is the console. `server_name`
  serves only `/.well-known/matrix/client` and `/.well-known/matrix/server` (the latter only
  while federation is open), everything else on it is 404, and it shares the console's
  `forward_port` (a different one is refused). `matrix` is Synapse with `/_synapse/admin/` and
  `/_synapse/mas/` closed at the edge, `auth` is MAS, `chat` is Element and `rtc` is LiveKit's
  signalling with `/twirp/` closed.
- **One address.** Every routed port is published on `forward_host`, which must be one LAN
  IPv4 address of the host: a wildcard, loopback, link-local, multicast or IPv6 address, or a
  name, is refused before connecting. `plan` offers `media_address` for it.
- **Calls.** LiveKit publishes TCP 7881 and UDP 7882 on `media_address` only, straight to callers,
  not through the edge, so both are open to your LAN; no TURN is installed, so callers must reach
  that address. Preflight refuses an address the host does not have (`ip -4 addr` lists them);
  the same address classes as `forward_host` are refused before connecting. Federation stays
  closed.
- **Two KyIdentity clients.** `kymessage` (the console: admin `kymessage.admin`, everyday
  `kymessage.user`, the signed directory webhook) and `kymessage-chat` (MAS's upstream provider,
  everyday only, role `kymessage.chat`): the dedicated admin has no chat. Both come in
  KyIdentity's first setup, or together in one setup amendment when KyMessages joins later. MAS's
  provider ID is derived from `installation_id` (a plan without one is refused) and seeded before
  `matrix-init`.
- **Verified email.** MAS takes the Matrix localpart from a verified email's local part. The
  owner's accounts are verified by KyIdentity's setup (v0.4.5 or later); anyone else needs the address
  verified by KyIdentity's mailed link, so **without `identity.mail` only the owner can chat**
  until you set a relay in KyIdentity's admin UI (Settings > Mail). The handover says which:

  ```yaml
  integrations:
    - product: kyidentity
      identity:
        mail: {host: smtp.example.com, port: 587, security: starttls, from: identity@example.com,
               username: kyidentity, password_file: /home/owner/.config/kyq/smtp.password}
  ```

  `security` is `tls` or `starttls`; plaintext SMTP is never used. `password_file` is a 0600
  regular file of yours on this workstation, not a symlink, one line (checked on the opened
  file; each refusal names its reason, for example `chmod 600 <file>`); apply copies it on stdin
  to `<data>/installer/smtp.password` in KyIdentity's container. The relay is set by
  KyIdentity's first setup only: changing or adding it later is refused, pointing at
  KyIdentity's admin UI. `plan` asks for it when it resumes a KyIdentity preparation: blank (or
  `none`) for no relay, and for a relay without authentication, a blank username.
- **Steps.** `kymessage.secrets`, `kymessage.matrix-init` (seed, then matrix-init's first pass from
  the pinned image), `kymessage.credentials` (both clients' secrets from KyIdentity's export to
  0600 files on the host, never replaced, then the second pass that writes MAS's configuration),
  `kymessage.deploy`, `kymessage.health`, `kymessage.setup` (the console's setup with the owner's
  admin and everyday accounts, then both clients rechecked), then the six routes, then
  `kymessage.directory`: apply waits (`resync-system --wait 10m`) until KyIdentity's connector
  has delivered the console's directory, so it reports KyMessages set up only once the console
  holds it (the console's first deliveries retry while it starts). KyIdentity 0.4.6 sends a
  removal made before that delivery; 0.4.5 and earlier dropped it, which this step also covered.
- **Adding and removing people.** To let someone chat, assign them to both "KyMessages" and
  "KyMessages chat": the console's directory webhook, which keeps a person's chat, follows
  "KyMessages". A person the console adds this way is an ordinary member (role `user`, no
  console sign-in). Someone assigned to "KyMessages chat" alone can sign in once, and the
  console's next sweep (on a directory event, or its 5-minute run) locks them in MAS as not
  known to KyMessages and ends their session. **Removing someone from "KyMessages chat" alone
  stops only their new chat sign-ins**: MAS does not ask KyIdentity again when it refreshes a
  token, so their open sessions keep working. To end their chat, remove them from "KyMessages"
  too, or offboard them.
- **Offboarding.** Disabling or deleting a person in KyIdentity reaches the console's directory
  webhook, whose sweep locks them in MAS and ends every MAS session they have
  (`matrix.lock`, `matrix.session_end` in the console's audit): their open session, its refresh
  token, new chat sign-in and call grants are all refused. Re-enabling unlocks them; ended
  sessions stay ended, so they sign in again. A KyIdentity sign-out ends nothing: KyIdentity
  registers no back-channel logout.
- **Backup and restore** follow [Backups and restore on Docker](#backups-and-restore-on-docker):
  a `pg_dumpall` of the stack's Postgres plus `data` (with the console's copy of its directory
  secret), `backups`, `matrix` (configuration, secrets, signing key), `media` and the session
  secret, taken while the stack runs. Restore starts Postgres, loads the dump (without the dump's
  `DROP ROLE`/`CREATE ROLE postgres`, which Postgres refuses for the role restoring it), starts
  MAS and the console, replays KyIdentity's removals (`replay_after: app`), and only then starts
  Synapse, Element and LiveKit.

### KyMessages: provenance, measurements and residuals

- **Images.** The console image is KyMessage v0.4.4's, attested by `Busnes-app/KyMessage-Server`
  `release-image.yml@refs/tags/v0.4.4` from commit `b0f2001e`. The five Matrix images are the
  stock upstream builds KyMessage's `docker-compose.matrix.yml` pins by digest; no attestation
  was found for them.
- **Measured in the end-to-end test.** A chat-only person was locked 0-3 s after their sign-in.
  Offboarding refused the open session and its refresh token 0.6-2.6 s after the disable, for
  the owner's everyday account and for a person added later. Removing "KyMessages chat" alone
  left the open session working; removing "KyMessages" too ended it 2.4-2.7 s later. A person
  who signed in to chat while the console was stopped was locked 2.1 s after it started (the
  console fails closed for people it does not know); disabled while KyIdentity's create to the
  console was failing, they reached its directory as inactive.
  After a restore, the replay's sweep ended the restored session of a person disabled after the
  snapshot 1-3 s before Synapse started, so it never worked again (with the replay after the whole
  stack was healthy, it had worked for about 9 s).
- **Restore window.** While the replay runs, MAS still accepts a removed person's restored
  refresh token, but Synapse is not started until the replay is delivered, so there is no chat
  to use it on. "Delivered" means the console accepted the replay; its sweep then ends the
  sessions in MAS asynchronously, so if the sweep is slower than Synapse's start (or cannot
  reach MAS and waits for its next 5-minute run), the session works until it does. The wait
  before Synapse starts is bounded by MAS's and the console's health wait plus the replay's
  delivery (up to 10 minutes). A failed replay leaves the stack stopped.
- **Residuals.** Routed ports are published on `forward_host` and reachable on the LAN without
  the edge, so the edge's path blocks are defence in depth; Synapse's admin API, MAS and
  LiveKit's API still need their own tokens. Secret files are readable by anyone with Docker
  access on the host. Three console values without a file form are read from files by its
  entrypoint (`KYQ_SECRETS`), never set in Compose. The break-glass password sits in the
  console's first log until `init-admin` replaces it (the handover prints the command).
  Client-IP accuracy behind Docker NAT is unproven. If the console is down while KyIdentity
  delivers a change for someone, the edge answers 502 and KyIdentity holds that person's later
  changes, a removal included, as an uncertain write until an administrator resumes the
  delivery in KyIdentity (the person's offboarding view shows it blocked); a person the console
  never heard of is still locked by its sweep.

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
grants, groups, sign-on tests, downstream offboarding or a printed
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
and suite acceptance remain open.

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
against the restored database. Putting credentials back on product targets remains a
complete-suite restore qualification.

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
inputs and one graceful activation. The catalog `kymessage` installs its whole Matrix stack
([Catalog KyMessages on Docker](#catalog-kymessages-on-docker)). Product contract: [KyMessage `docs/INSTALLER_SETUP.md`](https://github.com/Busnes-app/KyMessage-Server/blob/master/docs/INSTALLER_SETUP.md).

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

## Catalog Ky products on Docker

All catalog Ky products support Docker targets. KyIdentity, KyNotes, KyCalendar,
KyRecovery, kyPulse and KyYard have per-app Compose templates beside their Kubernetes
manifests; KyDrive, KyPost, KyDNS and KyMessages are Docker only (sections above).
The same pinned product images, explicit setup selections and owner/everyday
separation apply. On Docker these six are tested for startup, readiness, native setup capability
and restart (`TestDockerKyCatalog`); sign-in through an edge is tested on their Kubernetes
placement only ([edge compatibility](redirect.md#edge-compatibility)).

Choose the Docker target in `apps` and its integration. Give each app one edge
route with a literal Docker-host `forward_host` and unused `forward_port`; the
installer publishes only that address. KyYard additionally requires its edge's
exact `egress_address` and trusts that one address (`/32` for IPv4, `/128` for IPv6). Use the product's existing fixed
bundle path and an `identity_client`; KyIdentity still takes its typed identity
preparation. Keep protected credentials on targets. Every template uses the SSH
user's UID/GID and private bind directories that the driver creates before Compose.
Products need no host Docker socket.

For KyYard, release binaries inject their verified digest-pinned runner image into
a second `runner` service. Source builds need the release script's digest stamp; Go API callers can supply `Options.RunnerImage`. The two containers share only the
private socket bind; runner state/SSH credentials live in a separate private bind.
Handover prints `handoff --runner-container kyq-kyyard-runner-1`. Revoke ownership
before workstation upgrades of KyYard, then hand off again. The runner refuses to
change its own app. No Docker socket or workstation credential is mounted into it.

Late KyIdentity setup amendments, issuer-change refusal and restore removal replay
use the same native interfaces over `docker exec`. Backups and destructive restores
still follow the existing explicit gates; Docker support does not relax them.
The handover prints commands for the selected runtime. kyPulse's one-time local
bootstrap password is in its first container log; save it securely before replacing
that container. KyDNS retains its client-address/edge gate and host limitations.

The installer uses per-app bridges and routed host ports. Standalone deployments
that use shared `kypost-net` create it through KyPost; consumers, including a fixed-IP
containerized NPM, join it externally. Skipping KyPost requires no shared network.
KyQuickStart does not install KyDrive's Euro-Office editor. KyDrive (master 936e6d3) ships a
Kubernetes renderer (`deploy/render.py`) and no Compose service for it; see Unfinished in
[the redirect record](redirect.md#dispositions).
