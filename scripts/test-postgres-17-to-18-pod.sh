#!/bin/bash
# Proves the replicated KyIdentity's database pod (internal/kube/replicas.go) moves from
# PostgreSQL 17 to 18 on its claim. It runs the pod specs apply renders, from the previous
# commit (PREV) and from this checkout, in Docker as a kubelet would (each init container in
# order, then Postgres; the claim's subPath as a volume subpath, emptyDirs as tmpfs, the
# Secret as a read-only volume, fsGroup as the claim's group, read-only root, uid 70).
# Asserts rows, the app role's password over the network and a wrong one refused, the C
# locale and checksums; a dump beside a live 17 refuses and writes nothing; 17 restarts on
# its untouched data; back to 18 after 17 ran refuses, and the documented move-again and
# keep-18 ways out work, also after the dump alone is cleaned up; a rerun is a no-op; a fresh
# claim initialises. Not Kubernetes itself.
# NEW_SRC renders the new side from another source tree (mutation checks).
# usage: scripts/test-postgres-17-to-18-pod.sh   (from the repository; needs Docker, Go, git)
set -Eeuo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
PREV=${PREV:-950c3f8} # origin/master before PostgreSQL 18
P18=postgres:18.7-alpine3.24@sha256:0115267ab5a509022df9319c77be365cef335adb71556c75f60f5360ee247e71
work=$(mktemp -d "${TMPDIR:-/tmp}/kyq-pg18pod.XXXXXX")
id=kyqpg18pod$$
cleanup() {
  docker rm -f "$id-db" >/dev/null 2>&1 || true
  docker volume rm -f "$id-claim" "$id-fresh" "$id-secret" >/dev/null 2>&1 || true
  docker network rm "$id" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; docker logs --tail 30 "$id-db" >&2 2>&1 || true; exit 1; }

# The pod specs: this checkout's, and PREV's through the same renderer copied into it.
render() { (cd "$1" && KYQ_RENDER_POD=$2 go test ./internal/kube -run '^TestWriteDatabasePod$' -count=1 >/dev/null) || fail "render $2"; }
mkdir -p "$work/prev"
git -C "$repo" archive "$PREV" | tar -x -C "$work/prev"
cp "$repo/internal/kube/render_database_test.go" "$work/prev/internal/kube/"
render "$work/prev" "$work/old.json"
[ -z "${NEW_SRC:-}" ] || cp "$repo/internal/kube/render_database_test.go" "$NEW_SRC/internal/kube/"
render "${NEW_SRC:-$repo}" "$work/new.json"
grep -q 'postgres:17' "$work/old.json" || fail "$PREV's database pod is not on PostgreSQL 17"

docker network create "$id" >/dev/null
# The Secret kyq-database: 0440, group 70 (fsGroup), as the kubelet projects it.
docker volume create "$id-secret" >/dev/null
docker run --rm -v "$id-secret:/s" "$P18" sh -c 'printf app-pw > /s/password && printf su-pw > /s/superuser-password && chown 0:70 /s/* && chmod 0440 /s/*'
claim() { docker volume create "$1" >/dev/null; docker run --rm -v "$1:/v" "$P18" chown 70:70 /v; } # fsGroup

