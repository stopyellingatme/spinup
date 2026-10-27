#!/usr/bin/env bash
# script-log.sh <go-test-exit> <log> [script...]: print the result lines of a script run
# (make rs-script, make rs-app-gate). Main loop only.
# Fail when go test failed, when no script passed, or when a named script did not pass
# (a SKIP counts as a failure: a gate script that skips proves nothing).
set -uo pipefail
rc=$1 log=$2
shift 2
grep -E -- '--- (PASS|FAIL|SKIP): TestScripts/' "$log" || true
if [[ $rc -ne 0 ]]; then tail -80 "$log"; exit "$rc"; fi
if ! grep -q -- '--- PASS: TestScripts/' "$log"; then
  echo "rs-script: no script ran"
  echo "Try: list the script in testdata/script/parity.txt, or check RUN"
  exit 1
fi
for s in "$@"; do
  if ! grep -q -- "--- PASS: TestScripts/$s (" "$log"; then
    echo "rs-script: $s did not pass"
    echo "Try: install the tools that the script skips on (docker, curl, pnpm, Node 24), and list it in parity.txt"
    exit 1
  fi
done
