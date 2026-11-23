# shellcheck shell=bash disable=SC2154
# The console stage of the Docker-in-Docker suite (DIND_CONSOLE=1, ADR 0037).
#
# run.sh sources this file after it builds the CLI and the VM, writes the SSH identity, generates
# the app with the monitoring (lite), backups (pgdump), jobs, and console modules, sets the
# secrets, and prepares /opt/<app>. It reuses the helpers and the trap of run.sh.
#
# Stages (each prints its name):
#   ConsoleFirstDeploy    deploy, the installed console, isolation, status, backup, WAL, jobs, Heartbeat
#   ConsoleTunnels        two console open commands, the printed URL, links, and forward ownership
#   ConsoleLogParity      OTLP records, the four log filters, the Explore window, Grafana, and the CLI
#   ConsoleWithStoppedApp the live color stops, the console stays, and the page reports the state
#   ConsoleFailedDeploy   a failed deploy, a good deploy, both action commands, and the deploy lock
#
# The stage reads every value through the real CLI or the real forwards. It only reads the page:
# it sends no request other than GET to the console.

c_env=staging
c_web="$app-console-console-1"
c_relay="$app-console-console-query-1"
c_dir="/opt/$app/console"

c_py="$work/console_check.py"
cat >"$c_py" <<'PY'
# console_check.py <mode> <args...> checks and prints console documents for console.sh.
import html
import html.parser
import json
import os
import re
import shlex
import sys
import urllib.parse
from datetime import datetime, timezone

mode, args = sys.argv[1], sys.argv[2:]


def fail(message):
    print("console_check %s: %s" % (mode, message), file=sys.stderr)
    sys.exit(1)


