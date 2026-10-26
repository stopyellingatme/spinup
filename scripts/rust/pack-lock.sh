#!/usr/bin/env bash
# pack-lock.sh <spinup> <pack-tree> <stack> <kit-dir> <lock-out> [fetch]: make the union lock
# of the api pack (make rs-pack-lock, ADR 0021 §4). Main loop only.
#   1. Render three apps (full, small, embed) with the kit patch (SPINUP_KIT_REPLACE).
#   2. Give each api/ without src/main.rs a stub main (before R2.18 merges).
#   3. In full/api: start from the root Cargo.lock (its pins are the preferences), resolve,
#      and copy the lock to <lock-out>. With fetch, the network is allowed.
#   4. Proof: copy <lock-out> into the three apps and run cargo metadata --locked --offline.
# One lock fits every pack set: Cargo resolves a lock with all member features on, and
# Cargo.toml.tmpl declares every feature in every set.
set -euo pipefail
if [[ $# -lt 5 || $# -gt 6 ]]; then
  echo "usage: pack-lock.sh <spinup> <pack-tree> <stack> <kit-dir> <lock-out> [fetch]" >&2
  exit 2
fi
spinup=$1 tree=$2 stack=$3 kit=$4 out=$5 fetch=${6:-}
repo=$(cd "$(dirname "$0")/../.." && pwd)
fail() { echo "pack-lock: $1" >&2; echo "Try: $2" >&2; exit 1; }
[[ -x $spinup ]] || fail "$spinup is not a program" "make rs-pack-lock builds it"
[[ -d $kit ]] || fail "$kit is not the kit directory" "check RS_KIT_DIR"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
render() { # name, flags...
  local name=$1
  shift
  (cd "$work" && SPINUP_PACKS_SRC=$tree SPINUP_STACK_SRC=$stack SPINUP_KIT_REPLACE=$kit \
    "$spinup" new "$name" "$@" --no-git --non-interactive > "$work/$name.log" 2>&1) \
    || { cat "$work/$name.log" >&2; fail "spinup new $name failed" "run the command of pack-lock.sh by hand"; }
  if [[ ! -f $work/$name/api/src/main.rs ]]; then
    mkdir -p "$work/$name/api/src"
    printf 'fn main() {}\n' > "$work/$name/api/src/main.rs"
  fi
}
render full --module github.com/example/full --modules ci,auth,jobs,mail --auth-oidc
render small --api-only --module example.com/small
render embed --module example.com/embed --frontend-mode embed

cp "$repo/Cargo.lock" "$work/full/api/Cargo.lock"
if [[ -n $fetch ]]; then
  (cd "$work/full/api" && CARGO_NET_OFFLINE=false cargo metadata --format-version 1 > /dev/null) \
    || fail "cargo cannot resolve the full app" "check the pins of Cargo.toml.tmpl and the kit manifest"
  (cd "$work/full/api" && CARGO_NET_OFFLINE=false cargo fetch --locked) \
    || fail "cargo fetch failed in the full app" "check the network"
else
  (cd "$work/full/api" && cargo metadata --offline --format-version 1 > /dev/null) \
    || fail "cargo cannot resolve the full app offline" "make rs-pack-lock FETCH=1 (network)"
fi
cp "$work/full/api/Cargo.lock" "$out"

for name in full small embed; do
  cp "$out" "$work/$name/api/Cargo.lock"
  (cd "$work/$name/api" && cargo metadata --locked --offline --format-version 1 > /dev/null) \
    || fail "the pack lock does not fit the $name app" "make rs-pack-lock FETCH=1"
done
echo "pack-lock: wrote $out ($(grep -c '^name = ' "$out") packages); full, small, and embed resolve offline with it"
