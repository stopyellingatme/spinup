#!/usr/bin/env bash
# Regenerate the Go lockfiles that the api template pack embeds, from stackversions.yaml.
# Only the main loop runs this, during a pre-seed commit or a milestone gate. It needs network
# access (proxy.golang.org). Lanes never run it.
#
# Outputs, in internal/templates/packs/api/files/api/:
#   tools.mod.tmpl, tools.sum   dev tools for `go tool -modfile=tools.mod` (sqlc, oapi-codegen, goose, air)
#   go.mod.tmpl, go.sum         the generated API module: the kit, plus every module the kit and the scaffold need
#
# Modes:
#   seed (default)  The app module imports every kit package plus the third-party packages in
#                   kit/internal/deps. Use it before the api scaffold exists.
#   app             BUMP_STACK_MODE=app. Render a real app with `spinup new` (SPINUP_KIT_REPLACE pointing at
#                   kit/) and run `go mod tidy` in its api/ directory. Use it at a milestone gate, once
#                   `new` works, so go.mod lists exactly what the scaffold imports.
set -euo pipefail

export GOTOOLCHAIN=local GOFLAGS=
export GOPROXY="${BUMP_STACK_GOPROXY:-https://proxy.golang.org,direct}"

root=$(git rev-parse --show-toplevel)
sv="$root/internal/stackversions/stackversions.yaml"
out="$root/internal/templates/packs/api/files/api"
mode="${BUMP_STACK_MODE:-seed}"
kit_module=github.com/stopyellingatme/spinup/kit
placeholder=spinup.invalid/app/api

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$out"

# pin <section> <key> prints the version pinned for <key> in a top-level <section> of stackversions.yaml.
pin() {
  awk -v sec="$1:" -v key="$2:" '
    /^[^ #]/ { insec = ($1 == sec) }
    insec && $1 == key { print $2; found = 1; exit }
    END { if (!found) exit 1 }' "$sv" || {
    echo "bump-stack: no pin for $2 in section $1 of $sv" >&2
    echo "Try: add it to internal/stackversions/stackversions.yaml" >&2
    exit 1
  }
}

# The kit version that generated apps require (ADR 0018). It comes from the kit pin in
# stackversions.yaml. BUMP_STACK_KIT_VERSION overrides it for one run (a trial of a new tag).
# The pack renders the require line with the template expression, so the literal version never
# stays in go.mod.tmpl (see finish).
kit_version="${BUMP_STACK_KIT_VERSION:-$(pin go_modules "$kit_module")}"

# finish <dir> <go.mod target> <go.sum target> writes the module files as pack sources.
# The module line becomes a template, and the local kit replace directive is dropped.
# The kit require line keeps the template expression for the kit pin, not the literal version.
finish() {
  (cd "$1" && go mod edit -dropreplace="$kit_module" 2>/dev/null || true)
  sed -E \
    -e "s#^module .*#module [[ .module ]]/api#" \
    -e "s#^([[:space:]]*${kit_module//./\\.}) v[^[:space:]]+#\\1 [[ gomod \"${kit_module}\" ]]#" \
    "$1/go.mod" > "$2"
  if [[ "$2" == */go.mod.tmpl ]] && ! grep -q "^[[:space:]]*${kit_module//./\\.} \[\[ gomod " "$2"; then
    echo "bump-stack: $2 has no kit require line with the gomod expression" >&2
    echo "Try: restore '$kit_module [[ gomod \"$kit_module\" ]]' in the require block by hand" >&2
    exit 1
  fi
  cp "$1/go.sum" "$3"
  if grep -q '^toolchain ' "$2"; then
    echo "bump-stack: $2 has a toolchain line; a pinned module needs a newer Go than 1.26.0" >&2
    exit 1
  fi
}

# --- tools.mod ---------------------------------------------------------------------------------
tools=(
  github.com/sqlc-dev/sqlc/cmd/sqlc
  github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen
  github.com/pressly/goose/v3/cmd/goose
  github.com/air-verse/air
)
mkdir "$work/tools"
(
  cd "$work/tools"
  go mod init "$placeholder" >/dev/null 2>&1
  go mod edit -go=1.26.0
  for t in "${tools[@]}"; do
    go get -tool "$t@$(pin go_tools "$t")"
  done
  go mod tidy
)
finish "$work/tools" "$out/tools.mod.tmpl" "$out/tools.sum"
echo "bump-stack: wrote tools.mod.tmpl and tools.sum"

# --- go.mod (the API module) ---------------------------------------------------------------------
# Modules that generated code imports but the base scaffold does not (ADR 0007 §6: the lockfiles
# are a superset, so an app builds offline after any generator runs).
# Packages, not modules: a package in a pinned module gets the module's pin, and google/uuid
# (used by runtime/types for format: uuid) comes in at the version the runtime requires.
app_extra=(github.com/oapi-codegen/runtime github.com/oapi-codegen/runtime/types github.com/google/uuid
  github.com/riverqueue/river github.com/riverqueue/river/riverdriver/riverpgxv5
  github.com/alexedwards/scs/v2 github.com/alexedwards/scs/pgxstore golang.org/x/crypto/argon2
  go.opentelemetry.io/otel go.opentelemetry.io/otel/sdk/trace go.opentelemetry.io/otel/sdk/metric
  go.opentelemetry.io/otel/sdk/log go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
  go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp
  go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp go.opentelemetry.io/contrib/bridges/otelslog
  go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp
  go.opentelemetry.io/contrib/instrumentation/runtime github.com/exaring/otelpgx
  github.com/wneessen/go-mail github.com/coreos/go-oidc/v3/oidc golang.org/x/oauth2)