def load(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def integer(label, value, minimum=0):
    if type(value) is not int or value < minimum:
        fail("%s is %r, want an integer of at least %d" % (label, value, minimum))
    return value


def ok(doc, name):
    s = doc[name]
    if s["state"] != "ok" or s["reason"] is not None or s["at"] is None:
        fail("section %s is %r, want ok with a time" % (name, {k: s[k] for k in ("state", "reason", "at")}))
    return s["data"]


def nanos(text):
    """RFC3339Nano UTC text to Unix nanoseconds, without float rounding."""
    m = re.fullmatch(r"(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?Z", text)
    if not m:
        fail("time %r is not RFC3339Nano UTC" % text)
    seconds = int(datetime.strptime(m.group(1), "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp())
    return seconds * 10**9 + int((m.group(2) or "0").ljust(9, "0"))


HOST = ["release", "containers", "backup", "wal", "jobs", "disk", "memory", "history"]
SECTIONS = ["release", "containers", "alerts", "backup", "wal", "jobs", "disk", "memory", "history"]


class Page(html.parser.HTMLParser):
    def __init__(self):
        super().__init__()
        self.links, self.pre, self.times, self._pre, self._time = [], [], [], None, None

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag == "a":
            self.links.append(a.get("href", ""))
        elif tag == "pre":
            self._pre = ""
        elif tag == "time":
            self._time = ""

    def handle_endtag(self, tag):
        if tag == "pre" and self._pre is not None:
            self.pre.append(self._pre)
            self._pre = None
        elif tag == "time" and self._time is not None:
            self.times.append(self._time)
            self._time = None

    def handle_data(self, data):
        if self._pre is not None:
            self._pre += data
        if self._time is not None:
            self._time += data


def page(path):
    p = Page()
    with open(path, encoding="utf-8") as f:
        p.feed(f.read())
    return p


if mode == "field":
    value = load(args[0])
    for key in args[1].split("."):
        value = value[int(key)] if isinstance(value, list) else value[key]
    print(value if isinstance(value, str) else json.dumps(value))
elif mode == "overview":
    # overview <file> <app> <env>: the identity and the shape of every section.
    doc, app, env = load(args[0]), args[1], args[2]
    if doc["version"] != 1 or doc["app"] != app or doc["env"] != env or doc["profile"] != "lite":
        fail("identity %r" % {k: doc[k] for k in ("version", "app", "env", "profile")})
    if not isinstance(doc["observed_at"], str):
        fail("observed_at is %r" % doc["observed_at"])
    for name in SECTIONS:
        if sorted(doc[name]) != ["at", "data", "reason", "state"]:
            fail("section %s has keys %r" % (name, sorted(doc[name])))
    if doc["wal"] != {"state": "disabled", "reason": "no_wal", "at": None, "data": None}:
        fail("the WAL tile with pgdump is %r, want disabled/no_wal" % doc["wal"])
    for name in ("disk", "memory"):
        s = ok(doc, name)
        total, used, avail = (integer(name + "." + k, s[k + "_bytes"]) for k in ("total", "used", "available"))
        if total == 0 or used + avail > total:
            fail("%s is inconsistent: %r" % (name, s))
    m = ok(doc, "memory")
    if m["used_bytes"] != m["total_bytes"] - m["available_bytes"]:
        fail("memory used is not total minus available: %r" % m)
elif mode == "release":
    # release <file>: id color previous_id age_seconds, or "null".
    data = ok(load(args[0]), "release")
    if data is None:
        print("null")
    else:
        integer("release.age_seconds", data["age_seconds"])
        print(data["id"], data["color"], data["previous_id"] or "null", data["git_sha"])
elif mode == "containers":
    for row in ok(load(args[0]), "containers"):
        print(row["project"], row["service"], row["name"], row["state"], row["health"] or "null")
elif mode == "history":
    for row in ok(load(args[0]), "history"):
        print(row["id"], row["result"], row["user"] or "null", row["git_sha"] or "null", row["color"] or "null", row["at"])
elif mode == "backup":
    data = ok(load(args[0]), "backup")
    if data is None:
        print("null")
    else:
        size = data["size_bytes"]
        if size is not None:
            integer("backup.size_bytes", size)
        print(data["backend"], data["label"], data["kind"], data["finished_at"], "null" if size is None else size)
elif mode == "alerts":
    for row in ok(load(args[0]), "alerts"):
        print(row["name"], row["severity"] or "null", row["since"] or "null")
elif mode == "jobs":
    # jobs <overview> <psql rows queue|state|count>: the displayed counts equal the SQL counts.
    shown = [(r["queue"], r["state"], integer("count", r["count"], 1)) for r in ok(load(args[0]), "jobs")]
    with open(args[1], encoding="utf-8") as f:
        sql = [(q, s, int(c)) for q, s, c in (line.split("|") for line in f.read().split("\n") if line)]
    if shown != sql:
        fail("the displayed queues differ from SQL\n  shown: %r\n  sql:   %r" % (shown, sql))
elif mode == "space":
    # space <overview> <df -Pk output> <MemTotal kB>: the totals equal the VM counters.
    doc = load(args[0])
    with open(args[1], encoding="utf-8") as f:
        fields = f.read().split("\n")[1].split()
    if ok(doc, "disk")["total_bytes"] != int(fields[1]) * 1024:
        fail("disk total %r, want %d" % (ok(doc, "disk")["total_bytes"], int(fields[1]) * 1024))
    if ok(doc, "memory")["total_bytes"] != int(args[2]) * 1024:
        fail("memory total %r, want %d" % (ok(doc, "memory")["total_bytes"], int(args[2]) * 1024))
elif mode == "same":
    # same <a> <b>: exit 3 when the samples differ. Else the host sections and alert names agree.
    a, b = load(args[0]), load(args[1])
    if a["observed_at"] != b["observed_at"]:
        sys.exit(3)
    for name in HOST:
        if a[name] != b[name]:
            fail("section %s differs at the common sample time %s\n  a: %r\n  b: %r" % (name, a["observed_at"], a[name], b[name]))
    names = lambda d: sorted(r["name"] for r in (d["alerts"]["data"] or []))
    if a["alerts"]["state"] != b["alerts"]["state"] or names(a) != names(b):
        fail("the alerts differ: %r and %r" % (a["alerts"], b["alerts"]))
elif mode == "text":
    # text <status text> <overview>: the terminal form of the same document.
    with open(args[0], encoding="utf-8") as f:
        lines = f.read().split("\n")
    doc = load(args[1])
    if lines[0] != "%s (%s)" % (doc["app"], doc["env"]) or lines[1] != "Observed: " + doc["observed_at"]:
        fail("the text header is %r" % lines[:2])
    captions = [l.split(" [")[0] for l in lines[2:] if l and not l.startswith("  ")]
    want = ["Live release", "Containers", "Firing alerts", "Last backup", "WAL archive", "Job queues", "Disk", "Memory", "Deploy history"]
    if captions != want:
        fail("the text captions are %r" % captions)
    if "WAL archive [disabled: no_wal]" not in lines:
        fail("the text has no disabled WAL caption")
    rel = doc["release"]["data"]
    if rel and '  id="%s"' % rel["id"] not in lines:
        fail("the text has no release id row")
elif mode == "links":
    # links <html> <grafana port> <public url> <repo docs dir> <env>
    p, port, public, docs, env = page(args[0]), args[1], args[2], args[3], args[4]
    grafana = "http://127.0.0.1:%s" % port
    dashboards = sorted(l[len(grafana):] for l in p.links if l.startswith(grafana + "/"))
    want = sorted("/d/" + uid for uid in ("spinup-http-red", "spinup-go-runtime", "spinup-caddy", "spinup-backups", "spinup-deploys"))
    if dashboards != want:
        fail("the Grafana links are %r, want %r" % (dashboards, want))
    prefix = "https://raw.githubusercontent.com/stopyellingatme/spinup/main/docs/"
    for link in p.links:
        if link.startswith("https://"):
            if not link.startswith(prefix) or not os.path.isfile(os.path.join(docs, link[len(prefix):])):
                fail("the documentation link %r has no page in docs/" % link)
    origins = [l for l in p.links if l.startswith("http://localhost")]
    if [o.rstrip("/") for o in origins] != [public.rstrip("/")]:
        fail("the app links are %r, want %r" % (origins, public))
    others = [l for l in p.links if not (l.startswith(grafana + "/") or l.startswith(prefix) or l in origins or l.startswith("/") or l.startswith("#"))]
    if others:
        fail("unexpected links %r" % others)
    for text in ("spinup monitor open --env " + env, "spinup status --env " + env):
        if text not in p.pre:
            fail("the page has no command %r" % text)
    for l in p.links:
        if l.startswith("/") and "grafana_port=" + port not in l and l not in ("/assets/console.css",):
            fail("the console link %r drops grafana_port" % l)
    print("\n".join(dashboards))
elif mode == "commands":
    # commands <html>: print each action command shown as plain text, one per line.
    p = page(args[0])
    for text in p.pre:
        if text.startswith("spinup backup now") or text.startswith("spinup rollback"):
            print(text)
elif mode == "logs":
    # logs <logs.json>: one "service|level|line" row per entry, and checks of the document.
    doc = load(args[0])
    if doc["version"] != 1 or not isinstance(doc["more"], bool) or not doc["explore_path"].startswith("/explore?schemaVersion=1&panes="):
        fail("the log document is %r" % {k: doc[k] for k in ("version", "more", "explore_path")})
    nanos(doc["start"]), nanos(doc["end"])
    for e in doc["entries"]:
        nanos(e["at"])
        print("%s|%s|%s" % (e["service"], e["level"] if e["level"] is not None else "null", e["line"]))
elif mode == "explore":
    # explore <logs.html> <grafana port> <out dir>: the Explore link keeps the page window.
    p, port, out = page(args[0]), args[1], args[2]
    with open(args[0], encoding="utf-8") as f:
        text = f.read()
    m = re.search(r"Window: <time>([^<]+)</time> to <time>([^<]+)</time>", text)
    if not m:
        fail("the logs page shows no window")
    start, end = nanos(m.group(1)), nanos(m.group(2))
    links = [l for l in p.links if l.startswith("http://127.0.0.1:%s/explore?" % port)]
    if len(links) != 1:
        fail("the logs page has %d Explore links through the forward, want 1: %r" % (len(links), p.links))
    url = urllib.parse.urlsplit(links[0])
    if not url.query.startswith("schemaVersion=1&panes=") or not url.query.endswith("&orgId=1"):
        fail("the Explore query is %r" % url.query)
    panes = json.loads(urllib.parse.parse_qs(url.query)["panes"][0])
    pane = panes["a"]
    query = pane["queries"][0]
    if pane["datasource"] != "loki" or query["refId"] != "A" or query["datasource"] != {"type": "loki", "uid": "loki"}:
        fail("the Explore pane is %r" % pane)
    want_from, want_to = start // 10**6, -(-end // 10**6)
    if pane["range"] != {"from": str(want_from), "to": str(want_to)}:
        fail("the Explore range is %r, want the page window %d..%d" % (pane["range"], want_from, want_to))
    queries = [t for t in p.pre if t.startswith("{")]
    if queries != [query["expr"]]:
        fail("the page query %r is not the Explore expression %r" % (queries, query["expr"]))
    commands = [t for t in p.pre if t.startswith("spinup monitor logs ")]
    if len(commands) != 1:
        fail("the logs page has %d log commands" % len(commands))
    argv = shlex.split(commands[0])
    if argv[argv.index("--query") + 1] != query["expr"]:
        fail("the CLI query %r is not the Explore expression %r" % (argv, query["expr"]))
    with open(os.path.join(out, "explore.url"), "w") as f:
        f.write(links[0])
    with open(os.path.join(out, "explore.query"), "w") as f:
        f.write(urllib.parse.urlencode({"query": query["expr"], "start": start, "end": end, "limit": 101, "direction": "backward"}))
    with open(os.path.join(out, "explore.argv"), "wb") as f:
        f.write(b"\0".join(a.encode() for a in argv[1:]) + b"\0")
    print(start, end)
elif mode == "entries":
    # entries <logs.html>: one "service|level|line" row per result on the page.
    with open(args[0], encoding="utf-8") as f:
        text = f.read()
    for m in re.finditer(r'<article class="log"><h3><time>[^<]+</time> · ([^<]*?) · ([^<]*?)</h3><pre>([^<]*)</pre></article>', text):
        print("%s|%s|%s" % tuple(html.unescape(g) for g in m.groups()))
elif mode == "loki":
    # loki <query_range.json>: one line per log entry of a Loki answer.
    doc = load(args[0])
    if doc.get("status") != "success":
        fail("Loki answered %r" % doc.get("status"))
    for stream in doc["data"]["result"]:
        for value in stream["values"]:
            print(value[1])
elif mode == "prom-alerts":
    doc = load(args[0])
    for a in doc["data"]["alerts"]:
        if a["state"] == "firing":
            print(a["labels"]["alertname"])
elif mode == "isolation":
    # isolation <web inspect> <relay inspect> <app>: users, limits, networks, mounts, and ports.
    web, relay, app = load(args[0])[0], load(args[1])[0], args[2]
    for name, c, memory, gomem in (("console", web, 64 << 20, "40MiB"), ("console-query", relay, 48 << 20, "28MiB")):
        h, cfg = c["HostConfig"], c["Config"]
        checks = {
            "user": (cfg["User"], "65532:65532"), "read-only root": (h["ReadonlyRootfs"], True),
            "cap drop": (h["CapDrop"], ["ALL"]), "no new privileges": (h["SecurityOpt"], ["no-new-privileges:true"]),
            "pids": (h["PidsLimit"], 32), "cpus": (h["NanoCpus"], 250000000), "memory": (h["Memory"], memory),
            "privileged": (h["Privileged"], False), "pid mode": (h["PidMode"], ""), "ipc host": (h["IpcMode"] == "host", False),
            "GOMEMLIMIT": ("GOMEMLIMIT=" + gomem in cfg["Env"], True), "restart": (h["RestartPolicy"]["Name"], "unless-stopped"),
            "log driver": (h["LogConfig"]["Type"], "json-file"),
        }
        for label, (got, want) in checks.items():
            if got != want:
                fail("%s %s is %r, want %r" % (name, label, got, want))
        if "docker.sock" in json.dumps(c["Mounts"]) or "docker.sock" in json.dumps(h.get("Binds")):
            fail("%s mounts a Docker socket" % name)
    if sorted(web["NetworkSettings"]["Networks"]) != [app + "-console"]:
        fail("the console networks are %r" % sorted(web["NetworkSettings"]["Networks"]))
    if sorted(relay["NetworkSettings"]["Networks"]) != sorted([app + "-console", app + "-monitoring"]):
        fail("the relay networks are %r" % sorted(relay["NetworkSettings"]["Networks"]))
    mounts = [(m["Source"], m["Destination"], m["RW"]) for m in web["Mounts"]]
    if mounts != [("/opt/%s/console/data" % app, "/data", False)]:
        fail("the console mounts are %r" % mounts)
    if relay["Mounts"]:
        fail("the relay mounts %r" % relay["Mounts"])
    bound = {p: b for p, b in web["NetworkSettings"]["Ports"].items() if b}
    if bound != {"7410/tcp": [{"HostIp": "127.0.0.1", "HostPort": "7410"}]}:
        fail("the console publishes %r" % bound)
    if any(b for b in (relay["NetworkSettings"]["Ports"] or {}).values()):
        fail("the relay publishes %r" % relay["NetworkSettings"]["Ports"])
else:
    fail("unknown mode")
PY
cj() { python3 -I "$c_py" "$@"; }

# vm_ssh <command...> runs a command as the deploy user over SSH, as the CLI does.
vm_ssh() { ssh -F "$work/ssh_config" -o BatchMode=yes "$host_alias" "$@"; }

# vm_psql <sql> runs a query in the base PostgreSQL container with local socket authentication.
vm_psql() { vm_docker exec -i "$app-postgres-1" psql -X -w -U app -d app -tA -v ON_ERROR_STOP=1 -c "$1"; }

# c_status <name> writes spinup status --json to $work/<name>.json. Only stdout is the document.
c_status() {
  local code=0
  (cd "$work/$app" && "$work/spinup" status --env "$c_env" --json) >"$work/$1.json" 2>"$logs/$1.err" || code=$?
  if [[ $code -ne 0 ]]; then
    sed 's/^/    | /' "$logs/$1.err" >&2
    fail "spinup status --json exited $code" "Try: read $logs/$1.err"
  fi
}

# facts_time prints observed_at of the published facts on the VM.
facts_time() { docker exec "$vm" cat "$c_dir/data/facts.json" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["observed_at"])'; }

# new_sample waits for a new observation. It forces one sample over SSH, as deploy does after post.
new_sample() {
  local before after
  before="$(facts_time)"
  sleep 1.2
  vm_ssh "$c_dir/collect.sh" || fail "the forced console sample failed"
  after="$(facts_time)"
  [[ "$after" != "$before" ]] || fail "the forced sample did not publish a new observation" "observed_at: $after"
  echo "    new observation $after (was $before)"
}

# c_get <path> prints a console page through the forward of the open console command.
c_get() { curl -fsS -m 15 "http://127.0.0.1:$c_port$1"; }

# g_get <path> prints a Grafana answer through the second forward, with the Grafana admin login.
g_get() { curl -fsS -m 15 -u "admin:$grafana_password" "http://127.0.0.1:$g_port$1"; }

# common_sample <name>: the page and spinup status at one sample time. It retries while a sample
# changes between the reads, which is a legitimate change.
common_sample() {
  local name=$1 i code
  for i in 1 2 3 4 5; do
    c_get /api/v1/overview >"$work/$name-page.json" || fail "the console page does not answer through the forward"
    c_status "$name-cli"
    code=0
    cj same "$work/$name-page.json" "$work/$name-cli.json" || code=$?
    if [[ $code -eq 0 ]]; then
      echo "    the page and spinup status agree at $(cj field "$work/$name-cli.json" observed_at)"
      return 0
    fi
    [[ $code -eq 3 ]] || fail "the page and spinup status disagree at one sample time"
    echo "    a new sample arrived between the reads, try $i"
    sleep 2
  done
  fail "the page and spinup status never shared a sample time"
}

# open_console <name> starts spinup console open in the foreground of a background job. It reads
# the URL from the printed output and sets c_url, c_port, g_port, and c_pid.
open_console() {
  local name=$1 line="" i
  (cd "$work/$app" && exec "$work/spinup" console open --env "$c_env" --no-browser) \
    >"$logs/$name.out" 2>"$logs/$name.err" &
  c_pid=$!
  pids+=("$c_pid")
  for i in $(seq 1 120); do
    line="$(sed -n 's/^Console: //p' "$logs/$name.out" | head -n 1)"
    [[ -n "$line" ]] && break
    kill -0 "$c_pid" 2>/dev/null || { sed 's/^/    | /' "$logs/$name.err" >&2; fail "spinup console open exited before it printed the URL"; }
    sleep 0.5
  done
  [[ "$line" =~ ^http://127\.0\.0\.1:([0-9]+)/\?grafana_port=([0-9]+)$ ]] ||
    { sed 's/^/    | /' "$logs/$name.err" >&2; fail "spinup console open printed no console URL with a Grafana port" "got: ${line:-nothing}"; }
  c_url="$line" c_port="${BASH_REMATCH[1]}" g_port="${BASH_REMATCH[2]}"
  grep -q '^Press Ctrl-C to close the tunnels\.$' "$logs/$name.out" || fail "spinup console open does not explain how to close the tunnels"
  echo "    $name printed $c_url"
}

# close_console <pid> interrupts one console open command and checks that it exits 0.
close_console() {
  local pid=$1 code=0
  kill -INT "$pid"
  wait "$pid" || code=$?
  [[ $code -eq 0 ]] || fail "spinup console open exited $code after an interrupt, want 0"
  local rest=()
  for p in "${pids[@]}"; do [[ "$p" == "$pid" ]] || rest+=("$p"); done
  pids=(${rest[@]+"${rest[@]}"})
}

# console_started prints StartedAt and RestartCount of the console container.
console_started() { vm_docker inspect -f '{{.State.StartedAt}} {{.RestartCount}} {{.State.Status}}' "$c_web"; }

# send_logs <marker> sends four OTLP records to Alloy from the Caddy container, the path of the app:
# worker WARN now, worker without a severity now, worker ERROR 30 minutes ago, and api WARN now.
send_logs() {
  local payload
  payload="$(python3 -I - "$app" "$1" <<'PY'
import json, sys, time
app, marker = sys.argv[1], sys.argv[2]
now = time.time_ns()
def record(body, at, severity=None):
    r = {"timeUnixNano": str(at), "body": {"stringValue": body}}
    if severity:
        r["severityText"], r["severityNumber"] = severity
    return r
def resource(service, records):
    return {"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": service}}]},
            "scopeLogs": [{"scope": {"name": "dind"}, "logRecords": records}]}
print(json.dumps({"resourceLogs": [
    resource(app + "-worker", [record("worker A " + marker, now, ("WARN", 13)), record("worker B " + marker, now + 1000),
                               record("worker C " + marker, now - 30 * 60 * 10**9, ("ERROR", 17))]),
    resource(app + "-api", [record("api D " + marker, now + 2000, ("WARN", 13))]),
]}))
PY
)"
  vm_docker exec "$app-caddy-1" wget -q -O /dev/null --header 'Content-Type: application/json' \
    --post-data "$payload" http://alloy:4318/v1/logs || fail "Alloy did not accept the OTLP records"
}

