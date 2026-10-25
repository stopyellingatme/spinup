#!/usr/bin/env bash
# parity.sh: runs the script tests with the Go binary and with the Rust binary, then prints
# one line for each script (make parity). The Rust run reads testdata/script/parity.txt
# (R1.0a harness). RUN=<glob> narrows both runs.
# Environment: SPINUP_BIN (the Rust binary), SPINUP_PACKS_SRC, SPINUP_STACK_SRC (set by make).
set -uo pipefail

repo=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo" || exit 1
logs=$(mktemp -d)
trap 'rm -rf "$logs"' EXIT

run() { # name, extra env...
  local name=$1
  shift
  env "$@" SPINUP_SCRIPT_RUN="${RUN:-}" go test ./internal/testutil/scripttest -run 'TestScripts' -count=1 -timeout 30m -v > "$logs/$name.log" 2>&1
}
rust_bin=${SPINUP_BIN:?set SPINUP_BIN to the Rust binary}
run go -u SPINUP_BIN
go_status=$?
run rust SPINUP_BIN="$rust_bin"
rust_status=$?

result() { # log, script name
  local line
  line=$(grep -E -- "--- (PASS|FAIL|SKIP): TestScripts/$2( |$)" "$1" | head -1)
  case $line in
    *"--- PASS"*) echo pass ;;
    *"--- FAIL"*) echo FAIL ;;
    *"--- SKIP"*) echo skip ;;
    *) echo "-" ;;
  esac
}
names=$(grep -hoE -- '--- (PASS|FAIL|SKIP): TestScripts/[^ ]+' "$logs/go.log" "$logs/rust.log" | sed 's#.*TestScripts/##' | sort -u)
printf '%-28s %-6s %s\n' script go rust
for name in $names; do
  printf '%-28s %-6s %s\n' "$name" "$(result "$logs/go.log" "$name")" "$(result "$logs/rust.log" "$name")"
done
if [[ $go_status -ne 0 || $rust_status -ne 0 ]]; then
  echo "parity: go exit $go_status, rust exit $rust_status"
  echo "Try: make script RUN=<name>, or make rs-script RUN=<name>"
  exit 1
fi
