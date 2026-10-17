# Suite runner: KyYard manages the suite after install

Status: approved 2026-10-16. Replaces "KyYard imports the catalog and upgrade module" in
`2026-10-06-installer-architecture-design.md` (Upgrades, KyYard rows).

## Decision

After install, KyYard manages suite apps through a **suite runner**: the `kyquickstart` binary
in `serve` mode, running as its own container on KyYard's host. KyYard's server talks to it over
a Unix socket and can request only the operations below. The runner reaches targets the way the
CLI does (SSH, kubeconfig) with credentials only it holds.

- KyYard never holds target credentials and never runs shell on targets.
- Agents keep their protocol and their `ky.managed-by=kyquickstart` refusal; suite operations
  never pass through agents.
- The runner is an ordinary KyYard Application (not labelled), so KyYard's image updates move it
  to a newer release set. The release set the runner embeds is the newest one KyYard offers.

## Ownership

`stack.yaml` gains `revision` (integer, +1 on every write). Every mutating command writes a
marker on each target it touched, under the target lock:

- Docker: `<root>/.plan`, JSON `{"revision":N,"owner":"cli"|"runner","runner_id":"<uuid>"}`.
- Kubernetes: annotations `kyquickstart/plan-revision`, `kyquickstart/plan-owner`,
  `kyquickstart/runner-id` on the lock namespace.

A mutating command refuses (`plan_stale`) when a target's marker revision is greater than its own
plan's. After hand-off the owner is `runner`, and under each target's lock:

- The runner acts only on targets whose marker says `owner: runner` with its own `runner_id`;
  otherwise it refuses (`owned_by_cli`, or `owned_by_runner` for another runner's ID).
- The CLI refuses every command that would change the plan (`plan`, `install`, `uninstall`,
  `unmanage`) with `owned_by_runner`: change it from KyYard's Suite page, or run
  `handoff --revoke`. `plan` makes no connection, so it refuses on `handoff.json`, which
  `handoff` and `pull-plan` write into the CLI state directory and `--revoke` removes.
- The CLI runs plan-preserving commands (`apply`, `upgrade`, `integrate-*`, `status`) from the
  runner's plan, after `pull-plan`. Each refuses with `plan_stale` when a target it reads holds a
  newer marker. Markers are per target: one the runner's later operations did not touch keeps
  an older revision. The stale check is therefore "marker ≤ plan", not equality. Since the CLI
  cannot change a runner-owned plan, a plan that passes it is the runner's.

## Commands (KyQuickStart)

| Command | Does |
|---|---|
| `install <app> --target T` | Adds the app to the plan, validates placement, applies it and missing dependencies |
| `uninstall <app>` | Removes workloads, keeps data and the app's secrets on the target; refuses while an installed app depends on it |
| `unmanage <app>` | Removes `ky.managed-by`/`ky.release-set`, drops the app from the plan, records it in `handover.md` |
| `upgrade [app…]` | Upgrade rules, then one app at a time in dependency order; stops at the first failure |
| `status --json` | Per app: version, release set, target, last health, last step results |
| `handoff --kyyard-target T [--runner-container NAME] [--revoke]` | Moves the plan into the runner on target T (below) |
| `pull-plan --kyyard-target T [--runner-container NAME]` | Copies the runner's current plan back to the workstation |
| `serve --state DIR --socket PATH` | Runner mode |
| `serve-keygen`, `serve-import`, `serve-export` (`--state DIR`) | Run inside the runner container by `handoff` and `pull-plan` |

### Upgrade gate

For each app: installed `version` ≥ `upgrade.min_from`, no `upgrade.no_skip` version skipped,
then:

- target version has `migrates_data: true`: refused (`backup_required`) unless a verified
  pre-upgrade backup exists. Until KyRecovery integration lands, this is always refused.
- otherwise: refused unless the request sets `acknowledge_no_backup`; the acknowledgement is
  recorded in the step result.

### Hand-off

Run once from the workstation; a rerun reconciles. Over the CLI's own SSH connection to Docker
target T (KyYard's host), each runner step is `docker exec -i <runner> kyquickstart serve-…
--state /state` (`--runner-container`, default `kyquickstart-runner`). `handoff` connects to
every target and holds every target's lock from step 2 on.

1. `serve-keygen` creates an ed25519 key in `/state/ssh/id_ed25519` (0600) once and prints only
   the public line, commented `kyquickstart-runner <runner_id>`. The runner's ID is chosen once,
   in `/state/runner.json`. The private key never leaves the runner volume.
2. Docker targets: in the SSH user's `~/.ssh/authorized_keys`, every line commented
   `kyquickstart-runner <id>` is replaced by `restrict <key> kyquickstart-runner <runner_id>`;
   other lines are kept byte for byte. The file is rewritten 0600 by rename, and refused if it
   or `~/.ssh` is a symlink, not owned by the user, or group/world-writable. A new runner ID
   therefore also removes an old runner's key.
3. Kubernetes targets: ServiceAccount `kyquickstart-runner` in namespace `kyquickstart`
   (no token automount), ClusterRole and ClusterRoleBinding `kyquickstart-runner` holding
   exactly the cluster-wide verbs of the installer's preflight permission list, a Role and
   RoleBinding for its lock-namespace verbs (Leases), and a token Secret
   `kyquickstart-runner-token` annotated with the runner ID. A token issued to another runner
   ID is deleted and reissued. The runner gets a kubeconfig with that token, the cluster's CA
   inline, and the workstation's API server address; a workstation connection that skips
   certificate verification is refused. No object without the managed label is adopted.
4. `serve-import` reads a tar from stdin: `stack.yaml` at the next revision, `known_hosts`,
   `results/*.json` and `kubeconfigs/<target>.yaml`. Regular files only, those names only (no
   absolute, `..` or nested paths), at most 1 MiB for `stack.yaml` and `known_hosts`, 64 KiB per
   result or kubeconfig, 4096 entries and 16 MiB in all. A kubeconfig must authenticate with a
   token only and verify the server with inline CA data: no exec or auth plugins, files,
   proxies or skipped verification. The import refuses a plan older than the runner's own
   (`pull-plan` first) and rewrites each Kubernetes target to the runner-local kubeconfig in
   the runner's copy only. It writes `stack.yaml` last.
5. The workstation publishes the same revision, writes `handoff.json`, and every target's
   marker records `owner: runner` and the runner ID.

Until step 5 every marker still says `owner: cli`, so the runner refuses to act. A rerun after an
interruption repeats steps 1-4 at a new revision.

`handoff --revoke` (no `--kyyard-target` needed) removes every runner key line and the
ServiceAccount, roles, bindings and token, publishes a new revision, writes `owner: cli` with an
empty runner ID and removes `handoff.json`. Pull the plan first: a target holding a newer marker
refuses. The runner keeps its copy, which can no longer reach the targets.

`pull-plan` runs `serve-export` and replaces the local `stack.yaml` (the previous one becomes
`stack.yaml.bak`) only when the runner's revision is at least the local one. Kubeconfig paths and
contexts keep the workstation's values.

### Runner state

`/state` is the runner's private directory (0700, uid 10001): `runner.json`, `ssh/`,
`kubeconfigs/`, `operations/<id>.json`, and `plan/`, the CLI state directory the runner works
in (`stack.yaml`, `known_hosts`, `results/`, `handover.md`). `plan/` holds no secret, as on the
workstation; credentials live beside it. The runner's SSH accepts only host keys pinned in
`plan/known_hosts`: no prompt and no trust flag.

### Image

`ghcr.io/busnes-app/kyquickstart-runner`: a static `CGO_ENABLED=0` build on distroless static,
pinned by digest, uid 10001, entrypoint `kyquickstart serve --state /state --socket
/run/kyquickstart/runner.sock`, no shell. `docs/kyyard-runner.compose.yaml` runs it beside KyYard:
shared socket volume only, its own state volume, no ports, read-only root, all capabilities
dropped, `no-new-privileges`. `release-image.yml` publishes a release's `:X.Y.Z`, checks that
the image reports the tag's version, attests and verifies it, then moves `:stable`.

## Runner protocol v1

HTTP/1.1 over a Unix socket, JSON bodies, no network listener. The socket is created like
`ky-primitives/applysetup.StartLocal`: absolute canonical path, trusted ancestry, private
directory owned by the runner uid, socket mode 0600. Both containers run as uid 10001 and share
only that directory. Access is the file permission; there is no other authentication.
Requests are at most 64 KiB. Unknown JSON fields are refused.

### `GET /v1/status`

```json
{
  "runner": {"release_set": "0.5.0", "runner_id": "uuid", "plan_revision": 7, "plan_owner": "runner"},
  "operation": "uuid or null",
  "targets": [{"name": "nas", "kind": "docker"}],
  "apps": [{
    "name": "kynotes", "category": "ky", "version": "1.4.0", "target": "nas",
    "state": "installed | failed | unknown",
    "health": {"ok": true, "checked": "RFC 3339"},
    "last_result": {"step": "kynotes.health", "status": "ok | failed", "time": "RFC 3339"}
  }]
}
```

### `GET /v1/catalog`

```json
{
  "release_set": "0.5.0",
  "apps": [{
    "name": "hello", "description": "…", "category": "ky | edge | third-party",
    "version": "1.11.0", "depends_on": [], "installed": false,
    "upgrade": {"min_from": "1.0.0", "no_skip": [], "migrates_data": false}
  }]
}
```

### `GET /v1/plan`

`{"revision": 7, "stack": "<stack.yaml text>"}`. Secret-free by construction.

### `POST /v1/operations`

```json
{
  "id": "uuid v4, chosen by the caller",
  "kind": "install | uninstall | unmanage | upgrade",
  "apps": ["hello"],
  "target": "nas",
  "acknowledge_no_backup": false,
  "actor": "kyyard user id, for lock holder and records"
}
```

- `target` only for `install`; `apps` has exactly one entry except for `upgrade`, where an
  empty list means every installed app. `acknowledge_no_backup` only for `install` and
  `upgrade`. `id` is a lowercase UUID v4; `actor` matches `[A-Za-z0-9][A-Za-z0-9_.:@-]{0,127}`.
  The lock holder is `kyquickstart runner <runner_id> for <actor>`.
- `202 {"id": …}` when started. Repeating the same `id` with the same body returns `202` and
  starts nothing; the same `id` with a different body is `409 id_conflict`.
- `409 busy` while another operation runs (one at a time).
- Only a malformed request (400, or 413 over 64 KiB), `id_conflict`, `busy` and a runner without
  a plan (`409 owned_by_cli`) are refused here. Every other refusal (`unknown_app`,
  `target_locked`, `owned_by_cli`, …) is found while the operation runs and becomes its `error`.

### `GET /v1/operations/{id}`

```json
{
  "id": "…", "kind": "upgrade", "apps": ["hello"], "actor": "…",
  "state": "running | succeeded | failed | unknown",
  "started": "RFC 3339", "finished": "RFC 3339 or null",
  "steps": [{"id": "hello.deploy", "status": "running | ok | failed | skipped",
             "time": "RFC 3339", "diagnostics": "redacted, at most 2048 bytes"}],
  "error": {"code": "…", "message": "…"}
}
```

Operations are stored under `<state>/operations/<id>.json` (atomic writes, 0600), and every step
event rewrites the file. An operation found `running` at startup becomes `unknown`; nothing is
retried automatically. On SIGTERM the runner stops accepting, lets a running operation finish
for up to 4 minutes, then cancels it and records it `unknown` (give the container a longer stop
timeout). `GET /v1/status` reads every target and answers within 8 seconds.

### Error codes

`invalid_request`, `id_conflict`, `busy`, `unknown_app`, `unknown_target`, `already_installed`,
`not_installed`, `dependency` (uninstall while depended on), `plan_stale`, `target_locked`
(message names holder and age), `upgrade_refused` (`min_from`, `no_skip`), `backup_required`,
`ack_required`, `step_failed`, `owned_by_runner` (the CLI may not change a runner-owned plan;
the runner refuses another runner's target), `owned_by_cli` (the runner refuses a target not
handed to it, or has no plan yet), `not_found` (no such operation or route), `internal` (any
failure without a code above, such as an unreachable target).

Error bodies: `{"error": {"code": "…", "message": "…"}}`, message at most 512 bytes, redacted
with the run's redactor (every secret a step read). The same codes and messages appear in an
operation's `error`. A code's HTTP status, when the runner answers with it directly:

| Status | Codes |
|---|---|
| 400 | `invalid_request` (413 when the body exceeds 64 KiB; 405 for a wrong method) |
| 404 | `not_found` only: KyYard reads any 404 as an operation the runner does not know |
| 409 | `id_conflict`, `busy`, `plan_stale`, `target_locked`, `owned_by_runner`, `owned_by_cli` |
| 422 | `unknown_app`, `unknown_target`, `already_installed`, `not_installed`, `dependency`, `upgrade_refused`, `backup_required`, `ack_required` |
| 500 | `step_failed`, `internal` |

## KyYard

- `internal/suite`: socket client for the protocol above.
- Routes under `/api/suite/` (`status`, `catalog`, `plan`, `operations`, `operations/{id}`),
  permissions `suite.read` and `suite.manage`, both platform-admin only.
- Every POST requires typed confirmation (the app name, or `suite` for upgrade-all), the current
  password plus a TOTP code when MFA is enrolled, and CSRF; it is audited with actor, kind, apps
  and outcome. A `suite_operations` table mirrors runner operations for history and audit.
- Suite page: installed apps with Upgrade / Uninstall / Unmanage, available catalog apps with
  Install, upgrade-all, the live operation panel, plan owner and revision.
- When no runner socket is configured the page says so and offers nothing.
