#!/usr/bin/env bash
# Browser smoke test of a generated app with the auth module.
# It builds the CLI, generates an app, starts the API (and in node mode the web app), and runs Playwright.
# Usage: run.sh [--list] [extra playwright arguments]
# Set E2E_FRONTEND_MODE=node (the default) or embed (ADR 0039 section 10.6).
#   node:  the modules ci and auth, the todo resource, the Go API and the Node server on two ports.
#   embed: the modules ci, auth, flags, and live, the todo resource with --live, and one Go binary
#          (build tag embedweb) that serves the pages and the API on one port.
# Set E2E_ONLINE=1 to install packages from the network (CI). The default is the offline store.
# Set E2E_INSTALL_BROWSER=1 to install the requested Playwright browsers (needs the network).
# Set E2E_KEEP=1 to keep the work directory.
# Needs: DATABASE_URL (a disposable database; run.sh empties its public schema when psql exists), Go, Node 24, pnpm, and python3.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"

mode="${E2E_FRONTEND_MODE:-node}"
case "$mode" in
  node | embed) ;;
  *)
    echo "run.sh: E2E_FRONTEND_MODE=$mode is not known: the launcher supports node and embed." >&2
    echo "  Try: E2E_FRONTEND_MODE=node $0, or E2E_FRONTEND_MODE=embed $0" >&2
    exit 1
    ;;
esac
export E2E_FRONTEND_MODE="$mode"

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

# fail prints what failed and a Try: line, then stops the launcher.
fail() {
  echo "run.sh: $1" >&2
  echo "  Try: $2" >&2
  exit 1
}

# run_logged <log name> <what> <command...> runs the command with its output in logs/<log name>.log.
# When the command fails, the launcher stops with an error that names the step and the log.
run_logged() {
  local name=$1 what=$2
  shift 2
  "$@" >"$work/logs/$name.log" 2>&1 || fail "$what failed (mode $mode)." "read $work/logs/$name.log"
}