# add_app_extra <api dir> requires and imports every app_extra module, so tidy keeps them.
add_app_extra() {
  mkdir -p "$1/internal/spinupdeps"
  {
    echo "package spinupdeps"
    echo
    echo "import ("
    for m in "${app_extra[@]}"; do printf '\t_ "%s"\n' "$m"; done
    echo ")"
  } > "$1/internal/spinupdeps/deps.go"
  for m in "${app_extra[@]}"; do
    if v=$(pin go_modules "$m" 2>/dev/null); then (cd "$1" && go get "$m@$v"); fi
  done
}

# Every module pack, so the app-mode render turns on all of them (ADR 0007 §6).
all_modules=$(cd "$root/internal/templates/packs" && ls -d */ | tr -d / | grep -vE '^(_.*|base|api|web)$' | paste -sd, -)
case "$mode" in
  seed)
    app="$work/app"
    mkdir -p "$app/internal/deps"
    (
      cd "$app"
      go mod init "$placeholder" >/dev/null 2>&1
      go mod edit -go=1.26.0 -require="$kit_module@$kit_version" -replace="$kit_module=$root/kit"
      {
        echo "package deps"
        echo
        echo "import ("
        (cd "$root/kit" && go list ./... | grep -v '/internal/') | sed 's/.*/\t_ "&"/'
        grep -h '^\s*_ "' "$root"/kit/internal/deps/deps_m*.go | grep -v "$kit_module"
        echo ")"
      } > internal/deps/deps.go
      add_app_extra "$app"
      go mod tidy
    )
    ;;
  app)
    app="$work/new/demo/api"
    (
      cd "$root"
      go build -o "$work/spinup" ./cmd/spinup
      cd "$work" && mkdir new && cd new
      SPINUP_KIT_REPLACE="$root/kit" "$work/spinup" new demo --module "${placeholder%/api}" \
        --modules "$all_modules" --non-interactive --no-git
      add_app_extra "$app"
      cd "$app"
      go mod tidy
    )
    ;;
  *)
    echo "bump-stack: unknown BUMP_STACK_MODE=$mode" >&2
    echo "Try: BUMP_STACK_MODE=seed or BUMP_STACK_MODE=app" >&2
    exit 2
    ;;
esac
finish "$app" "$out/go.mod.tmpl" "$out/go.sum"
echo "bump-stack: wrote go.mod.tmpl and go.sum ($mode mode, kit $kit_version)"

# --- web: pnpm-lock.yaml ---------------------------------------------------------------------------
# Render the web package.json with the CLI, lock it with pnpm (through scripts/bin/pnpm), and
# install once so the pnpm store holds every package for offline lanes.
export PATH="$root/scripts/bin:/opt/homebrew/opt/node@24/bin:$PATH"
web_out="$root/internal/templates/packs/web/files/web"
if [[ -f "$web_out/package.json.tmpl" ]]; then
  (
    cd "$root" && go build -o "$work/spinup-web" ./cmd/spinup
    mkdir -p "$work/webapp" && cd "$work/webapp"
    "$work/spinup-web" new demo --module "${placeholder%/api}" --modules "$all_modules" --non-interactive --no-git >/dev/null
    cd demo/web
    # Start from the current lockfile, so new packages do not re-resolve the others.
    # BUMP_STACK_FRESH=1 re-resolves every package (a deliberate bump).
    if [[ "${BUMP_STACK_FRESH:-0}" == 1 ]]; then rm -f pnpm-lock.yaml; fi
    pnpm install --lockfile-only
    cp pnpm-lock.yaml "$web_out/pnpm-lock.yaml"
    pnpm install --frozen-lockfile
  )
  echo "bump-stack: wrote web/pnpm-lock.yaml and filled the pnpm store"

  # --- web: bun.lock (ADR 0014 §3) ---------------------------------------------------------------
  bun_bin="${BUN:-bun}"
  if command -v "$bun_bin" >/dev/null 2>&1; then
    (
      mkdir -p "$work/bunapp" && cd "$work/bunapp"
      "$work/spinup-web" new demo --module "${placeholder%/api}" --modules "$all_modules" --package-manager bun --non-interactive --no-git >/dev/null
      cd demo/web
      if [[ "${BUMP_STACK_FRESH:-0}" == 1 || ! -s bun.lock || "$(head -c 2 bun.lock)" == "{}" ]]; then rm -f bun.lock; fi
      "$bun_bin" install --lockfile-only
      cp bun.lock "$web_out/bun.lock"
      "$bun_bin" install --frozen-lockfile >/dev/null
    )
    echo "bump-stack: wrote web/bun.lock and filled the bun cache"
  else
    echo "bump-stack: bun is not on PATH; web/bun.lock is unchanged" >&2
  fi
fi
