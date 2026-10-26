# Purpose

The suite runner: `kyquickstart serve` beside KyYard, and the `serve-keygen`, `serve-import`
and `serve-export` commands the workstation runs in its container. Contract: runner protocol
v1 in `../../docs/superpowers/specs/2026-10-16-suite-runner-design.md`.

## Ownership

- `server.go`: the socket, HTTP routes, request checks, operation records, the HTTP status
  table, redaction and graceful shutdown. Operations call the exported `internal/cli`
  functions; this package holds no installer logic.
- `state.go`: the state directory layout, runner identity, SSH key, plan import and export.
- `runner.go`: command dispatch for `cmd/kyquickstart`.

## Local Contracts

- Before changing startup environment, outbound trust or credentials, read `../../docs/STRONG-CRYPTO.md`; the image supplies the runner TLS profile, while operations reuse the CLI clients.

- Answers must decode with KyYard's client types with unknown fields refused
  (`kyyard_test.go` copies them from KyYard `internal/suite`). Change wire types only with the
  spec and KyYard together. Only `not_found` is a 404.
- The socket is created as `ky-primitives/applysetup.StartLocal` creates its own, using
  `keyfile` for the directory checks; access is its 0600 mode in a 0700 directory.
- `--pod-volumes` (Kubernetes): the state and socket directories are subdirectories the runner
  creates if missing and checks strictly, and their parent, the kubelet's world-writable volume root, must
  be a mount point owned by root or the runner; ancestors above it are checked as usual.
  Residual risk: another container mounting that volume could rename the subdirectory; in the
  runner pod, or the catalog KyYard's pod where the runner is container `runner`
  (`internal/kube` renders it for `kubernetes.suite_runner`), only the runner mounts `/state`
  (after the `private-volumes` init container, which keeps `/state/runner` owner-only), and
  only KyYard (uid 10001) shares the socket.
- The runner never changes the app whose pod it runs in (`internal/cli`, `runner_host`, a 422).
- `plan/` is the CLI state directory and holds no secret; the key, kubeconfigs and
  `runner.json` live beside it. Import accepts only the listed names, regular files, bounded
  sizes and token-only kubeconfigs, and never an older plan.
- One operation at a time; same ID and body repeats nothing, a different body is
  `id_conflict`. Records are rewritten atomically on every step event; `running` at startup
  becomes `unknown`.
- Messages are redacted with the runs' shared redactor before the 512-byte cut.

## Work Guidance

## Verification

- `make ci`: HTTP tests over a temporary socket (every endpoint, busy, id_conflict, repeats,
  limits, unknown fields, startup and shutdown), import hardening, keygen and `--pod-volumes`.
- `FuzzStartRequest` (`fuzz_test.go`) posts arbitrary bodies to `handleStart` on a runner with
  no plan: every answer decodes as KyYard's, is a 400/413 `invalid_request` or a 409
  `owned_by_cli` with a message of at most 512 bytes, nothing is recorded, and an accepted
  request names its file inside `operations/` and re-encodes to an equal request that passes.
- `make e2e`: builds the image, hands a plan to a runner container over the sshd fixture and to
  the documented runner pod in kind (a test-only curl container shares its socket), installs
  and uninstalls through the socket, pulls the plan back, checks the CLI refusal and revokes.
  `TestKubernetesKyYard` does the same with the runner inside the catalog KyYard's pod.

## Child DOX Index
