#!/usr/bin/env bash
# Fill the Go module cache, the pnpm store, and the bun cache for the tests.
# Usage: scripts/ci/warm-cache.sh [go] [node]    (default: both)
#
# Some tests build or install a generated app with GOPROXY=off or "pnpm install --offline".
# They need every module and package of the generated app in the local caches first.
# This script downloads them. It never changes files in the repository.
#
# Environment:
#   SPINUP_PNPM_STORE  pnpm store directory (default: the pnpm default store, see "pnpm store path")
# Some tests run pnpm without --store-dir, so the default store must hold the packages.
# The pnpm metadata cache stays at the pnpm default (~/.cache/pnpm on Linux,
# ~/Library/Caches/pnpm on macOS), because the test harness does not pass a cache directory.
# In GitHub Actions the script writes the pnpm directories to GITHUB_ENV and
# adds scripts/bin to GITHUB_PATH for the next steps.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
store=${SPINUP_PNPM_STORE:-}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

die() {
  printf 'warm-cache: %s\nTry: %s\n' "$1" "$2" >&2
  exit 1
}

# warm_go_app downloads the modules of one generated app directory (go.mod and tools.mod).
# The kit may be unpublished at the version of the app. So the copy replaces the kit
# with the kit directory of this repository, as the tests do.
warm_go_app() { # directory with go.mod
  local dir=$1 copy
  copy=$(mktemp -d "$work/app.XXXXXX")
  cp "$dir/go.mod" "$copy/go.mod"
  [[ -f $dir/go.sum ]] && cp "$dir/go.sum" "$copy/go.sum"
  [[ -f $dir/tools.mod ]] && cp "$dir/tools.mod" "$copy/tools.mod"
  [[ -f $dir/tools.sum ]] && cp "$dir/tools.sum" "$copy/tools.sum"
  (
    cd "$copy"
    export GOWORK=off GOFLAGS=-mod=mod
    go mod edit -replace "github.com/stopyellingatme/spinup/kit=$root/kit"
    echo "warm-cache: go modules of ${dir#"$root"/}"
    go mod download
    if [[ -f tools.mod ]]; then
      if [[ -f tools.sum ]]; then
        # The tool module has its own sum file. The kit replace is not needed there.
        go mod download -modfile=tools.mod
      fi
    fi
  )
}

warm_go() {
  command -v go >/dev/null 2>&1 || die "go is not on PATH" "set up Go before this step"
  echo "warm-cache: go modules of the repository and the kit"
  (cd "$root" && go mod download)
  (cd "$root/kit" && go mod download)
  # Every generated app: the golden trees and the frozen upgrade corpora.
  # The plugin test project is not a generated app.
  local gomod
  while IFS= read -r gomod; do
    warm_go_app "$(dirname "$gomod")"
  done < <(find "$root/testdata/golden" "$root/testdata/corpus" -name go.mod -not -path '*/node_modules/*' | sort)
}

# warm_pnpm_lock installs one pnpm lockfile online. "pnpm fetch" fills only the store, but an
# offline install also checks the lockfile against the supply-chain policy, which needs the
# full package metadata in the cache. An online install writes both.
warm_pnpm_lock() { # directory with pnpm-lock.yaml and package.json
  local dir=$1 copy
  copy=$(mktemp -d "$work/web.XXXXXX")
  for file in package.json pnpm-lock.yaml pnpm-workspace.yaml .npmrc; do
    [[ -f $dir/$file ]] && cp "$dir/$file" "$copy/$file"
  done
  echo "warm-cache: pnpm store and metadata for ${dir#"$root"/}"
  (cd "$copy" && "$root/scripts/bin/pnpm" install --frozen-lockfile --ignore-scripts --silent --store-dir "$store")
}

# warm_pnpm_metadata fills the pnpm metadata cache for the packages that tests install
# without a lockfile. TestPageSchemaRoundTrip installs zod at the version of the golden web app.
warm_pnpm_metadata() {
  local zod copy
  zod=$(grep -o '"zod": *"[^"]*"' "$root/testdata/golden/web/web/package.json" | head -1 | sed 's/.*: *"\(.*\)"/\1/')
  [[ -n $zod ]] || die "zod is missing in testdata/golden/web/web/package.json" "update this script with the new package list"
  copy=$(mktemp -d "$work/meta.XXXXXX")
  printf '{"name":"warm","private":true,"type":"module","dependencies":{"zod":"%s"}}\n' "$zod" >"$copy/package.json"
  echo "warm-cache: pnpm metadata for zod $zod"
  (cd "$copy" && "$root/scripts/bin/pnpm" install --silent --store-dir "$store")
}

warm_bun() {
  command -v bun >/dev/null 2>&1 || {
    echo "warm-cache: bun is not on PATH; skip the bun cache"
    return
  }
  local lock copy
  while IFS= read -r lock; do
    copy=$(mktemp -d "$work/bun.XXXXXX")
    cp "$(dirname "$lock")/package.json" "$lock" "$copy/"
    echo "warm-cache: bun cache for ${lock#"$root"/}"
    (cd "$copy" && bun install --frozen-lockfile --ignore-scripts --silent)
  done < <(find "$root/testdata/golden" -name bun.lock -not -path '*/node_modules/*' | sort)
}

warm_node() {
  command -v node >/dev/null 2>&1 || die "node is not on PATH" "set up Node 24 before this step"
  command -v corepack >/dev/null 2>&1 || die "corepack is not on PATH" "use Node 24, which includes corepack"
  if [[ -z $store ]]; then
    # "pnpm store path" ends with the store version (for example /v11). --store-dir takes the
    # base directory, and pnpm adds the version itself.
    store=$("$root/scripts/bin/pnpm" store path)
    store=${store%/v[0-9]*}
  fi
  mkdir -p "$store"
  # This downloads the pinned pnpm binary into the corepack cache.
  "$root/scripts/bin/pnpm" --version
  local lock
  while IFS= read -r lock; do
    warm_pnpm_lock "$(dirname "$lock")"
  done < <(find "$root/testdata/golden" -name pnpm-lock.yaml -not -path '*/node_modules/*' | sort)
  warm_pnpm_metadata
  warm_bun
}

targets=("$@")
[[ ${#targets[@]} -gt 0 ]] || targets=(go node)
for target in "${targets[@]}"; do
  case "$target" in
    go) warm_go ;;
    node) warm_node ;;
    *) die "unknown target $target" "pass go, node, or nothing" ;;
  esac
done

if [[ -n ${GITHUB_ENV:-} ]]; then
  {
    if [[ -n $store ]]; then
      printf 'SPINUP_PNPM_STORE=%s\n' "$store"
      printf 'npm_config_store_dir=%s\n' "$store"
    fi
  } >>"$GITHUB_ENV"
fi
if [[ -n ${GITHUB_PATH:-} ]]; then
  printf '%s\n' "$root/scripts/bin" >>"$GITHUB_PATH"
fi
echo "warm-cache: done"