# pod <spec> <claim> [init]: the init containers in order, then (unless init) Postgres.
pod() {
  python3 - "$1" "$2" "$id" "${3:-}" > "$work/run.sh" <<'PY'
import json, shlex, sys
spec, claim, id, only_init = json.load(open(sys.argv[1])), sys.argv[2], sys.argv[3], sys.argv[4]
vols = {v["name"]: v for v in spec["volumes"]}
gid = spec["securityContext"]["fsGroup"]
def run(c, detach):
    sc = c["securityContext"]
    a = ["docker", "run", "--name", id + "-db" if detach else id + "-init", "--network", id if detach else "none",
         "--user", f'{sc["runAsUser"]}:{sc["runAsGroup"]}', "--group-add", str(gid), "--cap-drop", "ALL",
         "--security-opt", "no-new-privileges", "--memory", c["resources"]["limits"]["memory"].replace("Mi", "m")]
    a += ["-d"] if detach else ["--rm"]
    if sc.get("readOnlyRootFilesystem"):
        a.append("--read-only")
    for e in c.get("env", []):
        a += ["-e", f'{e["name"]}={e["value"]}']
    for m in c["volumeMounts"]:
        v = vols[m["name"]]
        if "persistentVolumeClaim" in v:
            a += ["--mount", f'type=volume,src={claim},dst={m["mountPath"]}' + (f',volume-subpath={m["subPath"]}' if m.get("subPath") else "")]
        elif "emptyDir" in v:
            a += ["--mount", f'type=tmpfs,dst={m["mountPath"]},tmpfs-mode=1777']
        else:
            a += ["--mount", f'type=volume,src={id}-secret,dst={m["mountPath"]},readonly']
    a += ["--entrypoint", c["command"][0], c["image"]] + c["command"][1:] + c.get("args", [])
    print(shlex.join(a) + (" >/dev/null" if detach else ""))
for c in spec["initContainers"]:
    run(c, False)
if not only_init:
    run(spec["containers"][0], True)
PY
  bash "$work/run.sh"
}
ready() { for _ in $(seq 1 120); do docker exec "$id-db" pg_isready -q -h 127.0.0.1 -U kyidentity -d kyidentity 2>/dev/null && return 0; [ "$(docker inspect -f '{{.State.Running}}' "$id-db")" = true ] || return 1; sleep 1; done; return 1; }
stop() { docker stop -t 120 "$id-db" >/dev/null; docker rm "$id-db" >/dev/null; } # the image's STOPSIGNAL, as containerd sends it
# Over the network from another container: the app's role and password (loopback is trust).
client() { docker run --rm --network "$id" -e PGPASSWORD="$1" "$P18" psql -h "$id-db" -U kyidentity -d kyidentity -XAtc "$2"; }
in_claim() { docker run --rm -v "$1:/v" "$P18" sh -c "$2"; }

# 1. A 17 database from the previous pod spec, with rows.
claim "$id-claim"
pod "$work/old.json" "$id-claim" || fail "17 pod"
ready || fail "17 not ready"
client app-pw "create table users(id bigserial primary key, name text collate \"C\" unique, j jsonb); insert into users(name, j) select 'u' || g, jsonb_build_object('g', g) from generate_series(1, 20000) g" >/dev/null || fail "seed"

# 2. The new pod's dump beside the live 17 refuses and writes nothing.
out=$(pod "$work/new.json" "$id-claim" init 2>&1) && fail "dump ran beside a live 17: $out"
grep -q postmaster.pid <<<"$out" || fail "no live-server message: $out"
in_claim "$id-claim" 'test ! -e /v/private/upgrade-from-17/dump.sql' || fail "dump written beside a live 17"

# 3. The rollout: the old pod stops, the new one dumps, restores and serves 18.
stop
pod "$work/new.json" "$id-claim" || fail "18 pod"
ready || fail "18 not ready after the move"
[ "$(client app-pw 'show server_version_num' | cut -c1-2)" = 18 ] || fail "not 18"
[ "$(client app-pw "select count(*) || ',' || sum((j->>'g')::int) from users")" = "20000,200010000" ] || fail "rows"
[ "$(client app-pw "select datcollate from pg_database where datname = 'kyidentity'")" = C ] || fail "locale"
[ "$(client app-pw 'show data_checksums')" = on ] || fail "checksums"
[ "$(client app-pw 'select rolsuper from pg_roles where rolname = current_user')" = f ] || fail "app role became a superuser"
client wrong 'select 1' >/dev/null 2>&1 && fail "a wrong password logged in"
# shellcheck disable=SC2016 # expanded in the container
in_claim "$id-claim" '[ "$(stat -c %u /v/private/18/docker)" = 70 ] && [ -s /v/private/pgdata/PG_VERSION ]' || fail "layout"

# 4. Rollback: the previous pod spec's 17 starts on its untouched data.
stop
{ pod "$work/old.json" "$id-claim" && ready; } || fail "17 no longer starts on its data"
[ "$(client app-pw 'select count(*) from users')" = 20000 ] || fail "17 data changed"
client app-pw "insert into users(name) values ('written on 17 after the move')" >/dev/null || fail "write on 17"
stop