# free_port prints a free loopback port. It never gives 55432 (another project's Postgres) or a
# port that an argument names, so two calls give two distinct ports.
free_port() {
  python3 -I -c '
import socket, sys
taken = {55432} | {int(a) for a in sys.argv[1:]}
for _ in range(100):
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    if port not in taken:
        print(port)
        sys.exit(0)
sys.exit(1)' "$@" || fail "no free loopback port was found." "stop some local servers and run the launcher again"
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

echo "==> build the CLI (mode $mode)"
run_logged cli-build "go build of the CLI" bash -c "cd '$repo' && go build -o '$work/spinup' ./cmd/spinup"

echo "==> generate the app"
cd "$work"
if [[ "$mode" == embed ]]; then
  # mail adds the reset pages. The API sends each mail to SMTP_URL on a free loopback port, where
  # reset.spec.ts listens with a small SMTP sink in the Playwright process. No other process starts.
  modules=(--modules 'ci,auth,flags,live,mail' --frontend-mode embed)
  resource_flags=(--live)
else
  modules=(--modules 'ci,auth')
  resource_flags=()
fi
SPINUP_KIT_REPLACE="$repo/kit" run_logged new "spinup new" \
  ./spinup new demo --module example.com/demo "${modules[@]}" --no-git --non-interactive
cd demo

api_port="$(free_port)"
if [[ "$mode" == embed ]]; then
  # One Go binary serves the pages and the API, so all three origins are the same.
  origin="http://127.0.0.1:$api_port"
  smtp_port="$(free_port "$api_port")"
  smtp_url="smtp://127.0.0.1:$smtp_port"
else
  web_port="$(free_port "$api_port")"
  origin="http://127.0.0.1:$web_port"
fi
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
  run_logged reset "the reset of the test database" psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q \
    -c 'DROP SCHEMA public CASCADE' -c 'CREATE SCHEMA public'
fi

echo "==> generate the todo resource and migrate"
run_logged resource "spinup g resource todo" \
  "$work/spinup" g resource todo title:string done:bool 'due:date?' ${resource_flags[@]+"${resource_flags[@]}"}
run_logged migrate "spinup db migrate up" "$work/spinup" db migrate up

if [[ "$mode" == embed ]]; then
  # A public flag for the flags admin case of todos.spec.ts (the root load reads /api/flags).
  cat >>spinup.toml <<'TOML'

[flags.beta_banner]
description = "The beta banner"
default = false
public = true
owner = "ops@example.com"
expires = "2099-12-31"
TOML
  run_logged flags "spinup g flags" "$work/spinup" g flags
fi

offline=(--offline)
if [[ -n "${E2E_ONLINE:-}" ]]; then offline=(); fi

if [[ "$mode" == embed ]]; then
  echo "==> install, check, test, and build the web app"
  cd web
  run_logged pnpm-install "pnpm install" pnpm install --frozen-lockfile ${offline[@]+"${offline[@]}"}
  run_logged pnpm-check "pnpm run check" pnpm run check
  run_logged pnpm-test "pnpm run test" pnpm run test
  # PUBLIC_ORIGIN is part of the client build. The adapter copies web/build to api/internal/webdist/dist.
  PUBLIC_ORIGIN="$origin" run_logged pnpm-build "pnpm run build" pnpm run build
  [[ -f ../api/internal/webdist/dist/index.html ]] ||
    fail "the web build did not reach api/internal/webdist/dist." "check the adapter in web/vite.config.js and read $work/logs/pnpm-build.log"
  echo "==> build the API with the embedded web app"
  run_logged api-build "go build -tags embedweb of the API" \
    bash -c "cd ../api && go build -tags embedweb -o ../api.bin ./cmd/api"
  # An account with the admin role of the flags module (ADR 0024: the operator user command).
  # Registration gives the default role, so only this command can create the admin.
  admin_email="e2e-admin@example.com"
  admin_role="$(sed -n "s/^export const adminRole = '\(.*\)';\$/\1/p" src/lib/flags.gen.ts)"
  [[ -n "$admin_role" ]] || fail "web/src/lib/flags.gen.ts names no admin role." "run spinup g flags in the app and read $work/logs/flags.log"
  printf '%s' 'correct horse battery staple' |
    (cd .. && SPINUP_ENV=dev DATABASE_URL="$DATABASE_URL" ./api.bin user create --email "$admin_email" \
      --role "$admin_role" --password-stdin) >"$work/logs/admin-user.log" 2>&1 ||
    fail "the creation of the admin account failed." "read $work/logs/admin-user.log"
else
  echo "==> build the API"
  run_logged api-build "go build of the API" bash -c "cd api && go build -o ../api.bin ./cmd/api"

  echo "==> build the web app"
  cd web
  run_logged pnpm-install "pnpm install" pnpm install --frozen-lockfile ${offline[@]+"${offline[@]}"}
  # adapter-node bakes PUBLIC_ORIGIN into the build (ADR 0002 rule 2).
  PUBLIC_ORIGIN="$origin" API_URL="$api_url" run_logged pnpm-build "pnpm build" pnpm build
fi

if [[ -n "${E2E_INSTALL_BROWSER:-}" ]]; then
  browsers=(chromium)
  if [[ "${E2E_BROWSERS:-}" == "all" ]]; then
    browsers=(chromium firefox webkit)
  fi
  echo "==> install ${browsers[*]}"
  pnpm exec playwright install --with-deps "${browsers[@]}"
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

# The bundle check runs right before the built app is served. Each mode has its own budget file:
# the embed fixture is another app (ADR 0039 section 10.9).
if [[ "$mode" == embed ]]; then
  budgets="$repo/perf/budgets-embed.json"
else
  budgets="$repo/perf/budgets.json"
fi
if [[ -f "$budgets" ]]; then
  echo "==> check the client bundle against ${budgets#"$repo"/}"
  python3 -I "$repo/scripts/perf/bundle.py" check build "$budgets" ||
    fail "the client bundle is above a budget of ${budgets#"$repo"/}." "make the client code smaller, or change the budget with a reason in the commit"
else
  echo "==> measure the client bundle"
  python3 -I "$repo/scripts/perf/bundle.py" measure build ||
    fail "the measure of the client bundle failed." "run python3 -I scripts/perf/bundle.py measure web/build in the app"
  echo "budget file missing: ${budgets#"$repo"/}"
fi

echo "==> start the API on $api_url"
# exec, so the recorded PID is the API itself and cleanup stops it (a subshell PID would leave
# the API running).
# env execs the API in place, so the PID stays the API. Only embed mode sets SMTP_URL.
(cd .. && PORT="$api_port" PUBLIC_ORIGIN="$origin" SPINUP_API_DOCS=true \
  exec env ${smtp_url:+"SMTP_URL=$smtp_url"} ./api.bin >"$work/logs/api.log" 2>&1) &
pids+=($!)
if [[ "$mode" == node ]]; then
  echo "==> start the web app on $origin"
  # ADDRESS_HEADER and XFF_DEPTH are the production values of compose.yml. hooks.server.ts then sends
  # the client address of a browser request to the API (ratelimit.spec.ts sends a public one). A
  # request without the header gets no client address, and the hook warns once.
  PORT="$web_port" HOST=127.0.0.1 PUBLIC_ORIGIN="$origin" API_URL="$api_url" \
    ADDRESS_HEADER=X-Forwarded-For XFF_DEPTH=1 node build >"$work/logs/web.log" 2>&1 &
  pids+=($!)
fi

wait_for "$api_url/healthz" "the API"
if [[ "$mode" == node ]]; then
  wait_for "$origin/healthz" "the web app"
fi

echo "==> run Playwright (mode $mode)"
# In node mode a reverse proxy sends /api to the Go API in production. Here the tests call the API
# directly. In embed mode the binary serves /api on the page origin.
E2E_FRONTEND_MODE="$mode" E2E_BASE_URL="$origin" E2E_API_URL="$api_url" E2E_ADMIN_EMAIL="${admin_email:-}" \
  E2E_SMTP_PORT="${smtp_port:-}" \
  pnpm exec playwright test --config e2e/playwright.config.ts "$@"

# The generated app's own smoke test (web/playwright.config.ts, web/e2e/smoke.spec.ts), the
# suite that its e2e.yml runs in CI. Its config reads PUBLIC_ORIGIN.
echo "==> run the app's own Playwright smoke test"
PUBLIC_ORIGIN="$origin" pnpm exec playwright test e2e/smoke.spec.ts
