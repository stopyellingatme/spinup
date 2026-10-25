#!/usr/bin/env bash
# pack-tree.sh <out>: writes the merged pack tree (converted packs plus the Rust-only
# overlay) to <out> (make rs-pack-tree). Main loop only. It follows the overlay merge rule of
# docs/design/rust-plan.md §1 and ADR 0020 §3:
#   1. An overlay file with the path of a converted file replaces it.
#   2. An overlay file with a new path is added.
#   3. overlay/<pack>/remove.txt lists converted paths to delete. A missing path fails.
#   4. overlay/<pack>/pack.d/<name>.toml lands as pack.d/~overlay-<name>.toml, so it loads
#      after the converted fragments, in file-name order.
#   5. An overlay cannot replace pack.toml.
# A template version (an archive, the cache, or SPINUP_TEMPLATES_DIR) holds this merged tree.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: pack-tree.sh <out>" >&2
  exit 2
fi
repo=$(cd "$(dirname "$0")/../.." && pwd)
packs=$repo/crates/spinup-packs/packs
overlay=$repo/crates/spinup-packs/overlay
out=$1
fail() { echo "pack-tree: $1" >&2; echo "Try: $2" >&2; exit 1; }

[[ -f $packs/ENGINE ]] || fail "$packs has no ENGINE marker" "make rs-convert-packs"
rm -rf "$out"
mkdir -p "$out"
# Copy the converted tree without the .gitkeep placeholder.
(cd "$packs" && find . -type f ! -path ./.gitkeep -print0 | while IFS= read -r -d '' f; do
  mkdir -p "$out/$(dirname "$f")"
  cp "$f" "$out/$f"
done)

[[ -d $overlay ]] || exit 0
for dir in "$overlay"/*/; do
  [[ -d $dir ]] || continue
  pack=$(basename "$dir")
  [[ -d $packs/$pack ]] || fail "overlay pack $pack has no converted pack" "remove overlay/$pack, or convert the pack"
  [[ ! -e $dir/pack.toml ]] || fail "overlay/$pack/pack.toml replaces pack.toml" "use a fragment in overlay/$pack/pack.d/"
  if [[ -f $dir/remove.txt ]]; then
    while IFS= read -r line || [[ -n $line ]]; do
      line=${line%%#*}
      line=$(echo "$line" | xargs)
      [[ -n $line ]] || continue
      [[ -f $out/$pack/$line ]] || fail "overlay/$pack/remove.txt lists $line, which does not exist" "remove the line from remove.txt"
      rm "$out/$pack/$line"
    done < "$dir/remove.txt"
  fi
  (cd "$dir" && find . -type f ! -path ./remove.txt -print0 | while IFS= read -r -d '' f; do
    f=${f#./}
    target=$f
    if [[ $f == pack.d/*.toml ]]; then
      target="pack.d/~overlay-${f#pack.d/}"
    fi
    mkdir -p "$out/$pack/$(dirname "$target")"
    cp "$f" "$out/$pack/$target"
  done)
done
echo "pack-tree: wrote $out"
