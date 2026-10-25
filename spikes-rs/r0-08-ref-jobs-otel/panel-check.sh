#!/usr/bin/env bash
# panel-check.sh: the † check of R0.8 (needs Docker and PostgreSQL).
#
# It starts the grafana/otel-lgtm image (no pull: the image must be local),
# runs the todo-api with OTLP export to it, sends traffic, and runs every
# query of the monitoring pack dashboard "HTTP RED" (http-red.json) through
# the Grafana Prometheus data source, with job="todo-api". A panel "shows
# data" when its query returns at least one series. It also asks Tempo and
# Loki through Grafana for the todo-api traces and logs.
#
# Usage (from this directory):
#   DATABASE_URL=postgres://postgres:postgres@127.0.0.1:55433/spinup_r08 ./panel-check.sh
# Environment:
#   LGTM_IMAGE  the image (default: the local grafana/otel-lgtm digest below)
#   BIN         the todo-api binary (default: cargo build, then target/debug)
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
image=${LGTM_IMAGE:-grafana/otel-lgtm@sha256:57c04cbd8e7022f32bb84c4ac005d79baf8b08e2c5be44facf11f5f1f9944ba2}
name=r08-lgtm-$$
grafana=http://127.0.0.1:43000
port=48080
: "${DATABASE_URL:?set DATABASE_URL to a scratch database}"

if [[ -z ${BIN:-} ]]; then
  (cd "$here" && cargo build --offline --locked -p todo-api)
  BIN=${CARGO_TARGET_DIR:-$here/target}/debug/todo-api
fi

app=""
cleanup() {
  [[ -n $app ]] && kill "$app" 2>/dev/null || true
  docker rm -f "$name" > /dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --rm --name "$name" --pull never \
  -p 127.0.0.1:43000:3000 -p 127.0.0.1:44318:4318 "$image" > /dev/null
echo "waiting for Grafana and the OTLP receiver"
for _ in $(seq 1 120); do
  if curl -fsS "$grafana/api/health" > /dev/null 2>&1 \
    && curl -fsS -o /dev/null -X POST -H 'content-type: application/x-protobuf' \
      --data-binary '' http://127.0.0.1:44318/v1/metrics 2>/dev/null; then
    break
  fi
  sleep 1
done

"$BIN" migrate up
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:44318 OTEL_METRIC_EXPORT_INTERVAL=5000 \
  PORT=$port LOG_LEVEL=info "$BIN" serve 2> "${TMPDIR:-/tmp}/r08-todo-api.log" &
app=$!
for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:$port/healthz" > /dev/null 2>&1 && break; sleep 0.2; done

echo "sending traffic for 40 s"
end=$((SECONDS + 40))
while ((SECONDS < end)); do
  curl -fsS -o /dev/null -X POST -H 'content-type: application/json' \
    -d '{"title":"t","done":false,"priority":"low"}' "http://127.0.0.1:$port/api/todos" || true
  curl -fsS -o /dev/null "http://127.0.0.1:$port/api/todos?limit=5" || true
  curl -sS -o /dev/null "http://127.0.0.1:$port/api/todos/999999999" || true
  curl -sS -o /dev/null -X POST -H 'content-type: application/json' -d '{}' "http://127.0.0.1:$port/api/todos" || true
  sleep 0.05
done
sleep 15

python3 - "$repo/internal/templates/packs/monitoring/files/monitoring/grafana/dashboards/stack/http-red.json" "$grafana" <<'PY'
import json, sys, urllib.parse, urllib.request
dash, grafana = sys.argv[1], sys.argv[2]
d = json.load(open(dash))
auth = {"Authorization": "Basic YWRtaW46YWRtaW4="}  # admin:admin, the LGTM default
def get(path):
    req = urllib.request.Request(grafana + path, headers=auth)
    with urllib.request.urlopen(req, timeout=20) as r:
        return json.load(r)
failed = 0
for p in d["panels"]:
    for t in p.get("targets", []):
        expr = t["expr"].replace("$job", "todo-api").replace("$__rate_interval", "1m")
        q = urllib.parse.quote(expr)
        res = get(f"/api/datasources/proxy/uid/prometheus/api/v1/query?query={q}")
        n = len(res["data"]["result"])
        sample = res["data"]["result"][0]["value"][1] if n else "-"
        five_xx = "5.." in expr
        ok = n > 0 or five_xx
        failed += 0 if ok else 1
        print(f"{'ok  ' if ok else 'FAIL'} {p['title']}: {n} series, first value {sample}"
              + (" (no 5xx traffic: empty is expected)" if five_xx and n == 0 else ""))
jobs = get("/api/datasources/proxy/uid/prometheus/api/v1/label/job/values")["data"]
print("job label values:", jobs)
traces = get("/api/datasources/proxy/uid/tempo/api/search?tags=" + urllib.parse.quote("service.name=todo-api") + "&limit=5")
names = sorted({t.get("rootTraceName", "") for t in traces.get("traces", [])})
print(f"tempo: {len(traces.get('traces', []))} traces, root names {names}")
logs = get("/api/datasources/proxy/uid/loki/loki/api/v1/query_range?query=" + urllib.parse.quote('{service_name="todo-api"}') + "&limit=5")
print(f"loki: {sum(len(s['values']) for s in logs['data']['result'])} log lines in the first page")
if failed or "todo-api" not in jobs or not names:
    sys.exit(1)
print("panel check: ok")
PY
