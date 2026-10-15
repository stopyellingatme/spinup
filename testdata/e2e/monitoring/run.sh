#!/usr/bin/env bash
# Monitoring stack boot test (docs/contributing/monitoring-boot.md).
#
# Usage: run.sh [lite|full]   (default lite)
#
# It renders an app with --modules ci,deploy,monitoring and the profile, starts stand-ins for
# the base project (the <app>-app network with Caddy, and in full the <app>-db network with
# Postgres), and boots monitoring/compose.monitoring.yml as the Compose project
# spinup-boot-<profile>. Then it checks:
#   - every service is running and healthy (a service without a health check: running)
#   - Grafana answers on 127.0.0.1:<port>/api/health
#   - Prometheus has its targets up (/api/v1/targets), and the targets that Alloy scrapes
#     have up == 1
#   - a test log line pushed to Alloy's OTLP endpoint appears in Loki
#   - in full, a test span pushed to Alloy appears in Tempo
#   - the total memory of the stack (docker stats) is under the budget of the profile
# A trap always removes every container, network, and volume that it creates (prefix
# spinup-boot-) and its work directory.
#
# Options (environment):
#   BOOT_GRAFANA_PORT  the host port of Grafana on 127.0.0.1. The default is 3000. When 3000 is
#                      in use and the variable is not set, the script picks a free port.
#   BOOT_BUDGET_MIB    the memory budget in MiB. The defaults are lite 1024, full 2560.
#   BOOT_LOG_DIR       a directory for the logs of a failed run (CI uploads it).
#   BOOT_TIMEOUT       the seconds to wait for each check. The default is 180.
# Needs: Docker with Compose v2.24 or later, Go, curl, and python3.
set -euo pipefail

profile="${1:-lite}"
case "$profile" in
  lite) default_budget=1024 ;;
  full) default_budget=2560 ;;
  *)
    echo "run.sh: the profile \"$profile\" is not known." >&2
    echo "  Try: $0 lite, or $0 full" >&2
    exit 2
    ;;
esac

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
check="$here/check.py"

for tool in docker go curl python3; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "run.sh: the command $tool is not on PATH." >&2
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

# image NAME prints the pinned image of NAME in stackversions.yaml as image:tag@digest.
image() {
  local ref
  ref="$(sed -nE "s/^ *$1: *\\{ *image: *([^,]+), *tag: *\"([^\"]+)\", *digest: *\"([^\"]+)\".*/\\1:\\2@\\3/p" \
    "$repo/internal/stackversions/stackversions.yaml")"
  if [[ -z "$ref" ]]; then
    echo "run.sh: cannot find the image $1 in internal/stackversions/stackversions.yaml." >&2
    echo "  Try: check the images section of that file" >&2
    return 1
  fi
  echo "$ref"
}
caddy_image="$(image caddy)"
postgres_image="$(image postgres)"

app="spinup-boot-$profile"
project="$app"
caddy="$app-caddy"
postgres="$app-postgres"
app_net="$app-app"
db_net="$app-db"
budget="${BOOT_BUDGET_MIB:-$default_budget}"
timeout="${BOOT_TIMEOUT:-180}"

port_free() {
  python3 - "$1" <<'PY'
import socket, sys
s = socket.socket()
try:
    s.bind(("127.0.0.1", int(sys.argv[1])))
except OSError:
    sys.exit(1)
PY
}
free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'
}

grafana_port="${BOOT_GRAFANA_PORT:-}"
if [[ -z "$grafana_port" ]]; then
  grafana_port=3000
  if ! port_free 3000; then
    grafana_port="$(free_port)"
    echo "note: 127.0.0.1:3000 is in use, so Grafana uses 127.0.0.1:$grafana_port. Set BOOT_GRAFANA_PORT to choose the port."
  fi
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/spinup-boot-$profile.XXXXXX")"
compose_files=()
started=""

compose() {
  DEPLOY_UID="$(id -u)" DEPLOY_GID="$(id -g)" PUBLIC_URL="${public_url:-}" \
    BOOT_GRAFANA_PORT="$grafana_port" BOOT_SECRETS="$work/secrets" BOOT_METRICS="$work/metrics" \
    docker compose -p "$project" ${compose_files[@]+"${compose_files[@]}"} "$@"
}

