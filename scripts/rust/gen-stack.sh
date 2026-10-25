#!/usr/bin/env bash
# gen-stack.sh: writes crates/spinup-stack/stackversions.yaml (schema 2) from the Go file
# internal/stackversions/stackversions.yaml (schema 1). Main loop only (rust-plan.md
# decision 11, ADR 0020 §3).
#
# The lines above the marker line are generated: the Go sections, unchanged, with
# `schema: 2`. The lines below the marker are the Rust sections (`rust`, `crates`,
# `cargo_tools`). The script keeps them as they are. Edit them by hand.
#
#   scripts/rust/gen-stack.sh          rewrite the generated part
#   scripts/rust/gen-stack.sh --check  fail when the generated part is stale
set -euo pipefail

repo=$(cd "$(dirname "$0")/../.." && pwd)
go_file=$repo/internal/stackversions/stackversions.yaml
rs_file=$repo/crates/spinup-stack/stackversions.yaml
marker='# ---- Rust sections (schema 2). The lines above are generated. Edit the lines below. ----'

check=0
case ${1:-} in
  --check) check=1 ;;
  "") ;;
  *) echo "usage: gen-stack.sh [--check]" >&2; exit 2 ;;
esac

if [[ ! -f $rs_file ]] || ! grep -qxF -- "$marker" "$rs_file"; then
  echo "gen-stack: $rs_file has no marker line" >&2
  echo "Try: restore the file from git; the Rust sections start at the marker line" >&2
  exit 1
fi
if ! grep -qx 'schema: 1' "$go_file"; then
  echo "gen-stack: $go_file is not schema 1" >&2
  echo "Try: update gen-stack.sh for the new Go schema" >&2
  exit 1
fi

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
{
  cat <<'HEADER'
# Pinned stack versions for the Rust CLI, schema 2 (ADR 0020 §3). Main loop only.
# Do not edit the lines above the marker line: scripts/rust/gen-stack.sh copies them from
# internal/stackversions/stackversions.yaml (the Go file, schema 1). Schema 2 is additive in
# R1: it keeps the Go sections, because the converted Go `api` pack reads them. The R2 gate
# removes the Go sections.
HEADER
  # The Go file without its leading comment block, with schema 2.
  awk 'started || (!/^#/ && !/^$/) { started = 1 } started' "$go_file" | sed 's/^schema: 1$/schema: 2/'
  echo
  echo "$marker"
  # The Rust sections: everything after the marker line.
  awk -v m="$marker" 'found { print } $0 == m { found = 1 }' "$rs_file"
} > "$tmp"

if [[ $check == 1 ]]; then
  if ! cmp -s "$tmp" "$rs_file"; then
    diff -u "$rs_file" "$tmp" | head -40 >&2 || true
    echo "gen-stack: $rs_file is stale" >&2
    echo "Try: scripts/rust/gen-stack.sh" >&2
    exit 1
  fi
  echo "gen-stack: $rs_file is current"
  exit 0
fi
cp "$tmp" "$rs_file"
echo "gen-stack: wrote $rs_file"
