#!/usr/bin/env bash
# cli-graph-check.sh: the CLI graph has no async runtime, no aws-lc, and no system TLS
# (ADR 0021 §9). cargo tree -p resolves the features of the binary only. Main loop only.
set -euo pipefail
out=$(cargo tree --locked --offline -p spinup -e normal,build --target all --prefix none --format '{p}')
bad=$(grep -E '^(tokio|aws-lc-rs|aws-lc-sys|openssl|openssl-sys|native-tls) ' <<<"$out" | sort -u || true)
if [[ -n $bad ]]; then
  echo "cli-graph-check: the spinup binary depends on:"; echo "$bad"
  echo "Try: cargo tree -p spinup -i <crate> shows the path; remove that edge"
  exit 1
fi
echo "cli-graph-check: the spinup binary has no tokio, aws-lc, openssl, or native-tls"
