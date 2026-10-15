#!/bin/sh
# Check a release before tagging. Use --tagged only in the tag workflow.
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$root"
exec go run ./scripts/release/check "$@"
