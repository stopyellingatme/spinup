#!/usr/bin/env bash
# app-check.sh <set> [resource]: the compile proof of one generated app (make rs-app-check,
# ADR 0021 §4). Main loop only. It never needs Postgres or the network.
#   1. Render the app with $SPINUP_BIN and the kit patch ($SPINUP_KIT_DIR).
#   2. With resource set: g resource todo (no database: the runner applies the plan), then
#      copy the .sqlx/ files of the reference app.
#   3. rustfmt --check on every api/**/*.rs.
#   4. cargo check --offline --locked --all-targets in api/ with SQLX_OFFLINE=true.
# Environment: SPINUP_BIN, SPINUP_PACKS_SRC, SPINUP_STACK_SRC, SPINUP_KIT_DIR, CARGO_TARGET_DIR.
set -euo pipefail
set_name=${1:-}
resource=${2:-}
repo=$(cd "$(dirname "$0")/../.." && pwd)
fail() { echo "app-check: $1" >&2; echo "Try: $2" >&2; exit 1; }
: "${SPINUP_BIN:?app-check: SPINUP_BIN is empty}"
: "${SPINUP_KIT_DIR:?app-check: SPINUP_KIT_DIR is empty}"
case $set_name in
  small) flags=(--api-only --module example.com/small) ;;
  embed) flags=(--module example.com/embed --frontend-mode embed) ;;
  full) flags=(--module github.com/example/full --modules ci,auth,jobs,mail --auth-oidc) ;;
  auth) flags=(--module example.com/auth --modules auth) ;;
  jobs) flags=(--module example.com/jobs --modules jobs) ;;
  mail) flags=(--module example.com/mail --modules mail) ;;
  auth-mail) flags=(--module example.com/authmail --modules auth,mail) ;;
  *) fail "unknown set ${set_name:-<empty>}" "use SET=small, embed, full, auth, jobs, mail, or auth-mail" ;;
esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
app=$work/app
(cd "$work" && SPINUP_KIT_REPLACE=$SPINUP_KIT_DIR "$SPINUP_BIN" new app "${flags[@]}" --no-git --non-interactive) \
  || fail "spinup new failed for the $set_name set" "run the command of app-check.sh by hand"
if [[ -n $resource ]]; then
  (cd "$app" && env -u DATABASE_URL -u DATABASE_URL_FILE "$SPINUP_BIN" g resource todo title:string done:bool 'due:date?' --non-interactive) \
    || fail "g resource todo failed" "check the generator output"
  mkdir -p "$app/api/.sqlx"
  cp "$repo"/testdata/reference/todo-rs/.sqlx/*.json "$app/api/.sqlx/"
fi
[[ -f $app/api/src/main.rs ]] || fail "the $set_name app has no api/src/main.rs" "run this check after R2.18"

bad=()
while IFS= read -r -d '' f; do
  rustfmt +"${RUSTUP_TOOLCHAIN:-1.99.1}" --edition 2024 --check "$f" > /dev/null 2>&1 || bad+=("${f#"$app"/}")
done < <(find "$app/api" -name target -prune -o -name '*.rs' -print0)
if [[ ${#bad[@]} -gt 0 ]]; then
  printf 'app-check: not rustfmt-stable: %s\n' "${bad[@]}" >&2
  fail "rustfmt --check failed" "make the template output rustfmt-stable"
fi
(cd "$app/api" && SQLX_OFFLINE=true cargo check --offline --locked --all-targets) \
  || fail "cargo check failed for the $set_name set" "fix the pack or the kit, then run make rs-app-check SET=$set_name again"
echo "app-check: $set_name${resource:+ with g resource todo} compiles"
