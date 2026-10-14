#!/usr/bin/env bash
# Regenerate the lockfiles (go.sum, tools.sum, pnpm-lock.yaml) that template packs embed.
# Only the main loop runs this, during a pre-seed commit. Lanes never run it.
#
# The full procedure needs `spinup new` (M1.4): render the default app into a temp dir,
# run `go mod tidy` and `pnpm install --lockfile-only` there, and copy the lockfiles back
# into each pack. Until a pack declares a go.mod or package.json input, there is nothing to do.
set -euo pipefail

packs=internal/templates/packs
if ! find "$packs" \( -name 'go.mod*' -o -name 'tools.mod*' -o -name 'package.json*' \) -print -quit 2>/dev/null | grep -q .; then
  echo "bump-stack: no pack declares Go or npm dependencies yet; nothing to do"
  exit 0
fi
echo "bump-stack: packs declare dependencies, but the render-and-lock procedure is not written yet (M1 pre-seed)" >&2
exit 1