# log_rows <query> prints service|level|line for the console log search with these filters.
log_rows() {
  c_get "/api/v1/logs?$1" >"$work/logs.json" || fail "the log search $1 failed"
  cj logs "$work/logs.json" | sort
}

expect_rows() {
  local query=$1 want=$2 got
  got="$(log_rows "$query")"
  [[ "$got" == "$want" ]] || fail "the log search $query returned the wrong lines" "want: ${want//$'\n'/ / }" "got:  ${got//$'\n'/ / }"
  echo "    $query: $(printf '%s\n' "$got" | grep -c .) lines"
}

# The forwards need sshd to permit local TCP forwarding, as Debian and Ubuntu do by default.
sshd_settings="$(docker exec "$vm" /usr/sbin/sshd -T 2>/dev/null)"
grep -qx 'allowtcpforwarding local' <<<"$sshd_settings" ||
  fail "sshd of the VM does not permit local TCP forwarding" "Try: check testdata/dind/vm/sshd_config"

# ---------------------------------------------------------------------------------------------
step "console 1. ConsoleFirstDeploy"
rel1="$(new_release)"
sp c-deploy-1 deploy --env "$c_env" --skip-build --tag "$rel1" --yes || { show_log c-deploy-1 80; fail "the first deploy with the console failed"; }
if grep -q 'Console sample delayed' "$logs/c-deploy-1.log"; then show_log c-deploy-1; fail "the first deploy delayed the final console sample"; fi
http_get /healthz >/dev/null || fail "/healthz does not answer after the first deploy"
for c in "$c_web" "$c_relay"; do
  health=""
  for _ in $(seq 1 40); do
    health="$(vm_docker inspect -f '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}' "$c" 2>/dev/null || true)"
    [[ "$health" == "running healthy" ]] && break
    sleep 1
  done
  [[ "$health" == "running healthy" ]] || fail "$c is not running and healthy" "state: ${health:-missing}"
