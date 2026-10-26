#!/usr/bin/env bash
# lock-align.sh: move the root Cargo.lock back to the versions of the R0.9 spike lock
# (ADR 0021 §12). Main loop only. It needs the network (cargo update), so it runs in the
# network step of a pre-seed, after the root lock resolves the new crates.
# For each package that both locks hold, a root version that differs from a spike version in
# the same semver range moves to the spike version with `cargo update --precise`. The script
# prints each change and fails when a same-range difference remains.
set -euo pipefail
repo=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo"
spike=${SPIKE_LOCK:-spikes-rs/r0-09-ref-web/Cargo.lock}
diffs() { # prints "<name> <root version> <spike version>" for each same-range difference
  python3 -I - "$repo/Cargo.lock" "$repo/$spike" <<'PY'
import sys, tomllib
def versions(path):
    out = {}
    with open(path, "rb") as f:
        for p in tomllib.load(f).get("package", []):
            out.setdefault(p["name"], set()).add(p["version"])
    return out
def key(v):
    core = v.split("+")[0].split("-")[0]
    parts = [int(x) for x in core.split(".")]
    if parts[0] > 0:
        return (parts[0],)
    if parts[1] > 0:
        return (0, parts[1])
    return (0, 0, parts[2])
root, spike = versions(sys.argv[1]), versions(sys.argv[2])
for name in sorted(root.keys() & spike.keys()):
    for r in sorted(root[name] - spike[name]):
        for s in sorted(spike[name] - root[name]):
            if key(r) == key(s):
                print(name, r, s)
                break
PY
}
changed=0
err=$(mktemp)
trap 'rm -f "$err"' EXIT
while read -r name have want; do
  [[ -n $name ]] || continue
  echo "lock-align: $name $have -> $want"
  if CARGO_NET_OFFLINE=${CARGO_NET_OFFLINE:-false} cargo update -p "$name@$have" --precise "$want" > /dev/null 2>"$err"; then
    changed=$((changed + 1))
  else
    echo "lock-align: cargo update of $name to $want failed:"; sed 's/^/  /' "$err"
  fi
done < <(diffs)
left=$(diffs)
if [[ -n $left ]]; then
  echo "lock-align: these packages still differ from $spike in the same semver range:"
  echo "$left" | sed 's/^/  /'
  echo "Try: check the pin of $(echo "$left" | head -1 | cut -d' ' -f1) in [workspace.dependencies]"
  exit 1
fi
echo "lock-align: $changed changes; the root lock agrees with $spike in every shared semver range"
