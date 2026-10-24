#!/usr/bin/env bash
# tools-check.sh: checks the Rust tool versions of docs/design/rust-plan.md §3.
# REQUIRED tools fail the check. LATER tools are needed only by later R0 spikes
# (R0.7, R0.11 to R0.14), so a missing one prints a warning.
set -uo pipefail
status=0
have() { command -v "$1" > /dev/null 2>&1; }
ver() { "$@" 2>/dev/null | head -1; }

if rustc +1.99.1 --version 2>/dev/null | grep -q '^rustc 1\.99\.1 '; then
  echo "ok   rust 1.99.1"
else
  echo "FAIL rust 1.99.1 is not installed (rustup toolchain install 1.99.1)"
  status=1
fi
for c in rustfmt clippy-driver; do
  if rustup run 1.99.1 "$c" --version > /dev/null 2>&1; then echo "ok   $c (1.99.1)"; else echo "FAIL $c (1.99.1)"; status=1; fi
done
for t in cargo-nextest sqlx; do
  if have "$t"; then echo "ok   $t: $(ver "$t" --version)"; else echo "FAIL $t"; status=1; fi
done
for t in sccache cargo-deny bacon watchexec cargo-zigbuild cargo-auditable zig goreleaser syft cosign; do
  if have "$t"; then echo "ok   $t: $(ver "$t" --version)"; else echo "warn $t is missing (needed later in R0)"; fi
done
exit $status
