#!/usr/bin/env bash
# doc-blocks.sh <page>: fail when a page of doc-parity.txt has no console block that the Rust
# binary runs (make rs-doctest, ADR 0021 §11). A block with <!-- doctest: only go --> or
# <!-- doctest: skip --> does not count. Main loop only.
set -euo pipefail
if [[ $# -ne 1 ]]; then
  echo "usage: doc-blocks.sh <page>" >&2
  exit 2
fi
page=$1
if [[ ! -f $page ]]; then
  echo "doc-blocks: $page does not exist"
  echo "Try: check the path in testdata/script/doc-parity.txt"
  exit 1
fi
count=$(awk '
  /^[[:space:]]*<!-- doctest:/ { if ($0 ~ /only go|doctest: skip/) excluded = 1; next }
  /^(```|~~~)[[:space:]]*console[[:space:]]*$/ { if (!excluded) n++; excluded = 0; next }
  { excluded = 0 }
  END { print n + 0 }
' "$page")
if [[ $count -eq 0 ]]; then
  echo "rs-doctest: $page has no console block for the Rust binary"
  echo "Try: add a block with <!-- doctest: only rust --> or a shared block"
  exit 1
fi
