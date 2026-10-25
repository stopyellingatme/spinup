#!/usr/bin/env bash
# tools-check.sh: checks the Rust tool versions of docs/design/rust-plan.md §3 (make rs-tools-check).
# Main loop only. Keep the pins in step with `cargo_tools` in crates/spinup-stack/stackversions.yaml
# and with .github/workflows/rust.yml.
# EXACT tools must have the pinned version. MIN tools must have at least the version.
# LATER tools are needed only by later milestones (R2 to R4), so a missing one prints a warning.
set -uo pipefail
status=0
have() { command -v "$1" > /dev/null 2>&1; }
version_of() { "$@" 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1; }

exact() { # name, pinned version, command that prints the version
  local name=$1 want=$2 got
  shift 2
  got=$(version_of "$@")
  if [[ -z $got ]]; then
    echo "FAIL $name is missing (need $want)"; status=1
  elif [[ $got != "$want" ]]; then
    echo "FAIL $name $got (need exactly $want)"; status=1
  else
    echo "ok   $name $got"
  fi
}

minimum() { # name, minimum version, command that prints the version
  local name=$1 want=$2 got
  shift 2
  got=$(version_of "$@")
  if [[ -z $got ]]; then
    echo "FAIL $name is missing (need >= $want)"; status=1
  elif [[ $(printf '%s\n%s\n' "$want" "$got" | sort -V | head -1) != "$want" ]]; then
    echo "FAIL $name $got (need >= $want)"; status=1
  else
    echo "ok   $name $got"
  fi
}

exact rust 1.99.1 rustc +1.99.1 --version
for c in rustfmt clippy-driver; do
  if rustup run 1.99.1 "$c" --version > /dev/null 2>&1; then echo "ok   $c (1.99.1)"; else echo "FAIL $c (1.99.1)"; status=1; fi
done
if rustup component list --installed --toolchain 1.99.1 2>/dev/null | grep -q '^llvm-tools'; then
  echo "ok   llvm-tools (1.99.1)"
else
  echo "FAIL llvm-tools (rust-toolchain.toml lists it; make rs-cover needs it)"; status=1
fi
exact cargo-nextest 0.9.146 cargo nextest --version
exact cargo-llvm-cov 0.9.1 cargo llvm-cov --version
minimum cargo-deny 0.20.2 cargo deny --version
minimum sqlx-cli 0.9.0 sqlx --version

# The shared sections of the two stack files are in step (rust-plan.md decision 11).
repo=$(cd "$(dirname "$0")/../.." && pwd)
if "$repo/scripts/rust/gen-stack.sh" --check > /dev/null 2>&1; then
  echo "ok   crates/spinup-stack/stackversions.yaml is current"
else
  echo "FAIL crates/spinup-stack/stackversions.yaml is stale (run scripts/rust/gen-stack.sh)"; status=1
fi

for t in sccache bacon watchexec cargo-zigbuild cargo-auditable cargo-chef zig goreleaser syft cosign; do
  if have "$t"; then echo "ok   $t: $("$t" --version 2>/dev/null | head -1)"; else echo "warn $t is missing (needed later, R2 to R4)"; fi
done
exit $status
