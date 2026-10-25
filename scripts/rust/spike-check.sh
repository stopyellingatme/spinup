#!/usr/bin/env bash
# spike-check.sh r0-NN: the check of every R0 unit (docs/design/rust-plan.md §4).
# It verifies the result file (five headings and one Verdict line), the verdict
# TOML (verdict plus the numeric fields of the unit row), and runs the spike's
# tests offline: cargo fmt --check, then cargo test --offline --locked.
# SPIKE_CHECK_NO_TEST=1 skips the cargo steps (the main loop uses it for a quick
# re-read of the documents).
set -euo pipefail

if [[ $# -ne 1 || ! $1 =~ ^r0-[0-9]{2}$ ]]; then
  echo "usage: spike-check.sh r0-NN" >&2
  exit 2
fi
id=$1
repo=$(cd "$(dirname "$0")/../.." && pwd)
fail() { echo "spike-check $id: $*" >&2; exit 1; }

shopt -s nullglob
dirs=("$repo"/spikes-rs/"$id"-*/)
results=()
for f in "$repo"/docs/design/rust-spike/"$id"-*.md; do results+=("$f"); done
verdicts=("$repo"/docs/design/rust-spike/"$id"-*.verdict.toml)
shopt -u nullglob

[[ ${#dirs[@]} -eq 1 ]] || fail "want one directory spikes-rs/$id-*/, found ${#dirs[@]}"
[[ ${#results[@]} -eq 1 ]] || fail "want one result file docs/design/rust-spike/$id-*.md, found ${#results[@]}"
[[ ${#verdicts[@]} -eq 1 ]] || fail "want one verdict file docs/design/rust-spike/$id-*.verdict.toml, found ${#verdicts[@]}"
dir=${dirs[0]%/}
result=${results[0]}
verdict_file=${verdicts[0]}

# Five headings, in this order, each exactly once.
want="Question Method Numbers Verdict Recommendation"
got=$(grep -E '^## ' "$result" | sed 's/^## //' | grep -xE 'Question|Method|Numbers|Verdict|Recommendation' | tr '\n' ' ' | sed 's/ $//')
[[ $got == "$want" ]] || fail "$result: headings are '$got', want '$want'"

# One Verdict line under the Verdict heading.
lines=$(grep -cE '^Verdict: (go|soft|no-go)$' "$result" || true)
[[ $lines -eq 1 ]] || fail "$result: want exactly one line 'Verdict: go|soft|no-go', found $lines"
md_verdict=$(awk '/^## Verdict/{on=1;next} /^## /{on=0} on && /^Verdict: /{print $2}' "$result")
[[ -n $md_verdict ]] || fail "$result: the Verdict line is not under '## Verdict'"

# The verdict TOML: verdict equals the Markdown, numeric fields are numbers.
case $id in
  r0-11) fields=(median_ms p90_ms) ;;
  r0-13) fields=(peak_disk_gb load1_max swap_gb) ;;
  *) fields=() ;;
esac
# ${fields[@]+...}: bash 3.2 (macOS) treats an empty array as unbound under set -u.
python3 - "$verdict_file" "$id" "$md_verdict" ${fields[@]+"${fields[@]}"} <<'PY' || fail "$verdict_file is not valid"
import sys, tomllib
path, spike, md, *fields = sys.argv[1:]
with open(path, "rb") as f:
    d = tomllib.load(f)
errs = []
if d.get("spike") != spike:
    errs.append(f"spike = {d.get('spike')!r}, want {spike!r}")
if d.get("verdict") not in ("go", "soft", "no-go"):
    errs.append(f"verdict = {d.get('verdict')!r}, want go, soft, or no-go")
elif d["verdict"] != md:
    errs.append(f"verdict = {d['verdict']!r}, but the result file says {md!r}")
for k in fields:
    v = d.get(k)
    if isinstance(v, bool) or not isinstance(v, (int, float)):
        errs.append(f"{k} = {v!r}, want a number")
for e in errs:
    print(f"{path}: {e}", file=sys.stderr)
sys.exit(1 if errs else 0)
PY

if [[ ${SPIKE_CHECK_NO_TEST:-} == 1 ]]; then
  echo "spike-check $id: documents ok (cargo steps skipped)"
  exit 0
fi

export RUSTUP_TOOLCHAIN=${RUSTUP_TOOLCHAIN:-1.99.1} CARGO_NET_OFFLINE=true
export CARGO_TARGET_DIR=${CARGO_TARGET_DIR:-$dir/target}
(cd "$dir" && cargo fmt --all --check) || fail "cargo fmt --check failed in $dir"
(cd "$dir" && cargo test --workspace --offline --locked) || fail "cargo test failed in $dir"
echo "spike-check $id: ok"
