#!/usr/bin/env bash
# twin-check.sh: fail when a Go script changed after its Rust twin recorded it
# (make rs-twin-check, ADR 0021 §11). Main loop only.
# A twin testdata/script/rust/<name>.txtar starts with the line
#   # Rust twin of testdata/script/<name>.txtar (Go blob <sha>).
# where <sha> is `git hash-object testdata/script/<name>.txtar`. Twins without the header
# are skipped (the R1 upgrade_flow twin until R2.18 adds it).
set -euo pipefail
repo=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo"
status=0
checked=0
for twin in testdata/script/rust/*.txtar; do
  [[ -f $twin ]] || continue
  first=$(head -1 "$twin")
  re='^# Rust twin of (testdata/script/[^ ]+\.txtar) \(Go blob ([0-9a-f]+)\)\.$'
  if [[ ! $first =~ $re ]]; then
    continue
  fi
  go_file=${BASH_REMATCH[1]} want=${BASH_REMATCH[2]}
  checked=$((checked + 1))
  if [[ ! -f $go_file ]]; then
    echo "twin-check: $twin names $go_file, which does not exist"; status=1; continue
  fi
  got=$(git hash-object "$go_file")
  if [[ $got != "$want" ]]; then
    echo "twin-check: $go_file changed after its twin $twin (recorded $want, now $got)"; status=1
  fi
done
if [[ $status -ne 0 ]]; then
  echo "Try: port the Go change into the twin, then write the new blob in its first line"
  exit 1
fi
echo "twin-check: $checked twins are current"
