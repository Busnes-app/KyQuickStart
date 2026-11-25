#!/bin/bash
# Proves the catalog kymessage's PostgreSQL 17 to 18 move on the Compose file deploy renders,
# as deploy lays it out (bind directories owned by the target user, who runs Postgres): a 17
# install made with the previous catalog (PREV, a commit) moves on `up -d` over the running
# stack, rows, roles and passwords intact; a dump beside a live 17 refuses and writes nothing;
# 17 still starts on its untouched data; a rerun is a no-op; a bad dump never yields a
# cluster; a fresh install initialises; a 17 backup (the manifest's commands) restores on 18.
# NEW_APPS renders the new side from another apps directory (mutation checks).
# usage: scripts/test-postgres-17-to-18.sh   (from the repository; needs Docker, Go, git)
set -Eeuo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
PREV=${PREV:-950c3f8} # origin/master before PostgreSQL 18
P18=postgres:18.7-alpine3.24@sha256:0115267ab5a509022df9319c77be365cef335adb71556c75f60f5360ee247e71
work=$(mktemp -d "${TMPDIR:-/tmp}/kyq-pg18.XXXXXX")
id=kyqpg18$$
cleanup() {
  for d in app fresh restored; do
    [ -f "$work/$d/compose.yaml" ] && docker compose -p "$id$d" --project-directory "$work/$d" -f "$work/$d/compose.yaml" down -t 30 >/dev/null 2>&1 || true
  done
  # Postgres wrote these as this user; nothing else to clean.
  rm -rf "$work"
}
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; docker logs --tail 30 "${proj:-x}-postgres-1" >&2 2>&1 || true; exit 1; }

# Render both sides with this checkout's renderer (internal/catalog/render_kymessage_test.go).
render() { (cd "$repo" && KYQ_RENDER_TO=$1 KYQ_RENDER_APPS=${2:-} go test ./internal/catalog -run '^TestWriteKyMessageCompose$' -count=1 >/dev/null) || fail "render $1"; }
mkdir -p "$work/prev-apps"
git -C "$repo" archive "$PREV" internal/catalog/apps | tar -x -C "$work/prev-apps" --strip-components=3
render "$work/old" "$work/prev-apps"
render "$work/new" "${NEW_APPS:-}"
grep -q 'postgres:17' "$work/old/compose.yaml" || fail "$PREV's kymessage is not on PostgreSQL 17"

# An app directory as deploy leaves it: private bind directories, the secret files, and the
# init script matrix-init writes (roles with passwords, databases with the C locale).
appdir() {
  local d=$work/$1
  (umask 077 && mkdir -p "$d/pgdata" "$d/matrix/postgres" "$d/matrix/secrets" "$d/secrets" "$d/data/installer")
  for s in matrix/secrets/postgres_password matrix/secrets/mas_admin_client_secret matrix/secrets/kybackup_db_password secrets/session_secret data/installer/directory_secret; do
    printf 'pw-%s' "${s##*/}" > "$d/$s"
  done
  cat > "$d/matrix/postgres/init.sql" <<'SQL'
CREATE ROLE synapse LOGIN PASSWORD 'synapse-pw';
CREATE DATABASE synapse OWNER synapse ENCODING 'UTF8' LOCALE 'C' TEMPLATE template0;
CREATE ROLE mas LOGIN PASSWORD 'mas-pw';
CREATE DATABASE mas OWNER mas ENCODING 'UTF8' LOCALE 'C' TEMPLATE template0;
SQL
}
use() { # use <app dir> <old|new>: that side's compose.yaml and catalog files in the app dir
  cp "$work/$2/compose.yaml" "$work/$1/compose.yaml"
  if [ -d "$work/$2/pg-upgrade" ]; then (umask 077 && mkdir -p "$work/$1/pg-upgrade"); cp -p "$work/$2/pg-upgrade/"* "$work/$1/pg-upgrade/"; fi
}
compose() { local d=$1; shift; KYQ_UID=$(id -u) KYQ_GID=$(id -g) docker compose -p "$id$d" --project-directory "$work/$d" -f "$work/$d/compose.yaml" "$@"; }
healthy() { # wait for postgres healthy
  for _ in $(seq 1 300); do
    [ "$(docker inspect -f '{{.State.Health.Status}}' "$(compose "$1" ps -q postgres)" 2>/dev/null)" = healthy ] && return 0
    sleep 1
  done
  return 1
}
# Over matrix-db from another container: pg_hba's scram-sha-256 applies (loopback is trust).
client() { local d=$1 pw=$2 role=$3 db=$4; shift 4; docker run --rm --network "$id${d}_matrix-db" -e PGPASSWORD="$pw" "$P18" psql -h postgres -U "$role" -d "$db" -XAtc "$@"; }
in_app() { docker run --rm -u "$(id -u):$(id -g)" -v "$work/$1:/v" "$P18" sh -c "$2"; }

# 1. A 17 install from the previous catalog, with Synapse-shaped rows.
proj=${id}app
appdir app; use app old
compose app up -d postgres >/dev/null 2>&1 || fail "17 did not start"
healthy app || fail "17 not healthy"
compose app exec -T postgres psql -U synapse -d synapse -X -q -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
create table events(id bigserial primary key, room text, body jsonb);
insert into events(room, body) select 'r' || (g % 50), jsonb_build_object('g', g) from generate_series(1, 20000) g;
create table c(x text collate "C" primary key);
insert into c values ('a'), ('B'), ('é');
SQL
# The manifest's backup, taken on 17, for step 8.
cmd=$(python3 -c 'import json,shlex,sys; print(shlex.join(json.load(open(sys.argv[1]))[sys.argv[2]]))' "$work/new/backup.json" command)
restore=$(python3 -c 'import json,shlex,sys; print(shlex.join(json.load(open(sys.argv[1]))[sys.argv[2]]))' "$work/new/backup.json" restore)
eval "compose app exec -T postgres $cmd" > "$work/backup-17.sql" && [ -s "$work/backup-17.sql" ] || fail "17 backup"