# remove_all removes every Docker object of this run. It is also the cleanup of a run that
# stopped before (the names are fixed).
remove_all() {
  if [[ ${#compose_files[@]} -gt 0 ]]; then
    compose down --volumes --remove-orphans --timeout 10 >/dev/null 2>&1 || true
  fi
  # Containers and volumes of the project, also when the Compose files are gone.
  local ids
  ids="$(docker ps -aq --filter "label=com.docker.compose.project=$project")"
  if [[ -n "$ids" ]]; then
    # shellcheck disable=SC2086
    docker rm -f $ids >/dev/null 2>&1 || true
  fi
  ids="$(docker volume ls -q --filter "label=com.docker.compose.project=$project")"
  if [[ -n "$ids" ]]; then
    # shellcheck disable=SC2086
    docker volume rm -f $ids >/dev/null 2>&1 || true
  fi
  docker rm -f "$caddy" "$postgres" >/dev/null 2>&1 || true
  for net in "$app-monitoring" "$app-monitoring-egress" "$app_net" "$db_net"; do
    docker network rm "$net" >/dev/null 2>&1 || true
  done
}

cleanup() {
  local status=$?
  set +e
  if [[ $status -ne 0 && -n "$started" ]]; then
    mkdir -p "$work/logs"
    compose ps -a >"$work/logs/ps.txt" 2>&1
    compose logs --no-color --timestamps >"$work/logs/compose.log" 2>&1
    docker logs "$caddy" >"$work/logs/caddy.log" 2>&1
    docker logs "$postgres" >"$work/logs/postgres.log" 2>&1
    echo "run.sh: failed. The last lines of each service log:" >&2
    for service in $(compose config --services 2>/dev/null); do
      echo "--- $service" >&2
      compose logs --no-color --tail 15 "$service" 2>&1 | cut -c1-300 | sed 's/^/    /' >&2
    done
    if [[ -n "${BOOT_LOG_DIR:-}" ]]; then
      mkdir -p "$BOOT_LOG_DIR/$profile"
      cp -R "$work/logs/." "$BOOT_LOG_DIR/$profile/"
      [[ -d "$work/http" ]] && cp -R "$work/http" "$BOOT_LOG_DIR/$profile/"
      echo "run.sh: the logs are in $BOOT_LOG_DIR/$profile" >&2
    fi
  fi
  echo "==> tear down $project"
  remove_all
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

mkdir -p "$work/logs" "$work/http" "$work/secrets" "$work/metrics" "$work/caddy/Caddyfile.d"

# Remove what an interrupted run left.
remove_all

echo "==> build the CLI"
(cd "$repo" && go build -o "$work/spinup" ./cmd/spinup)

echo "==> render $app (profile $profile)"
(cd "$work" && SPINUP_KIT_REPLACE="$repo/kit" ./spinup new "$app" --api-only \
  --module "example.com/$app" --modules ci,deploy,monitoring --monitoring-profile "$profile" \
  --no-git --non-interactive >"$work/logs/new.log" 2>&1) || {
  cat "$work/logs/new.log" >&2
  echo "run.sh: spinup new failed." >&2
  echo "  Try: read the output above" >&2
  exit 1
}
appdir="$work/$app"
compose_files=(-f "$appdir/monitoring/compose.monitoring.yml" -f "$work/boot.override.yml")

# The secrets. On the VM they are files in /opt/<app>/secrets, mode 0600, owned by the deploy
# user. Compose runs Grafana and the postgres-exporter as that user (DEPLOY_UID, DEPLOY_GID).
random_hex() { od -An -tx1 -N"$1" /dev/urandom | tr -d ' \n'; }
grafana_password="$(random_hex 16)"
postgres_password="$(random_hex 16)"
printf '%s' "$grafana_password" >"$work/secrets/GRAFANA_ADMIN_PASSWORD"
printf '%s' "$postgres_password" >"$work/secrets/POSTGRES_PASSWORD"
chmod 0700 "$work/secrets"
chmod 0600 "$work/secrets/"*

# The backup metrics file (ADR 0011 §4). On the VM, spinup backup and run.sh write it in
# /opt/<app>/backups/metrics. Here a sample says the last full backup finished an hour ago.
printf '# TYPE backup_last_success_timestamp_seconds gauge\nbackup_last_success_timestamp_seconds{kind="full"} %s\n' \
  "$(($(date +%s) - 3600))" >"$work/metrics/backup.prom"
chmod 0755 "$work/metrics"
chmod 0644 "$work/metrics/backup.prom"

# The only changes to the rendered file: the secret and backup-metrics directories of the
# test, and the Grafana port.
cat >"$work/boot.override.yml" <<'YAML'
services:
  alloy:
    volumes: !override
      - ./alloy/config.alloy:/etc/alloy/config.alloy:ro
      - alloy_data:/var/lib/alloy/data
      - ${BOOT_METRICS}:/var/lib/spinup/backup-metrics:ro
  grafana:
    ports: !override
      - "127.0.0.1:${BOOT_GRAFANA_PORT}:3000"
    volumes:
      - ${BOOT_SECRETS}:/run/secrets:ro
YAML
if [[ "$profile" == full ]]; then
  cat >>"$work/boot.override.yml" <<'YAML'
  postgres-exporter:
    volumes:
      - ${BOOT_SECRETS}:/run/secrets:ro
YAML
  # Docker Desktop (macOS, Windows) runs containers in a VM whose root mount is private, so
  # the rslave propagation of node-exporter fails there. A Linux host (the VM, CI) keeps it.
  if [[ "$(docker info --format '{{.OperatingSystem}}')" == "Docker Desktop" ]]; then
    echo "note: Docker Desktop: node-exporter mounts / without rslave (the VM root mount is private)."
    cat >>"$work/boot.override.yml" <<'YAML'
  node-exporter:
    volumes:
      - /:/host:ro
YAML
  fi
fi

# The base project stand-in: the app network with the real Caddy image and the rendered
# Caddyfile, so Alloy scrapes caddy:2020. upstream.caddy is a deploy artifact. Here it answers
# every route, so the blackbox exporter (full) probes a page that answers 200.
cp "$appdir/deploy/Caddyfile" "$work/caddy/Caddyfile"
echo 'respond "spinup-boot stand-in for {args[0]}" 200' >"$work/caddy/upstream.caddy"
public_url="http://caddy/"
started=1

echo "==> start the stand-in base project ($app_net with Caddy)"
docker network create "$app_net" >/dev/null
docker run -d --name "$caddy" --network "$app_net" --network-alias caddy \
  -e PUBLIC_HOST=:80 -v "$work/caddy:/etc/caddy:ro" "$caddy_image" >/dev/null
if [[ "$profile" == full ]]; then
  echo "==> start the stand-in Postgres ($db_net)"
  docker network create --internal "$db_net" >/dev/null
  docker run -d --name "$postgres" --network "$db_net" --network-alias postgres \
    -e POSTGRES_USER=app -e POSTGRES_DB=app -e POSTGRES_PASSWORD="$postgres_password" \
    "$postgres_image" >/dev/null
fi

echo "==> docker compose -p $project up -d --wait (Grafana on 127.0.0.1:$grafana_port)"
if ! compose up -d --wait --wait-timeout "$timeout" >"$work/logs/up.log" 2>&1; then
  cat "$work/logs/up.log" >&2
  echo "run.sh: the stack did not become healthy within ${timeout}s." >&2
  echo "  Try: read the service logs below" >&2
  exit 1
fi

# poll NAME COMMAND... runs COMMAND every 3 seconds until it succeeds or BOOT_TIMEOUT ends.
# The output of the last try is the failure message.
poll() {
  local name=$1 deadline=$((SECONDS + timeout)) out
  shift
  while true; do
    if out="$("$@" 2>&1)"; then
      echo "    $out"
      return 0
    fi
    if ((SECONDS >= deadline)); then
      echo "run.sh: $name failed after ${timeout}s: $out" >&2
      echo "  Try: read the service logs below" >&2
      return 1
    fi
    sleep 3
  done
}

echo "==> every service is running and healthy"
services="$(compose config --services | sort)"
service_state() {
  local bad=() service id state
  for service in $services; do
    id="$(compose ps -aq "$service")"
    if [[ -z "$id" ]]; then
      bad+=("$service: no container")
      continue
    fi
    state="$(docker inspect --format '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}} {{.RestartCount}}' "$id")"
    case "$state" in
      "running healthy 0" | "running none 0") ;;
      *) bad+=("$service: $state (status health restarts)") ;;
    esac
  done
  if [[ ${#bad[@]} -gt 0 ]]; then
    printf '%s; ' "${bad[@]}"
    return 1
  fi
  echo "$(echo "$services" | wc -l | tr -d ' ') services: $(echo "$services" | tr '\n' ' ')"
}
poll "the service check" service_state

grafana="http://127.0.0.1:$grafana_port"
# api PATH FILE GETs PATH from Grafana as the admin user and writes the body to FILE.
api() {
  curl -fsS --max-time 10 -u "admin:$grafana_password" -o "$2" "$grafana$1"
}
urlencode() { python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$1"; }

echo "==> Grafana answers on $grafana/api/health"
grafana_health() {
  curl -fsS --max-time 5 "$grafana/api/health" >"$work/http/health.json" &&
    python3 -c 'import json, sys; d = json.load(open(sys.argv[1])); assert d.get("database") == "ok", d; print("database", d["database"], "version", d.get("version"))' "$work/http/health.json"
}
poll "GET $grafana/api/health" grafana_health

# Grafana reaches the internal monitoring network. Its datasource proxy is the test's way in,
# and it also proves the provisioned datasources work.
prom="/api/datasources/proxy/uid/prometheus/api/v1"
echo "==> Prometheus has its targets up"
targets_check() {
  api "$prom/targets" "$work/http/targets.json" && python3 "$check" targets "$work/http/targets.json"
}
poll "the Prometheus targets" targets_check

# config.alloy relabels the self-exporter target (job="integrations/self") to job="alloy".
jobs=(prometheus alloy caddy loki grafana)
if [[ "$profile" == full ]]; then
  jobs+=(tempo node cadvisor postgres blackbox-exporter blackbox)
fi
echo "==> the targets that Alloy scrapes are up: ${jobs[*]}"
up_check() {
  api "$prom/query?query=up" "$work/http/up.json" && python3 "$check" up "$work/http/up.json" "${jobs[@]}"
}
poll "the up check" up_check

echo "==> the backup metrics file reaches Prometheus (backup_last_age_seconds)"
backup_age_check() {
  api "$prom/query?query=$(urlencode 'max(backup_last_age_seconds)')" "$work/http/backup_age.json" &&
    python3 -c 'import json, sys
r = json.load(open(sys.argv[1]))["data"]["result"]
assert r, "no backup_last_age_seconds yet"
age = float(r[0]["value"][1])
assert 3000 < age < 7200, f"backup_last_age_seconds = {age}, want about 3600"
print(f"backup_last_age_seconds {age:.0f}")' "$work/http/backup_age.json"
}
poll "the backup age check" backup_age_check

# post PATH JSON sends OTLP/HTTP JSON to Alloy from the app network, as the app does.
post() {
  docker exec "$caddy" wget -q -O - --header "Content-Type: application/json" \
    --post-data "$2" "http://alloy:4318$1"
}

marker="spinup-boot-$(random_hex 8)"
now_ns="$(python3 -c 'import time; print(time.time_ns())')"
start_ns=$((now_ns - 600 * 1000000000))
echo "==> push the log line $marker to Alloy"
log_json="$(
  cat <<JSON
{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"spinup-boot"}}]},
"scopeLogs":[{"scope":{"name":"spinup-boot"},"logRecords":[{"timeUnixNano":"$now_ns","observedTimeUnixNano":"$now_ns",
"severityNumber":9,"severityText":"INFO","body":{"stringValue":"boot test $marker"}}]}]}]}
JSON
)"
post /v1/logs "$log_json" >"$work/http/push-logs.txt"

