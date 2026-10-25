#!/usr/bin/env bash
# run.sh: the † check of R0.9. It runs the SvelteKit web pack against the Rust
# reference API and runs the Playwright suite of testdata/e2e/web.
#
# Steps:
#   1. Build the Go CLI. Render the app of testdata/e2e/web/run.sh
#      (`spinup new --modules ci,auth`, then `spinup g resource todo ...`).
#      Compare its web/ with the committed web/ of this spike. A difference
#      fails the run: the spike must use the web pack as the CLI renders it.
#   2. Copy web/ to a work directory. Install offline and build.
#   3. Generate the TypeScript schema from the Rust spec and compare it with
#      the committed web/src/lib/api/schema.d.ts (made from the Go spec).
#   4. Build todo-api, empty the test database, and run `todo-api migrate up`.
#   5. If Docker has the local grafana/otel-lgtm image, start it, and send
#      the traces of the web server and the API to it.
#   6. Start the API and the web server. Run the Playwright suite of
#      testdata/e2e/web, then the smoke test of the app (web/e2e).
#   7. With LGTM: find a trace in Tempo that has spans of demo-web and
#      demo-api, where an API server span is a child of a web span.
#
# Usage (from any directory):
#   DATABASE_URL=postgres://postgres:postgres@127.0.0.1:55433/postgres ./run.sh [playwright args]
# Environment:
#   DATABASE_URL  any database on the test server. The script uses the
#                 database E2E_DB (default spinup_r09_e2e) on the same
#                 server, creates it when it is missing, and empties its
#                 public schema.
#   LGTM=0        do not start the LGTM container (the Tempo step is "not run").
#   LGTM_IMAGE    the image (default: the local grafana/otel-lgtm digest of R0.8).
#   E2E_KEEP=1    keep the work directory.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../.." && pwd)
: "${DATABASE_URL:?set DATABASE_URL to a database on the test server (127.0.0.1:55433)}"
export GOTOOLCHAIN=${GOTOOLCHAIN:-local}
export PATH="$repo/scripts/bin:/opt/homebrew/opt/node@24/bin:$PATH"
export CARGO_NET_OFFLINE=true