done
echo "    $c_web and $c_relay run and are healthy"

# Isolation: users, limits, the dedicated bridge, mounts, the loopback port, and no secrets.
vm_docker inspect "$c_web" >"$work/inspect-web.json"
vm_docker inspect "$c_relay" >"$work/inspect-relay.json"
cj isolation "$work/inspect-web.json" "$work/inspect-relay.json" "$app"
members="$(vm_docker network inspect -f '{{range .Containers}}{{println .Name}}{{end}}' "$app-console" | grep . | sort)"
[[ "$members" == "$(printf '%s\n%s' "$c_web" "$c_relay" | sort)" ]] ||
  fail "the console bridge carries other containers" "members: ${members//$'\n'/ }"
listen="$(docker exec "$vm" netstat -tln | awk '$4 ~ /:7410$/ {print $4}' | sort -u)"
[[ "$listen" == "127.0.0.1:7410" ]] || fail "the VM listens for the console on ${listen:-nothing}, want 127.0.0.1:7410 only"
vm_ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$vm" | head -n 1)"
if docker exec "$vm" curl -fsS -m 3 "http://$vm_ip:7410/healthz" >/dev/null 2>&1; then
  fail "the console answers on the VM address $vm_ip"
fi
for path in /api/v1/overview /logs /assets/console.css; do
  body="$(curl -sS -m 10 "http://localhost:$http_port$path" || true)"
  [[ "$body" != *observed_at* && "$body" != *"SPINUP / CONSOLE"* && "$body" != *".tile"* ]] ||
    fail "the public edge serves the console route $path"
