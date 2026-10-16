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
plan's. After hand-off the owner is `runner`; the CLI still acts, but only after
`pull-plan`, and only under the same target lock.

## Commands (KyQuickStart)

| Command | Does |
|---|---|
| `install <app> --target T` | Adds the app to the plan, validates placement, applies it and missing dependencies |
| `uninstall <app>` | Removes workloads, keeps data and the app's secrets on the target; refuses while an installed app depends on it |
| `unmanage <app>` | Removes `ky.managed-by`/`ky.release-set`, drops the app from the plan, records it in `handover.md` |
| `upgrade [app…]` | Upgrade rules, then one app at a time in dependency order; stops at the first failure |
| `status --json` | Per app: version, release set, target, last health, last step results |
| `handoff --kyyard-target T` | Moves the plan into the runner on target T (below) |
| `pull-plan` | Copies the runner's current plan back to the workstation |
| `serve --state DIR --socket PATH` | Runner mode |

### Upgrade gate

For each app: installed `version` ≥ `upgrade.min_from`, no `upgrade.no_skip` version skipped,
then:

- target version has `migrates_data: true`: refused (`backup_required`) unless a verified
  pre-upgrade backup exists. Until KyRecovery integration lands, this is always refused.
- otherwise: refused unless the request sets `acknowledge_no_backup`; the acknowledgement is
  recorded in the step result.

### Hand-off

Run once from the workstation; a rerun reconciles. Over SSH to target T (KyYard's host):

1. Generate an ed25519 key inside the runner volume (`docker exec <runner> kyquickstart
   serve-keygen`); the private key never leaves the runner.
2. Docker targets: append the public key to the SSH user's `authorized_keys` with comment
   `kyquickstart-runner <runner_id>` (replace a line with the same comment). Kubernetes targets:
   create ServiceAccount `kyquickstart-runner` bound to the installer's existing RBAC and give the
   runner a token kubeconfig.
3. Stream `stack.yaml`, `known_hosts` and `results/` as a tar into
   `docker exec -i <runner> kyquickstart serve-import`.
4. Bump the revision; write markers with `owner: runner`.

`handoff --revoke` removes the key line and ServiceAccount and writes `owner: cli`.
`pull-plan` runs `docker exec <runner> kyquickstart serve-export` over the same SSH path.

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
  empty list means every installed app.
- `202 {"id": …}` when started. Repeating the same `id` with the same body returns `202` and
  starts nothing; the same `id` with a different body is `409 id_conflict`.
- `409 busy` while another operation runs (one at a time).

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

Operations are stored under `<state>/operations/<id>.json` (atomic writes, 0600). An operation
found `running` at startup becomes `unknown`; nothing is retried automatically.

### Error codes

`invalid_request`, `id_conflict`, `busy`, `unknown_app`, `unknown_target`, `already_installed`,
`not_installed`, `dependency` (uninstall while depended on), `plan_stale`, `target_locked`
(message names holder and age), `upgrade_refused` (`min_from`, `no_skip`), `backup_required`,
`ack_required`, `step_failed`.

Error bodies: `{"error": {"code": "…", "message": "…"}}`, message at most 512 bytes, redacted.

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
