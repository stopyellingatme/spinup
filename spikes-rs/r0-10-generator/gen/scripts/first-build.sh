#!/usr/bin/env bash
# first-build.sh WORKDIR: the R0.10 test of the first build after a new
# migration. It copies the spike workspace to WORKDIR, generates todo,
# migrates, prepares, and builds. Then it generates note and tries the
# post-step orders one by one. Each step prints its exit code, its time, and
# the first error line.
#
# It drops and creates the database spinup_r10 on 127.0.0.1:55433 (this
# unit's database) and leaves it migrated with todos and notes.
# Run it with the lane environment (offline cargo, CARGO_TARGET_DIR).
set -uo pipefail

work=${1:?usage: first-build.sh WORKDIR}
spike=$(cd "$(dirname "$0")/../.." && pwd)
admin=postgres://postgres:postgres@127.0.0.1:55433/postgres
export DATABASE_URL=postgres://postgres:postgres@127.0.0.1:55433/spinup_r10
gen=${CARGO_TARGET_DIR:?}/debug/gen

now() { python3 -c 'import time; print(time.time())'; }

# step NAME CMD...: runs CMD in the work copy and reports the result.
step() {
  local name=$1
  shift
  local t0 t1 rc
  t0=$(now)
  (cd "$work" && "$@") > "$work/.step.log" 2>&1
  rc=$?
  t1=$(now)
  printf '%-58s rc=%d  %6.1f s\n' "$name" "$rc" "$(python3 -c "print($t1 - $t0)")"
  if [[ $rc -ne 0 ]]; then
    grep -m1 -E '^error|error:|Error' "$work/.step.log" | cut -c1-220 | sed 's/^/    /'
    grep -m1 -E 'SQLX_OFFLINE|does not exist|cached data' "$work/.step.log" | cut -c1-220 | sed 's/^/    /'
  fi
}

rm -rf "$work"
mkdir -p "$work/todo-api/src" "$work/todo-api/migrations"
cp -R "$spike/Cargo.toml" "$spike/Cargo.lock" "$spike/.cargo" "$spike/kit" "$work/"
sed -i.bak 's/members = \["gen", "kit", "todo-api"\]/members = ["kit", "todo-api"]/' "$work/Cargo.toml"
rm "$work/Cargo.toml.bak"
cp "$spike/todo-api/Cargo.toml" "$work/todo-api/"
cp "$spike/todo-api/src/lib.rs" "$spike/todo-api/src/main.rs" "$work/todo-api/src/"

psql -q "$admin" -c 'drop database if exists spinup_r10' -c 'create database spinup_r10'

echo "== state 0: the app with todo, migrated, prepared, built"
step "gen resource todo" "$gen" resource todo title:string done:bool 'due:date?' 'priority:enum:low|normal|high' --app todo-api
step "sqlx migrate run" sqlx migrate run --source todo-api/migrations
step "cargo sqlx prepare --workspace" cargo sqlx prepare --workspace -- --offline
step "cargo build -p todo-api" cargo build -p todo-api --offline

echo "== gen resource note (migration 00003), then each order"
step "gen resource note" "$gen" resource note title:string 'body:text?' --app todo-api
step "1. build first (SQLX_OFFLINE=true, stale .sqlx/)" cargo build -p todo-api --offline
step "2. migrate through the app binary (cargo run -- migrate up)" cargo run -q -p todo-api --offline -- migrate up
step "3. prepare before migrate" cargo sqlx prepare --workspace -- --offline
step "4. build with SQLX_OFFLINE=false before migrate" env SQLX_OFFLINE=false cargo build -p todo-api --offline
step "5. migrate (sqlx-cli, does not build the app)" sqlx migrate run --source todo-api/migrations
step "6. build after migrate, before prepare" cargo build -p todo-api --offline
step "7. prepare after migrate" cargo sqlx prepare --workspace -- --offline
step "8. build after prepare" cargo build -p todo-api --offline
step "9. prepare --check" cargo sqlx prepare --workspace --check -- --offline

echo "== a query edit with SQLX_OFFLINE=true and the old .sqlx/"
sed -i.bak 's/FROM notes ORDER BY id LIMIT/FROM notes ORDER BY id DESC LIMIT/' "$work/todo-api/src/handlers/notes.rs"
rm "$work/todo-api/src/handlers/notes.rs.bak"
step "10. build after a query edit (stale .sqlx/)" cargo build -p todo-api --offline
step "11. prepare --check after a query edit" cargo sqlx prepare --workspace --check -- --offline
step "12. prepare, then build" bash -c 'cargo sqlx prepare --workspace -- --offline && cargo build -p todo-api --offline'
rm -f "$work/.step.log"
