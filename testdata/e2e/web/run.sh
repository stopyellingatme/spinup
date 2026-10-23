#!/usr/bin/env bash
# Browser smoke test of a generated app with the auth module.
# It builds the CLI, generates an app, starts the API and the web app, and runs Playwright.
# Usage: run.sh [--list] [extra playwright arguments]
# Set E2E_ONLINE=1 to install packages from the network (CI). The default is the offline store.
# Set E2E_INSTALL_BROWSER=1 to install the Chromium browser for Playwright (needs the network).
# Set E2E_KEEP=1 to keep the work directory.
# Needs: DATABASE_URL (a disposable database; run.sh empties its public schema when psql exists), Go, Node 24, pnpm, and python3.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "run.sh: DATABASE_URL is not set." >&2
  echo "  Try: DATABASE_URL=postgres://user:pass@127.0.0.1:5432/e2e?sslmode=disable $0" >&2
  exit 1
fi

export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"
export PATH="$repo/scripts/bin:/opt/homebrew/opt/node@24/bin:$PATH"

work="$(mktemp -d "${TMPDIR:-/tmp}/spinup-e2e.XXXXXX")"
pids=()
cleanup() {
  local status=$?
  for pid in ${pids[@]+"${pids[@]}"}; do
    kill "$pid" 2>/dev/null || true
  done
  for pid in ${pids[@]+"${pids[@]}"}; do
    wait "$pid" 2>/dev/null || true
  done
  if [[ $status -ne 0 ]]; then
    echo "run.sh: failed. Logs are in $work/logs" >&2
  elif [[ -z "${E2E_KEEP:-}" ]]; then
    rm -rf "$work"
  fi
}
trap cleanup EXIT
mkdir -p "$work/logs"

free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'
}

wait_for() {
  local url=$1 name=$2
  for _ in $(seq 1 120); do
    if curl -fsS -o /dev/null "$url"; then return 0; fi
    sleep 0.5
  done
  echo "run.sh: $name did not answer at $url within 60 seconds." >&2
  echo "  Try: read the logs in $work/logs" >&2
  return 1
}

echo "==> build the CLI"
(cd "$repo" && go build -o "$work/spinup" ./cmd/spinup)

echo "==> generate the app"
cd "$work"
SPINUP_KIT_REPLACE="$repo/kit" ./spinup new demo --module example.com/demo \
  --modules ci,auth --no-git --non-interactive >"$work/logs/new.log" 2>&1
cd demo

api_port="$(free_port)"
web_port="$(free_port)"
origin="http://127.0.0.1:$web_port"
api_url="http://127.0.0.1:$api_port"

cat >.env <<ENV
SPINUP_ENV=dev
PORT=$api_port
LOG_LEVEL=info
DATABASE_URL=$DATABASE_URL
PUBLIC_ORIGIN=$origin
ENV

# The test database must be empty, or a table from an earlier run would hide a migration change.
if command -v psql >/dev/null 2>&1; then
  echo "==> reset the test database"
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q \
    -c 'DROP SCHEMA public CASCADE' -c 'CREATE SCHEMA public' >"$work/logs/reset.log" 2>&1
fi

echo "==> generate the todo resource and migrate"
"$work/spinup" g resource todo title:string done:bool 'due:date?' >"$work/logs/resource.log" 2>&1
"$work/spinup" db migrate up >"$work/logs/migrate.log" 2>&1

echo "==> build the API"
(cd api && go build -o ../api.bin ./cmd/api)

echo "==> build the web app"
cd web
offline=(--offline)
if [[ -n "${E2E_ONLINE:-}" ]]; then offline=(); fi
pnpm install --frozen-lockfile ${offline[@]+"${offline[@]}"} >"$work/logs/pnpm-install.log" 2>&1
# adapter-node bakes PUBLIC_ORIGIN into the build (ADR 0002 rule 2).
PUBLIC_ORIGIN="$origin" API_URL="$api_url" pnpm build >"$work/logs/pnpm-build.log" 2>&1

if [[ -n "${E2E_INSTALL_BROWSER:-}" ]]; then
  echo "==> install Chromium"
  pnpm exec playwright install --with-deps chromium
fi

echo "==> copy the tests"
mkdir -p e2e
cp "$here/playwright.config.ts" e2e/
cp -R "$here/tests" e2e/

if [[ "${1:-}" == "--list" ]]; then
  shift
  pnpm exec playwright test --config e2e/playwright.config.ts --list "$@"
  exit 0
fi

echo "==> start the API on $api_url"
# exec, so the recorded PID is the API itself and cleanup stops it (a subshell PID would leave
# the API running).
(cd .. && PORT="$api_port" PUBLIC_ORIGIN="$origin" SPINUP_API_DOCS=true exec ./api.bin >"$work/logs/api.log" 2>&1) &
pids+=($!)
echo "==> start the web app on $origin"
# ADDRESS_HEADER and XFF_DEPTH are the production values of compose.yml. hooks.server.ts then sends
# the client address of a browser request to the API (ratelimit.spec.ts sends a public one). A
# request without the header gets no client address, and the hook warns once.
PORT="$web_port" HOST=127.0.0.1 PUBLIC_ORIGIN="$origin" API_URL="$api_url" \
  ADDRESS_HEADER=X-Forwarded-For XFF_DEPTH=1 node build >"$work/logs/web.log" 2>&1 &
pids+=($!)

wait_for "$api_url/healthz" "the API"
wait_for "$origin/healthz" "the web app"

echo "==> run Playwright"
# In production a reverse proxy sends /api to the Go API. Here the tests call the API directly.
E2E_BASE_URL="$origin" E2E_API_URL="$api_url" pnpm exec playwright test --config e2e/playwright.config.ts "$@"

# The generated app's own smoke test (web/playwright.config.ts, web/e2e/smoke.spec.ts), the
# suite that its e2e.yml runs in CI. Its config reads PUBLIC_ORIGIN.
echo "==> run the app's own Playwright smoke test"
PUBLIC_ORIGIN="$origin" pnpm exec playwright test e2e/smoke.spec.ts