e2e_db=${E2E_DB:-spinup_r09_e2e}
server_url=${DATABASE_URL%/*}
query=""
[[ $DATABASE_URL == *\?* ]] && query="?${DATABASE_URL#*\?}"
db_url="$server_url/$e2e_db$query"
admin_url="$server_url/postgres$query"

image=${LGTM_IMAGE:-grafana/otel-lgtm@sha256:57c04cbd8e7022f32bb84c4ac005d79baf8b08e2c5be44facf11f5f1f9944ba2}
lgtm_name=r09-lgtm-$$

work=$(mktemp -d "${TMPDIR:-/tmp}/r09-web.XXXXXX")
mkdir -p "$work/logs"
pids=()
lgtm_started=""
cleanup() {
  local status=$?
  for pid in ${pids[@]+"${pids[@]}"}; do kill "$pid" 2>/dev/null || true; done
  for pid in ${pids[@]+"${pids[@]}"}; do wait "$pid" 2>/dev/null || true; done
  if [[ -n $lgtm_started ]]; then docker rm -f "$lgtm_name" > /dev/null 2>&1 || true; fi
  if [[ $status -ne 0 ]]; then
    echo "run.sh: failed. Logs are in $work/logs" >&2
  elif [[ -z ${E2E_KEEP:-} ]]; then
    rm -rf "$work"
  else
    echo "run.sh: the work directory is $work"
  fi
}
trap cleanup EXIT

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

stamp() { echo "==> $* (load $(uptime | sed 's/.*load averages*: //' | awk '{print $1}'))"; }

stamp "build the Go CLI and render the app"
(cd "$repo" && go build -o "$work/spinup" ./cmd/spinup)
(cd "$work" && SPINUP_KIT_REPLACE="$repo/kit" ./spinup new demo --module example.com/demo \
  --modules ci,auth --no-git --non-interactive > "$work/logs/new.log" 2>&1)
(cd "$work/demo" && "$work/spinup" g resource todo title:string done:bool 'due:date?' > "$work/logs/resource.log" 2>&1)
if ! diff -r -x node_modules -x .svelte-kit "$work/demo/web" "$here/web" > "$work/logs/web.diff"; then
  echo "run.sh: the rendered web/ differs from the committed web/ of the spike:" >&2
  head -40 "$work/logs/web.diff" >&2
  echo "  Try: copy $work/demo/web (without node_modules) to $here/web" >&2
  exit 1
fi
echo "    the committed web/ equals the web/ that the Go CLI renders"

stamp "install and build the web app"
api_port=$(free_port)
web_port=$(free_port)
origin="http://127.0.0.1:$web_port"
api_url="http://127.0.0.1:$api_port"
mkdir -p "$work/app"
rsync -a --exclude node_modules --exclude .svelte-kit "$here/web/" "$work/app/web/"
cd "$work/app/web"
pnpm install --frozen-lockfile --offline > "$work/logs/pnpm-install.log" 2>&1
PUBLIC_ORIGIN="$origin" API_URL="$api_url" pnpm build > "$work/logs/pnpm-build.log" 2>&1

stamp "build todo-api"
(cd "$here" && cargo build --offline --locked -p todo-api > "$work/logs/cargo-build.log" 2>&1)
bin=${CARGO_TARGET_DIR:-$here/target}/debug/todo-api

stamp "compare the TypeScript schema of the Rust spec with the committed client"
"$bin" openapi > "$work/rust.openapi.yaml"
pnpm exec openapi-typescript "$work/rust.openapi.yaml" -o "$work/rust.schema.d.ts" > "$work/logs/openapi-ts.log" 2>&1
if diff "$work/app/web/src/lib/api/schema.d.ts" "$work/rust.schema.d.ts" > "$work/logs/schema.diff"; then
  echo "    schema.d.ts: 0 differing lines"
else
  echo "    schema.d.ts: $(grep -c '^[<>]' "$work/logs/schema.diff") differing lines (see logs/schema.diff)"
fi

stamp "reset the test database $e2e_db and migrate"
if ! psql "$admin_url" -tAc "SELECT 1 FROM pg_database WHERE datname = '$e2e_db'" | grep -q 1; then
  psql "$admin_url" -v ON_ERROR_STOP=1 -q -c "CREATE DATABASE $e2e_db"
fi
psql "$db_url" -v ON_ERROR_STOP=1 -q -c 'DROP SCHEMA public CASCADE' -c 'CREATE SCHEMA public' > "$work/logs/reset.log" 2>&1
DATABASE_URL="$db_url" "$bin" migrate up > "$work/logs/migrate.log" 2>&1

otel=()
lgtm_grafana=""
if [[ ${LGTM:-1} != 0 ]] && command -v docker > /dev/null 2>&1 && docker image inspect "$image" > /dev/null 2>&1; then
  stamp "start LGTM ($image)"
  g_port=$(free_port)
  o_port=$(free_port)
  docker run -d --rm --name "$lgtm_name" --pull never \
    -p "127.0.0.1:$g_port:3000" -p "127.0.0.1:$o_port:4318" "$image" > /dev/null
  lgtm_started=1
  lgtm_grafana="http://127.0.0.1:$g_port"
  for _ in $(seq 1 120); do
    if curl -fsS "$lgtm_grafana/api/health" > /dev/null 2>&1 &&
      curl -fsS -o /dev/null -X POST -H 'content-type: application/x-protobuf' \
        --data-binary '' "http://127.0.0.1:$o_port/v1/traces" 2> /dev/null; then
      break
    fi
    sleep 1
  done
  otel=(OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:$o_port" OTEL_METRIC_EXPORT_INTERVAL=5000)
else
  echo "==> LGTM: not run (LGTM=0, no Docker, or no local image $image)"
fi

stamp "start the Rust API on $api_url"
(cd "$work" && exec env ${otel[@]+"${otel[@]}"} SPINUP_ENV=dev PORT="$api_port" LOG_LEVEL=info \
  DATABASE_URL="$db_url" PUBLIC_ORIGIN="$origin" SPINUP_API_DOCS=true "$bin" serve > "$work/logs/api.log" 2>&1) &
pids+=($!)
stamp "start the web app on $origin"
# The values of testdata/e2e/web/run.sh: ADDRESS_HEADER and XFF_DEPTH are the production values.
(exec env ${otel[@]+"${otel[@]}"} PORT="$web_port" HOST=127.0.0.1 PUBLIC_ORIGIN="$origin" API_URL="$api_url" \
  ADDRESS_HEADER=X-Forwarded-For XFF_DEPTH=1 node build > "$work/logs/web.log" 2>&1) &
pids+=($!)
wait_for "$api_url/healthz" "the API"
wait_for "$origin/healthz" "the web app"

stamp "check the ADR 0002 evidence with curl (form action, cookie, SSR, CSRF)"
email="curl-$$-$RANDOM@example.com"
form=(--data-urlencode "email=$email" --data-urlencode "password=correct horse battery staple")
code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'Origin: http://evil.example' "${form[@]}" "$origin/register")
echo "    cross-site POST /register: $code (want 403)"
[[ $code == 403 ]]
code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${form[@]}" "$origin/register")
echo "    POST /register without Origin: $code (want 403)"
[[ $code == 403 ]]
code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'Origin: http://evil.example' \
  -H 'content-type: application/json' -d '{"email":"a@example.com","password":"x"}' "$api_url/api/auth/login")
echo "    cross-site POST to the Rust API /api/auth/login: $code (want 403)"
[[ $code == 403 ]]
curl -sS -D "$work/register.headers" -o /dev/null -X POST -H "Origin: $origin" \
  -H 'X-Forwarded-For: 198.51.100.50' "${form[@]}" "$origin/register"
status=$(head -1 "$work/register.headers" | tr -d '\r')
cookie_line=$(grep -i '^set-cookie: session=' "$work/register.headers" | tr -d '\r' || true)
echo "    same-origin form action POST /register: $status"
echo "    browser cookie: $(sed -E 's/session=[^;]*/session=<value>/' <<< "$cookie_line")"
# The Rust API sends Max-Age=604799 (tower-sessions truncates expiry minus now).
# Go (scs) sends 604800. The R0.9 result records the difference.
[[ $cookie_line == *HttpOnly* && $cookie_line == *SameSite=Lax* && $cookie_line == *Path=/* ]]
[[ $cookie_line =~ Max-Age=(604799|604800) ]]
session=$(sed -E 's/^[Ss]et-[Cc]ookie: session=([^;]*).*/\1/' <<< "$cookie_line")
body=$(curl -sS -H "Cookie: session=$session" "$origin/account")
if grep -q "$email" <<< "$body"; then
  echo "    SSR GET /account with the cookie: the page shows $email"
else
  echo "run.sh: SSR GET /account does not show the user" >&2
  exit 1
fi

mkdir -p e2e-suite
cp "$repo/testdata/e2e/web/playwright.config.ts" e2e-suite/
cp -R "$repo/testdata/e2e/web/tests" e2e-suite/
stamp "run the Playwright suite of testdata/e2e/web"
start=$SECONDS
E2E_BASE_URL="$origin" E2E_API_URL="$api_url" pnpm exec playwright test --config e2e-suite/playwright.config.ts "$@"
echo "    suite time: $((SECONDS - start)) s"
stamp "run the app's own Playwright smoke test (web/e2e)"
PUBLIC_ORIGIN="$origin" pnpm exec playwright test e2e/smoke.spec.ts

if [[ -n $lgtm_grafana ]]; then
  stamp "find a web-to-API trace in Tempo"
  python3 - "$lgtm_grafana" <<'PY'
import base64, json, sys, time, urllib.parse, urllib.request
grafana = sys.argv[1]
auth = {"Authorization": "Basic YWRtaW46YWRtaW4="}  # admin:admin, the LGTM default
def get(path):
    req = urllib.request.Request(grafana + path, headers=auth)
    with urllib.request.urlopen(req, timeout=20) as r:
        return json.load(r)
def hexid(v):
    if not v:
        return ""
    if len(v) in (16, 32) and all(c in "0123456789abcdef" for c in v.lower()):
        return v.lower()
    return base64.b64decode(v).hex()
q = urllib.parse.quote('{ resource.service.name = "demo-web" } >> { resource.service.name = "demo-api" && kind = server }')
deadline = time.time() + 60
traces = []
while time.time() < deadline:
    try:
        traces = get(f"/api/datasources/proxy/uid/tempo/api/search?q={q}&limit=20").get("traces", [])
    except Exception as e:  # Tempo may still start
        print("tempo search:", e)
    if traces:
        break
    time.sleep(3)
print(f"tempo: {len(traces)} traces with a demo-web span above a demo-api server span")
if not traces:
    sys.exit(1)
checked = 0
for t in traces[:5]:
    data = get(f"/api/datasources/proxy/uid/tempo/api/traces/{t['traceID']}")
    spans = {}
    for b in data.get("batches", data.get("resourceSpans", [])):
        svc = next((a["value"].get("stringValue") for a in b["resource"]["attributes"] if a["key"] == "service.name"), "?")
        for ss in b.get("scopeSpans", b.get("instrumentationLibrarySpans", [])):
            for s in ss["spans"]:
                spans[hexid(s["spanId"])] = (svc, s["name"], hexid(s.get("parentSpanId", "")), s.get("kind"))
    for sid, (svc, name, parent, kind) in spans.items():
        if svc == "demo-api" and parent in spans and spans[parent][0] == "demo-web":
            print(f"trace {t['traceID']}: root {t.get('rootServiceName')} {t.get('rootTraceName')!r}; "
                  f"api span {name!r} is a child of web span {spans[parent][1]!r}; "
                  f"{len(spans)} spans, services {sorted({v[0] for v in spans.values()})}")
            checked += 1
            break
if not checked:
    print("tempo: no API span with a web parent")
    sys.exit(1)
print(f"traceparent check: ok ({checked} of {min(5, len(traces))} traces checked)")
PY
fi
stamp "done"