done
for value in "$db_password" "$grafana_password"; do
  grep -qF "$value" "$work/inspect-web.json" "$work/inspect-relay.json" && fail "a console container has a secret value in its configuration"
  if docker exec "$vm" grep -rqF "$value" "$c_dir"; then fail "$c_dir holds a secret value"; fi
done
modes="$(docker exec "$vm" sh -c "cd $c_dir && stat -c '%n %a %U' collect.sh probe.conf .env compose.console.yml compose.query.yml data data/facts.json")"
want_modes="collect.sh 700 deploy
probe.conf 600 deploy
.env 600 deploy
compose.console.yml 644 deploy
compose.query.yml 644 deploy
data 755 deploy
data/facts.json 644 deploy"
[[ "$modes" == "$want_modes" ]] || fail "the console files have the wrong modes" "got: ${modes//$'\n'/ | }"
if vm_docker ps -a --format '{{.Names}}' | grep -q -- '-console-project-'; then fail "a projector container outlived its sample"; fi
echo "    isolation: 65532, read-only, no capabilities, own bridge, /data:ro, 127.0.0.1:7410 only, no secrets"

c_status c-first
cj overview "$work/c-first.json" "$app" "$c_env"
read -r id color previous sha <<<"$(cj release "$work/c-first.json")"
[[ "$id" == "$rel1" && "$previous" == null && "$sha" == "${rel1##*-}"* ]] ||
  fail "the release section is wrong" "got: $id $color $previous $sha" "want: $rel1 with no previous release"
live="$(live_projects)"
[[ "$live" == "$app-$color" ]] || fail "the release color $color is not the live project $live"
cj containers "$work/c-first.json" >"$work/c-first.containers"
for row in "$app caddy" "$app postgres" "$live api" "$live worker" "$app-monitoring grafana" "$app-monitoring prometheus" \
  "$app-monitoring loki" "$app-monitoring alloy" "$app-console console" "$app-console console-query"; do
  grep -q "^$row [^ ]* running " "$work/c-first.containers" || { cat "$work/c-first.containers" >&2; fail "the containers section has no running $row"; }
done
grep -q "^$live api [^ ]* running healthy$" "$work/c-first.containers" || fail "the api row is not healthy"
grep -q "^$app-monitoring loki [^ ]* running null$" "$work/c-first.containers" || fail "a container without a health check must report null health"
if grep -q -- '-console-project-' "$work/c-first.containers"; then fail "the sample lists its own projector"; fi
[[ "$(cj backup "$work/c-first.json")" == null ]] || fail "the first deploy reports a backup before any dump"
cj history "$work/c-first.json" >"$work/c-first.history"
[[ "$(awk '{print $1, $2}' "$work/c-first.history")" == "$rel1 succeeded" ]] || { cat "$work/c-first.history" >&2; fail "the history does not hold one succeeded attempt"; }
docker exec "$vm" df -Pk "/opt/$app" >"$work/df.txt"
memtotal="$(docker exec "$vm" awk '/^MemTotal:/ {print $2}' /proc/meminfo)"
cj space "$work/c-first.json" "$work/df.txt" "$memtotal"
echo "    status: release $rel1 ($color), containers, no backup yet, WAL disabled, disk and memory totals match the VM"

(cd "$work/$app" && "$work/spinup" status --env "$c_env") >"$work/c-first.txt" 2>"$logs/c-first-text.err" ||
  fail "spinup status (text) failed"
c_status c-first-json
cj text "$work/c-first.txt" "$work/c-first-json.json" ||
  { # A new sample can arrive between the two reads. Read both again once.
    (cd "$work/$app" && "$work/spinup" status --env "$c_env") >"$work/c-first.txt" 2>>"$logs/c-first-text.err"
    c_status c-first-json
    cj text "$work/c-first.txt" "$work/c-first-json.json"; }
echo "    status text: identity, observation time, nine captions, and the disabled WAL caption"

# Prometheus reports the Heartbeat rule by name. It fires at the first evaluation (30 s).
found=""
for _ in $(seq 1 30); do
  c_status c-alerts
  if cj alerts "$work/c-alerts.json" 2>/dev/null | grep -q '^Heartbeat none '; then found=1; break; fi
  sleep 5
done
[[ -n "$found" ]] || { cat "$work/c-alerts.json" >&2; fail "the alerts section never reported Heartbeat"; }
echo "    alerts: Heartbeat (severity none) fires"

# River jobs: three scheduled and two discarded jobs in an own queue. No worker takes them.
vm_psql "INSERT INTO river_job (args, kind, queue, state, max_attempts, scheduled_at)
  SELECT '{}', 'console_gate', 'console_gate', 'scheduled', 1, now() + interval '1 day' FROM generate_series(1, 3);