# 2. A dump beside the live 17 refuses and writes nothing.
use app new
out=$(compose app run --rm --no-deps postgres-17-dump 2>&1) && fail "dump ran beside a live 17: $out"
grep -q postmaster.pid <<<"$out" || fail "no live-server message: $out"
[ ! -e "$work/app/pgdata/upgrade-from-17/dump.sql" ] || fail "dump written beside a live 17"

# 3. The move: up -d over the running 17 stack, no down first.
compose app up -d postgres >/dev/null 2>&1 || fail "up with the new catalog"
healthy app || fail "18 not healthy after the move"
[ "$(client app synapse-pw synapse synapse 'show server_version_num' | cut -c1-2)" = 18 ] || fail "not 18"
[ "$(client app synapse-pw synapse synapse "select count(*) || ',' || sum((body->>'g')::int) from events")" = "20000,200010000" ] || fail "rows"
[ "$(client app synapse-pw synapse synapse "select string_agg(x, ',' order by x) from c")" = "B,a,é" ] || fail "C collation"
[ "$(client app mas-pw mas mas "select datcollate from pg_database where datname = 'mas'")" = C ] || fail "mas password or locale"
[ "$(client app pw-postgres_password postgres postgres 'show data_checksums')" = on ] || fail "checksums or superuser password"
client app wrong synapse synapse 'select 1' >/dev/null 2>&1 && fail "a wrong password logged in"
[ "$(stat -c %u "$work/app/pgdata/18/docker")" = "$(id -u)" ] || fail "18 data not the target user's"
[ -s "$work/app/pgdata/pgdata/PG_VERSION" ] || fail "17 data gone"

# 4. Rollback: the previous catalog's 17 starts on its untouched data.
compose app down -t 30 >/dev/null 2>&1
use app old
{ compose app up -d postgres >/dev/null 2>&1 && healthy app; } || fail "17 no longer starts on its data"
[ "$(compose app exec -T postgres psql -U synapse -d synapse -XAtc 'select count(*) from events')" = 20000 ] || fail "17 data changed"
compose app down -t 30 >/dev/null 2>&1

# 5. Rerun: nothing dumped or restored again.
use app new
{ compose app up -d postgres >/dev/null 2>&1 && healthy app; } || fail "18 did not restart"
grep -q 'already done' <<<"$(compose app logs postgres-17-dump 2>&1)" || fail "rerun dumped again"
! grep -q 'postgres-17-to-18: restored' <<<"$(compose app logs postgres 2>&1)" || fail "rerun restored again"
compose app down -t 30 >/dev/null 2>&1

# 6. A dump that fails to restore never yields a cluster, on any number of starts.
in_app app 'mv /v/pgdata/18/docker /v/pgdata/18/docker.saved && cp /v/pgdata/upgrade-from-17/dump.sql /v/dump.good && echo "THIS IS NOT SQL;" >> /v/pgdata/upgrade-from-17/dump.sql'
# Postgres restarts unless stopped: watch it fail for a while, then once more after a stop.
for _ in 1 2; do
  compose app up -d postgres >/dev/null 2>&1 || true
  for _ in $(seq 1 20); do
    [ ! -e "$work/app/pgdata/18/docker" ] || fail "bad dump produced a cluster"
    sleep 1
  done
  grep -q 'THIS IS NOT SQL' <<<"$(compose app logs postgres 2>&1)" || fail "the bad dump was not tried"
  compose app stop -t 30 >/dev/null 2>&1
done
compose app down -t 30 >/dev/null 2>&1
in_app app 'mv /v/dump.good /v/pgdata/upgrade-from-17/dump.sql && mv /v/pgdata/18/docker.saved /v/pgdata/18/docker'

# 7. A fresh install: nothing to move, init.sql runs.
proj=${id}fresh
appdir fresh; use fresh new
{ compose fresh up -d postgres >/dev/null 2>&1 && healthy fresh; } || fail "fresh 18 did not start"
grep -q 'nothing to move' <<<"$(compose fresh logs postgres-17-dump 2>&1)" || fail "fresh install did not skip the dump"
[ -s "$work/fresh/pgdata/18/docker/PG_VERSION" ] && [ ! -e "$work/fresh/pgdata/pgdata" ] || fail "fresh data directory"
[ "$(client fresh synapse-pw synapse synapse 'select 1')" = 1 ] || fail "fresh init.sql"

# 8. The 17 backup restores on 18 through the manifest's restore_command, as restore runs it.
proj=${id}restored
appdir restored; use restored new
{ compose restored up -d postgres >/dev/null 2>&1 && healthy restored; } || fail "restore target did not start"
eval "compose restored exec -T postgres $restore" < "$work/backup-17.sql" || fail "restore_command on 18"
[ "$(client restored synapse-pw synapse synapse 'select count(*) from events')" = 20000 ] || fail "restored rows"
echo "PASS: kymessage PostgreSQL 17 to 18 (up over running 17, live-17 refusal, passwords, rollback, rerun, bad dump, fresh, 17 backup on 18)"
