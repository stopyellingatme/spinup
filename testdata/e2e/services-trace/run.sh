#!/usr/bin/env bash
# Check one incoming trace across the Rust and Python service templates in Tempo.
# Needs: Docker, Go, curl, python3, and network access for the image builds.
# Set E2E_KEEP=1 to keep the work directory. Failed runs always keep it.
# Set SERVICES_TRACE_LOG_DIR to choose the log directory for CI artifacts.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"

for tool in docker go curl python3; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "run.sh: $tool is missing from PATH." >&2
    echo "  Try: install $tool, then run $0 again" >&2
    exit 1
  fi
done
if ! docker info >/dev/null 2>&1; then
  echo "run.sh: cannot reach the Docker daemon." >&2
  echo "  Try: start Docker, then run $0 again" >&2
  exit 1
fi

export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"

# Read the same pinned image that the monitoring templates render.
image="$(sed -nE 's/^ *otel-lgtm: *\{ *image: *([^,]+), *tag: *"([^"]+)", *digest: *"([^"]+)".*/\1:\2@\3/p' \
  "$repo/internal/stackversions/stackversions.yaml")"
if [[ -z "$image" ]]; then
  echo "run.sh: cannot find the otel-lgtm image in internal/stackversions/stackversions.yaml." >&2
  echo "  Try: check the images section of that file" >&2
  exit 1
fi

random_hex() { od -An -tx1 -N"$1" /dev/urandom | tr -d ' \n'; }
run_id="$(random_hex 6)"
network="spinup-services-trace-$run_id"
lgtm="$network-lgtm"
app=demo
trace_timeout=60
work="$(mktemp -d "${TMPDIR:-/tmp}/spinup-services-trace.XXXXXX")"
logs="${SERVICES_TRACE_LOG_DIR:-$work/logs}"
containers=("$lgtm" "$network-rs" "$network-py")
cleanup() {
  local status=$? container
  for container in "${containers[@]}"; do
    docker logs "$container" >"$logs/$container.log" 2>&1 || true
    docker rm -f "$container" >/dev/null 2>&1 || true
  done
  docker network rm "$network" >/dev/null 2>&1 || true
  docker image rm "$app-rs:$run_id" "$app-py:$run_id" >/dev/null 2>&1 || true
  if [[ $status -ne 0 ]]; then
    echo "run.sh: failed. Logs are in $logs. Work directory: $work" >&2
    echo "  Try: read the logs and the Tempo responses in $logs/tempo" >&2
  elif [[ -z "${E2E_KEEP:-}" ]]; then
    rm -rf "$work"
  else
    echo "Work directory: $work. Logs: $logs"
  fi
}
mkdir -p "$logs/tempo"
trap cleanup EXIT

wait_for() {
  local url=$1 name=$2
  for _ in $(seq 1 120); do
    if curl -fs --max-time 2 -o /dev/null "$url"; then return 0; fi
    sleep 0.5
  done
  echo "run.sh: $name did not answer at $url after 120 attempts." >&2
  echo "  Try: read the logs in $logs" >&2
  return 1
}

# Read Tempo through the container so it needs no published host port.
tempo() {
  docker exec "$lgtm" curl -fsS --max-time 5 -H 'Accept: application/json' "http://127.0.0.1:3200$1"
}

echo "==> build the CLI"
(cd "$repo" && go build -o "$work/spinup" ./cmd/spinup) >"$logs/build-cli.log" 2>&1

echo "==> generate the app and both services"
cd "$work"
SPINUP_KIT_REPLACE="$repo/kit" ./spinup new "$app" --api-only --module "example.com/$app" \
  --modules ci --no-git --non-interactive >"$logs/new.log" 2>&1
cd "$app"
"$work/spinup" g service rs --lang rust >"$logs/generate-rs.log" 2>&1
"$work/spinup" g service py --lang python >"$logs/generate-py.log" 2>&1
for name in rs py; do
  echo "==> build $app-$name:$run_id"
  docker build -t "$app-$name:$run_id" "services/$name" >"$logs/build-$name.log" 2>&1
done

echo "==> start $image as $lgtm"
docker network create "$network" >"$logs/network.log"
docker run -d --name "$lgtm" --network "$network" --network-alias otel-lgtm \
  "$image" >"$logs/lgtm-id.log"
ready=""
for _ in $(seq 1 240); do
  if docker logs "$lgtm" 2>&1 | grep -F "The OpenTelemetry collector and the Grafana LGTM stack are up and running" >/dev/null \
    && tempo /ready >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.5
done
if [[ -z "$ready" ]]; then
  echo "run.sh: the LGTM stack in $lgtm did not start after 240 attempts." >&2
  echo "  Try: read the container logs in $logs" >&2
  exit 1
fi

# Both requests use the same sampled remote parent, so Tempo must hold both services.
trace_id="$(random_hex 16)"
parent_id="$(random_hex 8)"
traceparent="00-$trace_id-$parent_id-01"
echo "trace_id=$trace_id parent_span_id=$parent_id"
printf 'trace_id=%s\nparent_span_id=%s\n' "$trace_id" "$parent_id" >"$logs/trace.log"
for name in rs py; do
  container="$network-$name"
  echo "==> start $app-$name with OTLP to http://otel-lgtm:4318"
  docker run -d --name "$container" --network "$network" -p 127.0.0.1::8080 \
    -e OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-lgtm:4318 \
    -e "OTEL_SERVICE_NAME=$app-$name" -e OTEL_BSP_SCHEDULE_DELAY=200 \
    "$app-$name:$run_id" >"$logs/$name-id.log"
  port="$(docker port "$container" 8080/tcp | head -n1 | sed -E 's/.*:([0-9]+)$/\1/')"
  url="http://127.0.0.1:$port"
  wait_for "$url/healthz" "$app-$name"
  curl -fsS --max-time 10 -H 'Content-Type: application/json' -H "traceparent: $traceparent" \
    -d '{"values": [1, 2.5]}' "$url/v1/sum" >"$logs/$name-response.json"
done

echo "==> wait up to ${trace_timeout}s for both parent links in Tempo"
start="$(date +%s)"
found=""
last="no trace in Tempo yet"
while (($(date +%s) - start < trace_timeout)); do
  if tempo "/api/traces/$trace_id" >"$logs/tempo/$trace_id.json" 2>"$logs/tempo/trace.err"; then
    if last="$(python3 "$here/check_trace.py" link "$logs/tempo/$trace_id.json" \
      "$trace_id" "$parent_id" "$app-rs" "$app-py")"; then
      found=1
      break
    fi
  fi
  sleep 1
done
if [[ -z "$found" ]]; then
  echo "run.sh: Tempo has no trace with both service spans under parent $parent_id after ${trace_timeout}s." >&2
  echo "  Last result: $last" >&2
  echo "  Try: read the container logs and Tempo responses in $logs" >&2
  exit 1
fi

echo "$last" | tee -a "$logs/trace.log"
echo "PASS: one trace across the incoming parent, $app-rs, and $app-py"
