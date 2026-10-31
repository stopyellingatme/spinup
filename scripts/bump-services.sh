#!/usr/bin/env bash
# CI and the main loop only. Lock refreshes need Docker and network access.
# Reports need curl, jq, and network access. Dry runs need none of these tools.
# Usage: scripts/bump-services.sh report | lock [--dry-run]
# BUMP_SERVICES_DRY_RUN=1 also selects a dry run.
# BUMP_SERVICES_FRESH=1 refreshes transitive dependencies from the current pins.
set -Eeuo pipefail

fail() {
  printf 'bump-services: %s\nTry: %s\n' "$1" "$2" >&2
  exit "${3:-1}"
}

trap 'fail "command failed at line $LINENO" "check the tools and registry access, then repeat the command"' ERR

mode=${1:-}
dry_run=${BUMP_SERVICES_DRY_RUN:-0}
case "$mode" in
  report) [[ $# == 1 ]] || fail 'report takes no arguments' 'scripts/bump-services.sh report' 2 ;;
  lock)
    if [[ $# == 2 && $2 == --dry-run ]]; then
      dry_run=1
    elif [[ $# != 1 ]]; then
      fail 'invalid lock arguments' 'scripts/bump-services.sh lock [--dry-run]' 2
    fi
    ;;
  *) fail 'expected report or lock' 'scripts/bump-services.sh report | lock [--dry-run]' 2 ;;
esac

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
sv="$root/internal/stackversions/stackversions.yaml"
templates="$root/internal/gen/service/templates"
[[ -r "$sv" ]] || fail 'cannot read stackversions.yaml' 'restore internal/stackversions/stackversions.yaml'

# Read the scalar maps used for package pins, including quoted npm names.
pins() {
  awk -v section="$1:" '
    /^[^ #]/ { active = ($1 == section) }
    active && /^  [^ #]/ {
      line = $0
      sub(/^[[:space:]]+/, "", line)
      sub(/[[:space:]]+#.*/, "", line)
      colon = index(line, ":")
      name = substr(line, 1, colon - 1)
      version = substr(line, colon + 1)
      gsub(/["[:space:]]/, "", name)
      gsub(/["[:space:]]/, "", version)
      print name "\t" version
    }' "$sv"
}

# Read an inline image map without requiring a YAML tool or a Go binary.
image_ref() {
  local fields image tag digest
  fields=$(awk -v key="$1:" '
    /^[^ #]/ { active = ($1 == "images:") }
    active && $1 == key {
      sub(/^[^{]*\{[[:space:]]*/, "")
      sub(/[[:space:]]*\}.*/, "")
      count = split($0, fields, ",")
      for (i = 1; i <= count; i++) {
        value = fields[i]
        sub(/^[[:space:]]*/, "", value)
        name = value
        sub(/:.*/, "", name)
        sub(/^[^:]*:[[:space:]]*/, "", value)
        gsub(/["[:space:]]/, "", value)
        if (name == "image") image = value
        if (name == "tag") tag = value
        if (name == "digest") digest = value
      }
      print image "\t" tag "\t" digest
      exit
    }' "$sv")
  IFS=$'\t' read -r image tag digest <<< "$fields"
  [[ -n "$image" && -n "$tag" && "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] ||
    fail "missing or invalid image pin: $1" 'restore the image pin in internal/stackversions/stackversions.yaml'
  printf '%s:%s@%s\n' "$image" "$tag" "$digest"
}

report_packages() {
  local section=$1 kind=$2 base=$3 filter=$4 suffix=$5 name pinned latest same_major
  while IFS=$'\t' read -r name pinned; do
    # Disable curlrc and suppress registry diagnostics, which can contain credentials.
    # The `|| exit 1` keeps the ERR trap quiet in bash 3.2 (macOS /bin/bash), which runs the trap
    # inside a command substitution even when the substitution is an if condition.
    if ! latest=$(curl -q --fail --silent --location --connect-timeout 10 --max-time 30 \
      --user-agent spinup-bump-services --url "${base%/}/$name$suffix" 2>/dev/null |
      jq -er "$filter | select(type == \"string\" and test(\"^[0-9][0-9A-Za-z.+-]*$\"))" 2>/dev/null ||
      exit 1); then
      latest=unknown
    fi
    same_major=no
    if [[ "$latest" != unknown && "${pinned%%.*}" == "${latest%%.*}" ]]; then
      same_major=yes
    fi
    printf '| %s | %s | %s | %s | %s |\n' "$kind" "$name" "$pinned" "$latest" "$same_major"
  done < <(pins "$section")
}

report() {
  local tool name ref tag
  for tool in curl jq; do
    command -v "$tool" >/dev/null || fail "report requires $tool" "install $tool, then repeat the report"
  done
  printf '| kind | name | pinned | latest | same-major |\n'
  printf '| --- | --- | --- | --- | --- |\n'
  report_packages crates crate "${BUMP_SERVICES_CRATES_API:-https://crates.io/api/v1/crates}" '.crate.max_stable_version' ''
  report_packages python_packages python "${BUMP_SERVICES_PYPI_API:-https://pypi.org/pypi}" '.info.version' /json
  report_packages node_packages node "${BUMP_SERVICES_NPM_API:-https://registry.npmjs.org}" '.["dist-tags"].latest' ''
  for name in rust python uv node distroless-static-debian13; do
    ref=$(image_ref "$name")
    tag=${ref%@*}
    tag=${tag##*:}
    printf '| image | %s | %s | manual check | no |\n' "$name" "$tag"
  done
  printf '\nNewer image tags need a manual check. Unknown versions could not be read from the registry.\n'
}

run() {
  if [[ "$dry_run" == 1 ]]; then
    printf '%q' "$1"
    shift
    printf ' %q' "$@"
    printf '\n'
  else
    "$@"
  fi
}

work=
uv_container=
cleanup() {
  if [[ -n "$uv_container" ]]; then docker rm "$uv_container" >/dev/null 2>&1 || true; fi
  if [[ -n "$work" && "$dry_run" != 1 ]]; then rm -rf -- "$work"; fi
}
trap cleanup EXIT

lock() {
  local rust_ref python_ref uv_ref node_ref cargo_command=generate-lockfile
  local uv_args=(lock)
  rust_ref=$(image_ref rust)
  python_ref=$(image_ref python)
  uv_ref=$(image_ref uv)
  node_ref=$(image_ref node)
  if [[ ${BUMP_SERVICES_FRESH:-0} == 1 ]]; then
    cargo_command=update
    uv_args+=(--upgrade)
  fi
  if [[ "$dry_run" == 1 ]]; then
    work='<temporary-directory>'
  else
    command -v docker >/dev/null || fail 'lock requires Docker' 'install Docker, or use lock --dry-run'
    work=$(mktemp -d)
    mkdir -p "$work/rust/src" "$work/python" "$work/node"
    cp "$templates/rust/Cargo.toml" "$templates/rust/Cargo.lock" "$work/rust/"
    printf 'fn main() {}\n' > "$work/rust/src/main.rs"
    cp "$templates/python/pyproject.toml" "$templates/python/uv.lock" "$work/python/"
    cp "$templates/node/package.json" "$templates/node/pnpm-lock.yaml" "$work/node/"
    if [[ ${BUMP_SERVICES_FRESH:-0} == 1 ]]; then rm "$work/node/pnpm-lock.yaml"; fi
  fi
  run docker run --rm -v "$work/rust:/work" -w /work "$rust_ref" cargo "$cargo_command"

  # The uv image has no shell. Extract its binary without starting the container.
  if [[ "$dry_run" == 1 ]]; then
    run docker create "$uv_ref" /uv --version
    run docker cp '<uv-container>:/uv' "$work/uv"
    run docker rm '<uv-container>'
  else
    uv_container=$(docker create "$uv_ref" /uv --version)
    run docker cp "$uv_container:/uv" "$work/uv"
    docker rm "$uv_container" >/dev/null
    uv_container=
  fi
  run docker run --rm -v "$work/python:/work" -v "$work/uv:/usr/local/bin/uv:ro" -w /work \
    -e UV_PYTHON_DOWNLOADS=never "$python_ref" uv "${uv_args[@]}"
  run docker run --rm -v "$work/node:/work" -w /work "$node_ref" \
    corepack pnpm install --lockfile-only --ignore-scripts
  if [[ "$dry_run" != 1 ]]; then
    # Publish only lockfiles after all three commands succeed. Manifests stay untouched.
    cp "$work/rust/Cargo.lock" "$templates/rust/Cargo.lock"
    cp "$work/python/uv.lock" "$templates/python/uv.lock"
    cp "$work/node/pnpm-lock.yaml" "$templates/node/pnpm-lock.yaml"
    printf 'bump-services: refreshed Cargo.lock, uv.lock, and pnpm-lock.yaml\n'
  fi
}

"$mode"