# 4b. Back to 18 after 17 ran: the dump init container refuses, so 18 never starts.
out=$(pod "$work/new.json" "$id-claim" init 2>&1) && fail "18's dump accepted a 17 that ran since the move: $out"
grep -q 'has run on .* since it was moved to 18' <<<"$out" || fail "no rollback message: $out"
echo "refused as documented: $(grep -o 'postgres-17-to-18: PostgreSQL 17 has run.*' <<<"$out" | head -1)"

# 4c. The documented move again: rename 18 and the dump; 18 has the row, the old 18 is kept.
in_claim "$id-claim" 'mv /v/private/18 /v/private/18.replaced && mv /v/private/upgrade-from-17 /v/private/upgrade-from-17.replaced'
{ pod "$work/new.json" "$id-claim" && ready; } || fail "18 not ready after the move again"
[ "$(client app-pw 'select count(*) from users')" = 20001 ] || fail "the move again lost the row written on 17"
in_claim "$id-claim" 'test -s /v/private/18.replaced/docker/PG_VERSION' || fail "the replaced 18 cluster is gone"
stop

# 5. Rerun: nothing dumped or restored again.
out=$(pod "$work/new.json" "$id-claim" 2>&1) || fail "18 restart: $out"
grep -q 'already done' <<<"$out" || fail "rerun dumped again: $out"
ready || fail "18 not ready after a restart"
! grep -q 'postgres-17-to-18: restored' <<<"$(docker logs "$id-db" 2>&1)" || fail "rerun restored again"
stop

# 5b. Keep 18 after 17 ran: delete the file the refusal names; accepted, and it sticks.
{ pod "$work/old.json" "$id-claim" && ready; } || fail "17 did not start for the keep-18 check"
stop
out=$(pod "$work/new.json" "$id-claim" init 2>&1) && fail "18's dump accepted a 17 that ran (keep-18 check): $out"
keep=$(sed -n 's|.*To keep 18 as it is: delete /v/\([^ ]*\), then start again.*|\1|p' <<<"$out" | tail -1)
[ -n "$keep" ] || fail "the refusal names no file to delete to keep 18: $out"
in_claim "$id-claim" "rm /v/private/$keep"
for _ in 1 2; do
  out=$(pod "$work/new.json" "$id-claim" 2>&1) || fail "keep 18 was not accepted: $out"
  grep -q 'already done' <<<"$out" || fail "keep 18 did not stick: $out"
  ready || fail "18 not ready after keep 18"
  [ "$(client app-pw 'select count(*) from users')" = 20001 ] || fail "rows after keep 18"
  stop
done

# 5c. The documented clean-up of the dump alone (17 kept): later starts go on, and a rollback
# that writes on 17 is still refused on the way back to 18.
in_claim "$id-claim" 'mv /v/private/upgrade-from-17 /v/upgrade-from-17.saved'
out=$(pod "$work/new.json" "$id-claim" 2>&1) || fail "18 after the dump was removed: $out"
grep -q 'already done' <<<"$out" || fail "start after the clean-up: $out"
ready || fail "18 not ready after the clean-up"
stop
{ pod "$work/old.json" "$id-claim" && ready; } || fail "17 did not start after the clean-up"
client app-pw "insert into users(name) values ('written on 17 after the clean-up')" >/dev/null || fail "write on 17"
stop
out=$(pod "$work/new.json" "$id-claim" init 2>&1) && fail "18's dump accepted a 17 that ran since the clean-up: $out"
grep -q 'has run on .* since it was moved to 18' <<<"$out" || fail "no rollback message after the clean-up: $out"

# 6. A fresh claim: nothing to move, the app's role and database from the init script.
claim "$id-fresh"
out=$(pod "$work/new.json" "$id-fresh" 2>&1) || fail "fresh pod: $out"
grep -q 'nothing to move' <<<"$out" || fail "fresh claim did not skip the dump"
ready || fail "fresh 18 not ready"
[ "$(client app-pw 'show server_version_num' | cut -c1-2)" = 18 ] || fail "fresh role or password"
in_claim "$id-fresh" 'test -s /v/private/18/docker/PG_VERSION && test ! -e /v/private/pgdata' || fail "fresh data directory"
echo "PASS: database pod PostgreSQL 17 to 18 (live-17 refusal, move, passwords, rollback, refused return after 17 ran, move again, keep 18, dump clean-up, rerun, fresh claim); Kubernetes itself unproven"