INSERT INTO river_job (args, kind, queue, state, max_attempts, finalized_at)
  SELECT '{}', 'console_gate', 'console_gate', 'discarded', 1, now() FROM generate_series(1, 2);" >/dev/null ||
  fail "cannot insert the River jobs"
jobs_sql="SELECT queue, state::text, count(*) FROM public.river_job GROUP BY queue, state ORDER BY queue COLLATE \"C\", state::text COLLATE \"C\""
matched=""
for _ in 1 2 3 4 5; do
  vm_psql "$jobs_sql" >"$work/jobs-before.txt"
  new_sample
  vm_psql "$jobs_sql" >"$work/jobs-after.txt"
  c_status c-jobs
  if cmp -s "$work/jobs-before.txt" "$work/jobs-after.txt"; then
    cj jobs "$work/c-jobs.json" "$work/jobs-after.txt"
    matched=1
    break
  fi
  echo "    the queues changed during the sample, read again"
done
[[ -n "$matched" ]] || fail "the River queues never stayed still for one sample"
if ! { grep -qx 'console_gate|discarded|2' "$work/jobs-after.txt" && grep -qx 'console_gate|scheduled|3' "$work/jobs-after.txt"; }; then
  fail "SQL does not show the inserted jobs"
fi
echo "    jobs: the displayed queue counts equal SQL ($(tr '\n' ' ' <"$work/jobs-after.txt"))"
echo "    ConsoleFirstDeploy: ok"

# ---------------------------------------------------------------------------------------------
step "console 2. ConsoleTunnels"
open_console console-open-a
a_pid=$c_pid a_port=$c_port a_gport=$g_port
open_console console-open-b
b_pid=$c_pid
[[ "$c_port" != "$a_port" && "$g_port" != "$a_gport" ]] || fail "two console open commands share a local port"
for p in "$a_port" "$a_gport" "$c_port" "$g_port"; do
  (( p >= 1024 )) || fail "the local port $p is not ephemeral"