echo "==> the log line appears in Loki"
loki_check() {
  local query
  query="$(urlencode "{service_name=\"spinup-boot\"} |= \"$marker\"")"
  api "/api/datasources/proxy/uid/loki/loki/api/v1/query_range?query=$query&start=$start_ns&end=$((now_ns + 600 * 1000000000))&limit=10" \
    "$work/http/loki.json" && python3 "$check" loki "$work/http/loki.json" "$marker"
}
poll "the Loki check" loki_check

if [[ "$profile" == full ]]; then
  trace_id="$(random_hex 16)"
  span_id="$(random_hex 8)"
  echo "==> push the span $span_id of the trace $trace_id to Alloy"
  span_json="$(
    cat <<JSON
{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"spinup-boot"}}]},
"scopeSpans":[{"scope":{"name":"spinup-boot"},"spans":[{"traceId":"$trace_id","spanId":"$span_id",
"name":"boot test $marker","kind":2,"startTimeUnixNano":"$now_ns","endTimeUnixNano":"$((now_ns + 5000000))"}]}]}]}
JSON
  )"
  post /v1/traces "$span_json" >"$work/http/push-traces.txt"

  echo "==> the span appears in Tempo"
  tempo_check() {
    { api "/api/datasources/proxy/uid/tempo/api/v2/traces/$trace_id" "$work/http/tempo.json" ||
      api "/api/datasources/proxy/uid/tempo/api/traces/$trace_id" "$work/http/tempo.json"; } &&
      python3 "$check" tempo "$work/http/tempo.json" "$trace_id" "$span_id"
  }
  poll "the Tempo check" tempo_check
fi

echo "==> the stack uses less than $budget MiB"
names=()
for service in $services; do
  names+=("$(docker inspect --format '{{.Name}}' "$(compose ps -q "$service")" | sed 's|^/||')")
done
: >"$work/http/stats.txt"
for i in 1 2 3; do
  if ((i > 1)); then
    echo "--" >>"$work/http/stats.txt"
    sleep 5
  fi
  docker stats --no-stream --format '{{.Name}}\t{{.MemUsage}}' "${names[@]}" >>"$work/http/stats.txt"
done
if ! python3 "$check" memory "$work/http/stats.txt" "$budget"; then
  echo "run.sh: the $profile stack uses more memory than its budget of $budget MiB." >&2
  echo "  Try: find the service that grew in the table above, and lower its memory use or fix the budget in docs/contributing/monitoring-boot.md" >&2
  exit 1
fi

echo "PASS: monitoring profile $profile"
