#!/usr/bin/env bash
# Trace propagation test: one request makes one trace across the SvelteKit server and the Go API.
# It generates an app, starts grafana/otel-lgtm, starts the API and the built web app with
# OpenTelemetry on, requests a page whose server load calls the API, and asks Tempo for the trace.
# It passes when a Go span has its parent span in the web service. It prints the trace ID.
# Set E2E_ONLINE=1 to install packages from the network (CI). The default is the offline store.
# Set E2E_KEEP=1 to keep the work directory.
# Needs: DATABASE_URL (a database you own), Docker, Go, Node 24, pnpm, curl, and python3.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "run.sh: DATABASE_URL is not set." >&2
  echo "  Try: DATABASE_URL=postgres://user:pass@127.0.0.1:5432/e2e?sslmode=disable $0" >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "run.sh: cannot reach the Docker daemon." >&2
  echo "  Try: start Docker, then run $0 again" >&2
  exit 1
fi

export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"
export PATH="$repo/scripts/bin:/opt/homebrew/opt/node@24/bin:$PATH"

# The pinned image, as the [[ image "otel-lgtm" ]] template function renders it.
image="$(sed -nE 's/^ *otel-lgtm: *\{ *image: *([^,]+), *tag: *"([^"]+)", *digest: *"([^"]+)".*/\1:\2@\3/p' \
  "$repo/internal/stackversions/stackversions.yaml")"
if [[ -z "$image" ]]; then
  echo "run.sh: cannot find the otel-lgtm image in internal/stackversions/stackversions.yaml." >&2
  echo "  Try: check the images section of that file" >&2
  exit 1
fi

lgtm=spinup-trace-lgtm
app=demo
web_service="$app-web"
api_service="$app-api"
trace_timeout=60

work="$(mktemp -d "${TMPDIR:-/tmp}/spinup-trace.XXXXXX")"
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
    docker logs "$lgtm" >"$work/logs/lgtm.log" 2>&1 || true
  fi
  docker rm -f "$lgtm" >/dev/null 2>&1 || true
  if [[ $status -ne 0 ]]; then
    echo "run.sh: failed. Logs are in $work/logs" >&2
  elif [[ -z "${E2E_KEEP:-}" ]]; then
    rm -rf "$work"
  fi
}
trap cleanup EXIT
mkdir -p "$work/logs" "$work/tempo"

free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'
}

wait_for() {
  local url=$1 name=$2
  for _ in $(seq 1 120); do
    if curl -fs -o /dev/null "$url"; then return 0; fi
    sleep 0.5
  done
  echo "run.sh: $name did not answer at $url within 60 seconds." >&2
  echo "  Try: read the logs in $work/logs" >&2
  return 1
}

# tempo GETs a path from the Tempo HTTP API inside the LGTM container.
tempo() {
  docker exec "$lgtm" curl -fsS "http://127.0.0.1:3200$1"
}

echo "==> start $image as $lgtm"
docker rm -f "$lgtm" >/dev/null 2>&1 || true
docker run -d --name "$lgtm" -p 127.0.0.1::4318 "$image" >"$work/logs/lgtm-id.log"
otlp_port="$(docker port "$lgtm" 4318/tcp | head -n1 | sed -E 's/.*:([0-9]+)$/\1/')"
otlp="http://127.0.0.1:$otlp_port"

echo "==> build the CLI"
(cd "$repo" && go build -o "$work/spinup" ./cmd/spinup)

echo "==> generate the app"
cd "$work"
SPINUP_KIT_REPLACE="$repo/kit" ./spinup new "$app" --module "example.com/$app" \
  --modules ci --no-git --non-interactive >"$work/logs/new.log" 2>&1
cd "$app"
# A page whose server load calls the Go API.
cp -R "$here/app/." web/

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

echo "==> build the API"
(cd api && go build -o ../api.bin ./cmd/api)

echo "==> build the web app"
cd web
offline=(--offline)
if [[ -n "${E2E_ONLINE:-}" ]]; then offline=(); fi
pnpm install --frozen-lockfile ${offline[@]+"${offline[@]}"} >"$work/logs/pnpm-install.log" 2>&1
# adapter-node bakes PUBLIC_ORIGIN into the build (ADR 0002 rule 2).
PUBLIC_ORIGIN="$origin" API_URL="$api_url" pnpm build >"$work/logs/pnpm-build.log" 2>&1
cd ..

echo "==> wait for the LGTM stack"
ready=""
for _ in $(seq 1 240); do
  if docker logs "$lgtm" 2>&1 | grep -q "The OpenTelemetry collector and the Grafana LGTM stack are up and running" \
    && tempo /ready >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.5
done
if [[ -z "$ready" ]]; then
  echo "run.sh: the LGTM stack in $lgtm did not start within 120 seconds." >&2
  echo "  Try: read $work/logs/lgtm.log" >&2
  exit 1
fi

# Short batch delays, so the spans reach Tempo soon after the request.
export OTEL_EXPORTER_OTLP_ENDPOINT="$otlp" OTEL_BSP_SCHEDULE_DELAY=200

echo "==> start the API on $api_url (OTLP to $otlp)"
# exec, so the recorded PID is the API itself and cleanup stops it.
(PORT="$api_port" PUBLIC_ORIGIN="$origin" exec ./api.bin >"$work/logs/api.log" 2>&1) &
pids+=($!)
echo "==> start the web app on $origin (OTLP to $otlp)"
# The same command as deploy/Dockerfile.web.
(cd web && PORT="$web_port" HOST=127.0.0.1 PUBLIC_ORIGIN="$origin" API_URL="$api_url" \
  exec node --import ./build/server/instrumentation.server.js build >"$work/logs/web.log" 2>&1) &
pids+=($!)

wait_for "$api_url/healthz" "the API"
wait_for "$origin/healthz" "the web app"

echo "==> request $origin/trace"
start="$(date +%s)"
body="$(curl -fsS "$origin/trace")"
if [[ "$body" != *"api-status=200"* ]]; then
  echo "run.sh: GET $origin/trace did not show api-status=200." >&2
  echo "  Try: read $work/logs/web.log and $work/logs/api.log" >&2
  exit 1
fi

echo "==> wait up to ${trace_timeout}s for the trace in Tempo"
query="$(python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1]))' \
  "{resource.service.name=\"$api_service\"}")"
found=""
last="no trace of $api_service in Tempo yet"
while (($(date +%s) - start < trace_timeout)); do
  if tempo "/api/search?q=$query&limit=50&start=$((start - 60))&end=$((start + 600))" \
    >"$work/tempo/search.json" 2>"$work/tempo/search.err"; then
    while read -r id; do
      [[ -n "$id" ]] || continue
      tempo "/api/v2/traces/$id" >"$work/tempo/$id.json" 2>/dev/null || continue
      if last="$(python3 "$here/check_trace.py" link "$work/tempo/$id.json" "$id" "$web_service" "$api_service")"; then
        found="$id"
        break
      fi
    done < <(python3 "$here/check_trace.py" ids "$work/tempo/search.json")
  fi
  [[ -n "$found" ]] && break
  sleep 1
done
if [[ -z "$found" ]]; then
  echo "run.sh: Tempo has no trace with a $web_service span as the parent of a $api_service span after ${trace_timeout}s." >&2
  echo "  Last result: $last" >&2
  echo "  Try: read $work/logs/web.log, $work/logs/api.log, and the Tempo responses in $work/tempo" >&2
  exit 1
fi

echo "$last"
echo "trace_id=$found"
echo "PASS: one trace from $web_service to $api_service"
