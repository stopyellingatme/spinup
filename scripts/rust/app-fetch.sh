#!/usr/bin/env bash
# app-fetch.sh [--offline]: fetch the crates of the api pack lock into CARGO_HOME (network).
# The generated-app tests build apps with cargo --offline --locked, so CI runs this first
# (make rs-app-fetch). It uses the manifest of the frozen full corpus app, the current pack
# lock, and a patch to the local kit. With --offline, it only proves that CARGO_HOME has them.
set -euo pipefail
repo=$(cd "$(dirname "$0")/../.." && pwd)
manifest=$repo/testdata/corpus-rs/0.1.0/full/tree/api
lock=$repo/crates/spinup-packs/overlay/api/files/api/Cargo.lock
kit=$repo/crates/spinup-kit
# On Windows (Git Bash), cargo needs a C:/ path in the TOML value.
if command -v cygpath > /dev/null; then kit=$(cygpath -m "$kit"); fi
net=false
if [[ ${1:-} == --offline ]]; then net=true; fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$manifest" "$work/api"
cp "$lock" "$work/api/Cargo.lock"
(cd "$work/api" && CARGO_NET_OFFLINE=$net cargo fetch --locked \
  --config "patch.crates-io.spinup-kit.path=\"$kit\"") || {
  echo "app-fetch: cargo cannot fetch the crates of the api pack lock" >&2
  echo "Try: make rs-pack-lock, then run app-fetch.sh again" >&2
  exit 1
}
echo "app-fetch: the crates of $(grep -c '^name = ' "$lock") lock entries are in CARGO_HOME"