done
[[ "$(curl -fsS -m 5 "http://127.0.0.1:$c_port/healthz")" == ok ]] || fail "/healthz does not answer through the console forward"
g_get /api/health | grep -q '"database": *"ok"' || fail "Grafana does not answer through the second forward"
curl -fsS -m 10 -o "$work/page.html" "$c_url" || fail "the printed console URL does not answer"
cj links "$work/page.html" "$g_port" "http://localhost:$http_port" "$repo/docs" "$c_env" >"$work/dashboards.txt"
while IFS= read -r path; do
  g_get "/api/dashboards/uid/${path#/d/}" >/dev/null || fail "the dashboard link $path does not exist in Grafana"
  code="$(curl -sS -m 10 -o /dev/null -w '%{http_code}' -u "admin:$grafana_password" "http://127.0.0.1:$g_port$path")"
  [[ "$code" == 200 ]] || fail "the dashboard link $path answers $code through the second forward"
done <"$work/dashboards.txt"
echo "    the page links five Grafana dashboards on 127.0.0.1:$g_port, the app origin, and existing docs pages"
code="$(curl -sS -m 10 -o /dev/null -w '%{http_code}' -H "Origin: http://evil.example" "http://127.0.0.1:$c_port/")"
[[ "$code" == 403 ]] || fail "a foreign Origin answered $code through the forward, want 403"
code="$(curl -sS -m 10 -o /dev/null -w '%{http_code}' -H 'Host: console.example.com' "http://127.0.0.1:$c_port/")"
[[ "$code" == 403 ]] || fail "a foreign Host answered $code through the forward, want 403"
# Interrupt the first command. It closes only its own forwards. The second remains active.
close_console "$a_pid"
if curl -fsS -m 3 "http://127.0.0.1:$a_port/healthz" >/dev/null 2>&1; then fail "the interrupted command left its console forward open"; fi
if curl -fsS -m 3 "http://127.0.0.1:$a_gport/api/health" >/dev/null 2>&1; then fail "the interrupted command left its Grafana forward open"; fi
[[ "$(curl -fsS -m 5 "http://127.0.0.1:$c_port/healthz")" == ok ]] || fail "closing one console command closed the forward of another"
g_get /api/health >/dev/null || fail "closing one console command closed the Grafana forward of another"
echo "    an interrupt closes only the forwards of that command"
echo "    ConsoleTunnels: ok"

# ---------------------------------------------------------------------------------------------
step "console 3. ConsoleLogParity"
marker="dindmark$run_id$RANDOM"
send_logs "$marker"
for _ in $(seq 1 45); do
  [[ "$(log_rows "text=$marker&since=1h" | grep -c .)" -ge 4 ]] && break
  sleep 2
done
w="$app-worker" a="$app-api"
# The record without a severity has no detected level. It matches only the unfiltered searches.
b_row="$(log_rows "text=worker%20B%20$marker&since=1h")"
[[ "$b_row" == "$w|"*"|worker B $marker" ]] || fail "the record without a severity is missing" "got: $b_row"
b_level="$(cut -d'|' -f2 <<<"$b_row")"
case "$b_level" in debug | info | warn | error) fail "the record without a severity has the level $b_level" ;; esac
expect_rows "text=$marker&since=1h" "$(printf '%s\n' "$a|warn|api D $marker" "$b_row" "$w|error|worker C $marker" "$w|warn|worker A $marker" | sort)"
expect_rows "text=$marker&since=15m" "$(printf '%s\n' "$a|warn|api D $marker" "$b_row" "$w|warn|worker A $marker" | sort)"
expect_rows "service=$w&text=$marker&since=1h" "$(printf '%s\n' "$b_row" "$w|error|worker C $marker" "$w|warn|worker A $marker" | sort)"
expect_rows "level=warn&text=$marker&since=1h" "$(printf '%s\n' "$a|warn|api D $marker" "$w|warn|worker A $marker" | sort)"
expect_rows "level=error&text=$marker&since=1h" "$w|error|worker C $marker"
expect_rows "level=info&text=$marker&since=24h" ""
expect_rows "level=debug&text=$marker&since=24h" ""
expect_rows "service=$w&level=warn&text=$marker&since=15m" "$w|warn|worker A $marker"
echo "    the service, level, since, and text filters select exactly the OTLP records (level of B: $b_level)"

# The logs page, its Explore link, Grafana, and the CLI read the same absolute window.
c_get "/logs?service=$w&since=15m&text=$marker&grafana_port=$g_port" >"$work/logs.html" || fail "the logs page does not answer"
read -r w_start w_end <<<"$(cj explore "$work/logs.html" "$g_port" "$work")"
cj entries "$work/logs.html" | sort >"$work/page-lines.txt"
[[ "$(cat "$work/page-lines.txt")" == "$(printf '%s\n' "$b_row" "$w|warn|worker A $marker" | sort)" ]] ||
  fail "the logs page shows the wrong lines" "got: $(tr '\n' '/' <"$work/page-lines.txt")"
code="$(curl -sS -m 15 -o /dev/null -w '%{http_code}' -u "admin:$grafana_password" "$(cat "$work/explore.url")")"
[[ "$code" == 200 ]] || fail "the Explore link answers $code through the second forward"
g_get "/api/datasources/proxy/uid/loki/loki/api/v1/query_range?$(cat "$work/explore.query")" >"$work/loki.json" ||
  fail "Grafana does not answer the Explore query through the second forward"
cj loki "$work/loki.json" | sort >"$work/grafana-lines.txt"
cut -d'|' -f3- "$work/page-lines.txt" | sort >"$work/page-text.txt"
cmp -s "$work/grafana-lines.txt" "$work/page-text.txt" ||
  fail "Grafana and the console disagree for the window $w_start..$w_end" "grafana: $(tr '\n' '/' <"$work/grafana-lines.txt")" "console: $(tr '\n' '/' <"$work/page-text.txt")"
argv=()
while IFS= read -r -d '' arg; do argv+=("$arg"); done <"$work/explore.argv"
(cd "$work/$app" && "$work/spinup" "${argv[@]}") >"$work/cli-logs.txt" 2>"$logs/cli-logs.err" ||
  { sed 's/^/    | /' "$logs/cli-logs.err" >&2; fail "the log command of the page failed: spinup ${argv[*]}"; }
for line in "worker A $marker" "worker B $marker"; do
  grep -qF "$line" "$work/cli-logs.txt" || fail "the CLI log command of the page does not show '$line'"
done
for line in "worker C $marker" "api D $marker"; do
  if grep -qF "$line" "$work/cli-logs.txt"; then fail "the CLI log command of the page shows '$line' outside its filter"; fi
done
echo "    the page, the Explore window ($w_start..$w_end), Grafana, and spinup ${argv[0]} ${argv[1]} agree"
# Prometheus and the console name the same firing alerts.
g_get /api/datasources/proxy/uid/prometheus/api/v1/alerts >"$work/prom-alerts.json" || fail "Prometheus does not answer through Grafana"
c_get /api/v1/overview >"$work/alerts-page.json"
[[ "$(cj prom-alerts "$work/prom-alerts.json" | sort)" == "$(cj alerts "$work/alerts-page.json" | awk '{print $1}' | sort)" ]] ||
  fail "the console and Prometheus name different firing alerts"
echo "    the console and Prometheus name the same firing alerts, including Heartbeat"
echo "    ConsoleLogParity: ok"

# ---------------------------------------------------------------------------------------------
step "console 4. ConsoleWithStoppedApp"
started_before="$(console_started)"
live="$(live_projects)"
stopped="$(vm_docker ps --filter "label=com.docker.compose.project=$live" --format '{{.Names}}' | sort)"
if ! { grep -q -- '-api-' <<<"$stopped" && grep -q -- '-worker-' <<<"$stopped"; }; then
  fail "the live color $live has no api and worker" "got: ${stopped//$'\n'/ }"
fi
# shellcheck disable=SC2086
vm_docker stop $stopped >/dev/null
if http_get /healthz >/dev/null 2>&1; then fail "the app still answers after its color stopped"; fi
new_sample
common_sample c-stopped
cj containers "$work/c-stopped-page.json" >"$work/c-stopped.containers"
for name in $stopped; do
  grep -q "^$live [^ ]* $name exited " "$work/c-stopped.containers" || { cat "$work/c-stopped.containers" >&2; fail "the page does not report $name exited"; }
done
read -r id _ <<<"$(cj release "$work/c-stopped-page.json")"
[[ "$id" == "$rel1" ]] || fail "the release section changed while the app stopped"
[[ "$(console_started)" == "$started_before" ]] || fail "the console restarted while the app stopped" "before: $started_before" "after: $(console_started)"
[[ "$(curl -fsS -m 5 "http://127.0.0.1:$c_port/healthz")" == ok ]] || fail "the console forward stopped with the app"
# shellcheck disable=SC2086
vm_docker start $stopped >/dev/null
for _ in $(seq 1 60); do http_get /healthz >/dev/null 2>&1 && break; sleep 1; done
http_get /healthz >/dev/null || fail "the app did not come back after its color started"
echo "    the page and status report $(wc -w <<<"$stopped" | tr -d ' ') exited containers, the console kept running"
echo "    ConsoleWithStoppedApp: ok"

# ---------------------------------------------------------------------------------------------
step "console 5. ConsoleFailedDeploy"
main_go="$work/$app/api/cmd/api/main.go"
awk '{ print }
/app\.WithReadiness\("db", pool\.Ping\),/ {
  print "\t\tapp.WithReadiness(\"dind-broken\", func(context.Context) error { return fmt.Errorf(\"broken by the dind overlay\") }),"
}' "$main_go" >"$main_go.tmp" && mv "$main_go.tmp" "$main_go"
grep -q dind-broken "$main_go" || fail "could not add the failing readiness check"
commit "dind: break readiness"
broken="$(new_release)"
started_before="$(console_started)"
# The pre-deploy backup always uses pgBackRest (docs/backups-pgdump.md). A pgdump app takes a dump
# itself and deploys with --skip-backup. The release adds no migration.
sp c-backup-1 backup now --type full --env "$c_env" || { show_log c-backup-1; fail "spinup backup now (pgdump) failed"; }
if sp c-deploy-broken deploy --env "$c_env" --skip-build --tag "$broken" --yes --skip-backup --health-timeout 20s; then
  fail "the deploy of a release with a failing /readyz succeeded"
fi
grep -q "did not become healthy" "$logs/c-deploy-broken.log" || { show_log c-deploy-broken; fail "the broken deploy failed for another reason"; }
new_sample
common_sample c-failed
read -r id _ <<<"$(cj release "$work/c-failed-page.json")"
[[ "$id" == "$rel1" ]] || fail "the failed deploy changed the live release" "got: $id"
cj history "$work/c-failed-page.json" >"$work/c-failed.history"
grep -q "^$broken failed " "$work/c-failed.history" || { cat "$work/c-failed.history" >&2; fail "the history has no failed attempt $broken"; }
# deploy.state holds only the latest attempt. A retained release without its events is "recorded".
grep -q "^$rel1 recorded ${USER:-unknown} ${rel1##*-}" "$work/c-failed.history" || { cat "$work/c-failed.history" >&2; fail "the history lost the retained release $rel1"; }
[[ "$(console_started)" == "$started_before" ]] || fail "the failed deploy restarted the console"
echo "    the failed attempt $broken is in the history, $rel1 stays live, and the console did not restart"

git -C "$work/$app" checkout HEAD~1 -- api/cmd/api/main.go
commit "dind: repair readiness"
rel2="$(new_release)"
sp c-deploy-2 deploy --env "$c_env" --skip-build --tag "$rel2" --yes --skip-backup || { show_log c-deploy-2 80; fail "the second deploy failed"; }
c_status c-second
read -r id color previous _ <<<"$(cj release "$work/c-second.json")"
[[ "$id" == "$rel2" && "$previous" == "$rel1" ]] || fail "the second release is wrong" "got: $id previous $previous" "want: $rel2 previous $rel1"
cj history "$work/c-second.json" >"$work/c-second.history"
[[ "$(awk '{print $1, $2}' "$work/c-second.history" | sort)" == "$(printf '%s\n' "$rel2 succeeded" "$rel1 recorded" | sort)" ]] ||
  { cat "$work/c-second.history" >&2; fail "the history after the second deploy is wrong"; }
read -r backend label kind _ _ <<<"$(cj backup "$work/c-second.json")"
[[ "$backend" == pgdump && "$kind" == full ]] || fail "the pgdump backup is not reported" "got: $backend $label $kind"
grep -qF "$label" "$logs/c-backup-1.log" || { show_log c-backup-1; fail "the backup tile reports $label, not the dump of spinup backup now"; }
echo "    $rel2 is live with previous $rel1, and the dump $label is the last backup"

# The page shows both action commands as plain text. The suite runs them on the workstation.
c_get "/?grafana_port=$g_port" >"$work/page2.html" || fail "the console page does not answer after the console was replaced"
cj commands "$work/page2.html" >"$work/commands.txt"
[[ "$(cat "$work/commands.txt")" == "spinup backup now --type full --env $c_env
spinup rollback --env $c_env" ]] || fail "the page shows the wrong action commands" "got: $(tr '\n' '/' <"$work/commands.txt")"
read -r -a backup_cmd <<<"$(sed -n 1p "$work/commands.txt")"
read -r -a rollback_cmd <<<"$(sed -n 2p "$work/commands.txt")"
sleep 1.2 # pgdump labels have a one-second resolution.
sp c-backup-now "${backup_cmd[@]:1}" || { show_log c-backup-now; fail "the backup command of the page failed"; }
new_sample
c_status c-backup
read -r _ label2 _ _ size <<<"$(cj backup "$work/c-backup.json")"
[[ "$label2" != "$label" ]] || fail "the backup section did not move to the new dump"
vm_size="$(docker exec "$vm" stat -c '%s' "/opt/$app/backups/pgdump/$label2.dump")" || fail "the reported dump $label2 is not on the VM"
[[ "$size" == "$vm_size" ]] || fail "the dump size is $size, want $vm_size"
echo "    '${backup_cmd[*]}' made $label2 ($size bytes), and the backup tile reports it"

# A held deploy lock still blocks the rollback command of the page.
# shellcheck disable=SC2087
vm_ssh sh -s >"$logs/c-lock-holder.log" 2>&1 <<EOF &
set -eu
d=/opt/$app/deploy.lock
mkdir "\$d"
printf '{"owner":"dind-holder","pid":%d,"host":"dind","acquired":"%s","ttl_seconds":1800,"release":"dind-lock-test"}' \$\$ "\$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"\$d/owner.json"
chmod 0600 "\$d/owner.json"
echo held
exec sleep 600
EOF
holder=$!
pids+=("$holder")
for _ in $(seq 1 30); do grep -q held "$logs/c-lock-holder.log" 2>/dev/null && break; sleep 0.5; done
grep -q held "$logs/c-lock-holder.log" || fail "the lock holder did not take the lock"
status=0
sp c-rollback-locked "${rollback_cmd[@]:1}" --yes || status=$?
[[ $status -ne 0 ]] || fail "the rollback command ran while another process held the deploy lock"
grep -q dind-holder "$logs/c-rollback-locked.log" || { show_log c-rollback-locked; fail "the locked rollback does not name the lock owner"; }
expect_version "$rel2"
kill "$holder" 2>/dev/null || true
wait "$holder" 2>/dev/null || true
sp c-unlock deploy unlock --force --env "$c_env" || { show_log c-unlock; fail "deploy unlock --force failed"; }
echo "    '${rollback_cmd[*]} --yes' exits $status while the lock is held, and names the owner"

sp c-rollback "${rollback_cmd[@]:1}" --yes || { show_log c-rollback 60; fail "the rollback command of the page failed"; }
expect_version "$rel1"
new_sample
common_sample c-rolled
read -r id color previous _ <<<"$(cj release "$work/c-rolled-page.json")"
[[ "$id" == "$rel1" ]] || fail "the page does not report the restored release" "got: $id"
[[ "$(live_projects)" == "$app-$color" ]] || fail "the restored color $color is not live"
[[ "$(cj history "$work/c-rolled-page.json" | awk -v id="$rel1" '$1 == id' | grep -c .)" == 1 ]] || fail "the restored release appears more than once in the history"
[[ "$(curl -fsS -m 5 "http://127.0.0.1:$c_port/healthz")" == ok ]] || fail "the console stopped during the rollback"
echo "    the rollback restored $rel1, and the console kept reading the VM"
echo "    ConsoleFailedDeploy: ok"

# ---------------------------------------------------------------------------------------------
step "console 6. memory and teardown"
vm_docker stats --no-stream --format '{{.Name}} {{.MemUsage}}' "$c_web" "$c_relay" | sed 's/^/    idle memory: /'
close_console "$b_pid"
if curl -fsS -m 3 "http://127.0.0.1:$c_port/healthz" >/dev/null 2>&1; then fail "the interrupted command left its console forward open"; fi

printf '\nrun.sh: PASS console (releases %s, %s; failed %s)\n' "$rel1" "$rel2" "$broken"
