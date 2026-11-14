"""The app suite: the build and the runtime of a generated API.

The scenario app: `spinup new` with the modules ci, auth, jobs, and mail (the web directory is
generated and never built, because the auth module needs it), then the resources of
scripts/perf/resources.txt with `g resource --api-only`.

Phases:
  1. Dev builds: the cold build (a new GOCACHE) and the incremental build after a one-line edit.
  2. The release build as the Dockerfile builds it, and the binary size.
  3. Runtime on the release binary: start to /healthz, idle RSS, load on /healthz and on the
     session list endpoint (scripts/perf/loadgen), RSS after the load, and the login latency.
  4. Live data (only with PERF_LIVE=1, ADR 0033 section 17.6): a second app with the live module
     and `g resource Note body:text --live`. scripts/perf/livegen opens many SSE streams and
     measures the fan-out latency, the memory of a stream, the resume storm, and the drain.
"""

import http.client
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import time
import traceback
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import lib  # noqa: E402

SUITE = "app"
MARK = re.compile(r"mark-\d{4}")
LOGIN_EMAIL = "perf@example.com"
LOGIN_PASSWORD = "correct horse battery staple"
SEED_ROWS = 100
HEALTH_POLL_S = 0.002
IDLE_WAIT_S = 2.0
CONCURRENCY = 32
LIST_PATH = "/api/notes?limit=50"

PERF_MARK_GO = """\
package routes

// perfMark is the target of the incremental build edits of the perf loop (scripts/perf).
var perfMark = "mark-0000"
"""

LATENCY_INFO = "the same load run as the req/s metric; latency percentiles are noisy on a shared machine"

BUILD_METRICS = [("app_cold_build", "s", "lower", None), ("app_incremental_build", "s", "lower", None),
                 ("app_release_build_time", "s", "lower", None), ("app_release_binary_size", "MB", "lower", None)]
RUNTIME_METRICS = [
    ("app_start_to_healthz", "ms", "lower", None), ("app_idle_rss", "MiB", "lower", None),
    ("app_rps_healthz", "req/s", "higher", None),
    *[(f"app_latency_{p}_healthz", "ms", "lower", LATENCY_INFO) for p in ("p50", "p90", "p99", "p999")],
    ("app_rps_list", "req/s", "higher", None),
    *[(f"app_latency_{p}_list", "ms", "lower", LATENCY_INFO) for p in ("p50", "p90", "p99", "p999")],
    ("app_rss_after_load", "MiB", "lower", None), ("app_login_latency", "ms", "lower", None),
]


# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def rmtree(path):
    if path:
        shutil.rmtree(path, ignore_errors=True)


def timed(argv, cwd, env, timeout=3600):
    """Runs argv and returns the wall time in seconds. Failure carries the output tail."""
    t = time.perf_counter()
    lib.run(argv, cwd=cwd, env=env, timeout=timeout, capture=True)
    return time.perf_counter() - t


def healthz_ok(port, timeout=0.5):
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=timeout) as s:
            s.sendall(b"GET /healthz HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
            return s.recv(32).startswith(b"HTTP/1.1 200")
    except OSError:
        return False


def rss_mib(pid):
    out = subprocess.run(["ps", "-o", "rss=", "-p", str(pid)], capture_output=True, text=True).stdout.strip()
    if not out:
        raise lib.Failure(f"cannot read the RSS of process {pid} (the process is gone)", "read the app log")
    return int(out) / 1024


class Server:
    """A running release binary. stop() ends it."""

    def __init__(self, ctx, binary, db_url, cwd, extra_env=None):
        self.ctx, self.binary, self.db_url, self.cwd = ctx, binary, db_url, cwd
        self.extra_env = dict(extra_env or {})
        self.respawns = 0
        self._fh = None
        self._spawn()

    def _spawn(self):
        self.port = free_port()
        self.log = self.ctx.tmp("srv") / "app.log"
        env = self.ctx.cmd_env(DATABASE_URL=self.db_url, PORT=self.port, SPINUP_ENV="production", LOG_LEVEL="info",
                               **self.extra_env)
        for k in ("OTEL_EXPORTER_OTLP_ENDPOINT", "SPINUP_KIT_REPLACE", "DATABASE_URL_FILE", "GOFLAGS"):
            env.pop(k, None)
        if self._fh:
            self._fh.close()
        self._fh = open(self.log, "w")
        self.t0 = time.perf_counter()
        self.proc = subprocess.Popen([str(self.binary)], cwd=self.cwd, env=env, stdin=subprocess.DEVNULL,
                                     stdout=self._fh, stderr=subprocess.STDOUT, start_new_session=True)
        lib.track(self.proc.pid)

    def tail(self, n=15):
        try:
            return "\n".join(self.log.read_text(errors="replace").splitlines()[-n:])
        except OSError:
            return ""

    def wait_ready(self, timeout=30.0):
        """Polls /healthz every 2 ms. Returns ms since the spawn.

        The machine is shared, so another process can take the free port before the API
        listens on it. Then the API stops with "address already in use", and wait_ready starts
        it again on a new port (at most 3 times). The time counts from the last spawn.
        """
        deadline = time.perf_counter() + timeout
        while True:
            if healthz_ok(self.port):
                return (time.perf_counter() - self.t0) * 1000
            if self.proc.poll() is not None:
                if "address already in use" in self.tail(5) and self.respawns < 3:
                    lib.untrack(self.proc.pid)
                    self.respawns += 1
                    lib.log(f"the API port {self.port} was taken; starting the API again on a new port")
                    self._spawn()
                    continue
                raise lib.Failure(f"the API stopped with exit code {self.proc.returncode} before /healthz "
                                  f"answered\n{self.tail()}", "read the app log")
            if time.perf_counter() > deadline:
                raise lib.Failure(f"the API did not answer /healthz in {timeout:.0f} s\n{self.tail()}",
                                  "read the app log")
            time.sleep(HEALTH_POLL_S)

    def stop(self):
        p = self.proc
        if p.poll() is None:
            try:
                os.killpg(p.pid, signal.SIGTERM)
            except (ProcessLookupError, PermissionError):
                pass
            try:
                p.wait(timeout=10)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(p.pid, signal.SIGKILL)
                except (ProcessLookupError, PermissionError):
                    pass
                p.wait()
        lib.untrack(p.pid)
        self._fh.close()


def loadgen(ctx, url, cookie=None):
    """Runs scripts/perf/loadgen against url. Returns its JSON. Failure on any failed request."""
    dur = ctx.n("5s", "2s")
    argv = [ctx.loadgen, "-url", url, "-c", str(CONCURRENCY), "-d", dur, "-warmup", "1s"]
    if cookie:
        argv += ["-cookie", cookie]
    rc, so, se = lib.run(argv, timeout=300, capture=True, check=False)
    try:
        res = json.loads(so)
    except ValueError:
        raise lib.Failure(f"loadgen printed no JSON (exit code {rc}):\n" + "\n".join((so + se).splitlines()[-8:]),
                          "run scripts/perf/loadgen by hand against the URL")
    if rc != 0 or res.get("errors"):
        raise lib.Failure(f"loadgen saw {res.get('errors')} failed requests of {res.get('requests')} at {url} "
                          f"(first: {res.get('first_error')})", "check the app log and the route")
    return res


def api_json(port, method, path, body=None, conn=None, cookie=None):
    """One JSON request. Returns (status, body bytes, header list)."""
    own = conn is None
    c = conn or http.client.HTTPConnection("127.0.0.1", port, timeout=30)
    try:
        headers = {"Content-Type": "application/json"} if body is not None else {}
        if cookie:
            headers["Cookie"] = cookie
        c.request(method, path, body=json.dumps(body) if body is not None else None, headers=headers)
        r = c.getresponse()
        return r.status, r.read(), r.getheaders()
    finally:
        if own:
            c.close()


def session_cookie(headers):
    """The `name=value` pair of the first Set-Cookie header, or None."""
    for k, v in headers:
        if k.lower() == "set-cookie":
            return v.split(";", 1)[0]
    return None


def sign_in(port, register=False):
    """Registers (when asked) or logs in the perf user. Returns (user id, session cookie)."""
    path = "/api/auth/register" if register else "/api/auth/login"
    st, body, hdr = api_json(port, "POST", path, {"email": LOGIN_EMAIL, "password": LOGIN_PASSWORD})
    if st not in (200, 201):
        raise lib.Failure(f"POST {path} answered {st}: {body[:160]!r}", "read the app log")
    cookie = session_cookie(hdr)
    if not cookie:
        raise lib.Failure(f"POST {path} set no session cookie", "read the app log")
    return json.loads(body)["user"]["id"], cookie


# ---------------------------------------------------------------------------
# The scenario app
# ---------------------------------------------------------------------------


class AppState:
    """The generated app, its build dirs, and the edit counter."""

    def __init__(self, app):
        self.app = Path(app)
        self.api = self.app / "api"
        self.mark = self.api / "internal" / "routes" / "perf_mark.go"
        self.cache = None
        self.out = None
        self.edits = 0
        self.warm = False
        self.errors = []
        self.release_bin = None

    def edit(self):
        """Changes the marker string. Every call changes the file."""
        self.edits += 1
        self.mark.write_text(MARK.sub(f"mark-{self.edits:04d}", self.mark.read_text()))

    def env(self, ctx, **extra):
        return ctx.cmd_env(GOCACHE=self.cache, **extra)


def generate_app(ctx, parent):
    app = lib.new_app(ctx, parent, modules=lib.APP_MODULES, api_only=False)
    lib.gen_resources(ctx, app)
    st = AppState(app)
    if not st.mark.parent.is_dir():
        raise lib.Failure(f"the generated app has no {st.mark.parent}", "check the api pack layout")
    st.mark.write_text(PERF_MARK_GO)
    return st


# ---------------------------------------------------------------------------
# Phase 1 and 2: builds
# ---------------------------------------------------------------------------


def dev_phase(ctx, st, recs):
    def cold_fn(i, warm):
        rmtree(st.cache)
        st.warm = False
        st.cache = str(ctx.tmp("gocache"))
        st.out = ctx.tmp("out") / "api"
        s = timed(["go", "build", "-o", str(st.out), "./cmd/api"], st.api, st.env(ctx))
        st.warm = True
        return s

    recs.append(lib.measure(
        ctx, SUITE, "app_cold_build", cold_fn, n=ctx.n(2, 1), unit="s", warmup=0,
        note="go build -o <bin> ./cmd/api with a new empty GOCACHE (the standard library compiles too); the "
             "modules are already downloaded. No warm-up, because a warm-up is another cold build"))

    if not st.warm:
        recs.append(lib.skipped(SUITE, "app_incremental_build", "s", "no finished cold build"))
        return

    def inc_fn(i, warm):
        st.edit()
        return timed(["go", "build", "-o", str(st.out), "./cmd/api"], st.api, st.env(ctx))

    recs.append(lib.measure(
        ctx, SUITE, "app_incremental_build", inc_fn, n=ctx.n(5, 2), unit="s",
        note="go build after a one-string edit in api/internal/routes/perf_mark.go: the routes package "
             "compiles again, and the binary links again"))
    rmtree(st.cache)
    st.cache = None


def release_phase(ctx, st, recs):
    def rel_fn(i, warm):
        outdir = ctx.tmp("rel")
        cache = ctx.tmp("rel-gocache")
        out = outdir / "api"
        try:
            s = timed(["go", "build", "-trimpath", "-ldflags", "-s -w", "-o", str(out), "./cmd/api"], st.api,
                      ctx.cmd_env(GOCACHE=cache, CGO_ENABLED="0"), timeout=5400)
        finally:
            rmtree(cache)
        if not out.is_file():
            raise lib.Failure(f"the release build wrote no {out}", "run the build by hand in the api dir")
        st.release_bin = out
        return s

    recs.append(lib.measure(
        ctx, SUITE, "app_release_build_time", rel_fn, n=1, unit="s", warmup=0,
        note="cold release build as the Dockerfile builds it: CGO_ENABLED=0 go build -trimpath -ldflags "
             "\"-s -w\" with a new GOCACHE. One sample: the binary is also the one of the runtime metrics"))
    if st.release_bin and st.release_bin.is_file():
        recs.append(lib.record(SUITE, "app_release_binary_size", "MB", [st.release_bin.stat().st_size / 1e6],
                               note="the binary of app_release_build_time (stripped, the host target)"))
    else:
        recs.append(lib.skipped(SUITE, "app_release_binary_size", "MB", "no release binary"))


# ---------------------------------------------------------------------------
# Phase 3: runtime
# ---------------------------------------------------------------------------


def seed_notes(url, user_id):
    """Inserts SEED_ROWS rows into the notes table for the perf user."""
    lib.psql(url, f"INSERT INTO notes (user_id, title, body) SELECT {int(user_id)}, 'note ' || g, "
                  f"repeat('x', 200) FROM generate_series(1, {SEED_ROWS}) g")


def runtime_setup(ctx, st):
    """A migrated database with the perf user and the seeded rows. Returns (db url, cookie).

    The cookie comes from the registration here. Sessions live in the database, so every
    server on that database accepts it. The measured servers never run a login, so no
    argon2id buffer is in their RSS.
    """
    _, url = lib.create_db(ctx, "app")
    env = ctx.cmd_env(DATABASE_URL=url, SPINUP_ENV="production", LOG_LEVEL="warn")
    env.pop("SPINUP_KIT_REPLACE", None)
    cwd = ctx.tmp("mig")
    lib.run([st.release_bin, "migrate", "up"], cwd=cwd, env=env, timeout=300, capture=True)
    srv = Server(ctx, st.release_bin, url, cwd)
    try:
        srv.wait_ready()
        uid, cookie = sign_in(srv.port, register=True)
    finally:
        srv.stop()
    seed_notes(url, uid)
    return url, cookie


def runtime_phase(ctx, st, recs):
    if not (st.release_bin and st.release_bin.is_file()):
        for metric, unit, better, info in RUNTIME_METRICS:
            recs.append(lib.skipped(SUITE, metric, unit, "no release binary", better=better, informational=info))
        return
    url, cookie = runtime_setup(ctx, st)
    cwd = ctx.tmp("cwd")

    def start_fn(i, warm):
        srv = Server(ctx, st.release_bin, url, cwd)
        try:
            return srv.wait_ready()
        finally:
            srv.stop()

    recs.append(lib.measure(
        ctx, SUITE, "app_start_to_healthz", start_fn, n=ctx.n(20, 5), unit="ms",
        note="release binary, SPINUP_ENV=production, a migrated database; spawn to the first 200 from "
             "/healthz, polled every 2 ms with a new connection"))

    side = {k: [] for k in ("idle", "after", "list_rps",
                            *[f"h_{p}" for p in ("p50", "p90", "p99", "p999")],
                            *[f"l_{p}" for p in ("p50", "p90", "p99", "p999")])}

    def load_fn(i, warm):
        srv = Server(ctx, st.release_bin, url, cwd)
        try:
            srv.wait_ready()
            time.sleep(IDLE_WAIT_S)
            idle = rss_mib(srv.proc.pid)
            base = f"http://127.0.0.1:{srv.port}"
            code, body, _ = api_json(srv.port, "GET", LIST_PATH, cookie=cookie)
            if code != 200 or b'"items"' not in body:
                raise lib.Failure(f"GET {LIST_PATH} answered {code}: {body[:120]!r}", "read the app log")
            h = loadgen(ctx, f"{base}/healthz")
            lst = loadgen(ctx, f"{base}{LIST_PATH}", cookie=cookie)
            after = rss_mib(srv.proc.pid)
        finally:
            srv.stop()
        if not warm:
            side["idle"].append(idle)
            side["after"].append(after)
            side["list_rps"].append(lst["rps"])
            for p in ("p50", "p90", "p99", "p999"):
                side[f"h_{p}"].append(h["latency_ms"][p])
                side[f"l_{p}"].append(lst["latency_ms"][p])
        return h["rps"]

    lg_note = (f"scripts/perf/loadgen -c {CONCURRENCY} -d {ctx.n('5s', '2s')} -warmup 1s (keep-alive, the client "
               f"runs on all cores of the same machine) against the release binary; one new server for each "
               f"sample")
    primary = lib.measure(ctx, SUITE, "app_rps_healthz", load_fn, n=ctx.n(3, 1), unit="req/s", better="higher",
                          note="GET /healthz. " + lg_note)
    recs.append(primary)
    fails = [p for p in primary["note"].split("; ") if p.startswith("failed at") or p.startswith("load_gate")]

    def side_rec(metric, key, unit, better, note, info=None):
        recs.append(lib.record(SUITE, metric, unit, side[key], better=better, warmup=1,
                               note="; ".join([note, *fails]), informational=info))

    for p in ("p50", "p90", "p99", "p999"):
        side_rec(f"app_latency_{p}_healthz", f"h_{p}", "ms", "lower", f"GET /healthz, {CONCURRENCY} concurrent. "
                 + lg_note, LATENCY_INFO)
    list_note = (f"GET {LIST_PATH} with the session cookie of the perf user: one session lookup and a list of "
                 f"50 rows as JSON ({SEED_ROWS} seeded rows of that user). " + lg_note)
    side_rec("app_rps_list", "list_rps", "req/s", "higher", list_note)
    for p in ("p50", "p90", "p99", "p999"):
        side_rec(f"app_latency_{p}_list", f"l_{p}", "ms", "lower", list_note, LATENCY_INFO)
    side_rec("app_idle_rss", "idle", "MiB", "lower",
             f"ps RSS {IDLE_WAIT_S:.0f} s after the first 200 from /healthz, before any load")
    side_rec("app_rss_after_load", "after", "MiB", "lower",
             "ps RSS right after the two load runs (the Go runtime may keep freed pages). The server never "
             "ran a login, so the argon2id buffer is not in this number")

    login_phase(ctx, st, url, cwd, recs)


def login_phase(ctx, st, url, cwd, recs):
    try:
        srv = Server(ctx, st.release_bin, url, cwd)
        srv.wait_ready()
    except Exception as e:
        recs.append(lib.skipped(SUITE, "app_login_latency", "ms",
                                f"could not start the API: {str(e).splitlines()[0][:200]}"))
        return
    conn = http.client.HTTPConnection("127.0.0.1", srv.port, timeout=30)

    def login_fn(i, warm):
        t = time.perf_counter()
        code, body, _ = api_json(srv.port, "POST", "/api/auth/login",
                                 {"email": LOGIN_EMAIL, "password": LOGIN_PASSWORD}, conn=conn)
        ms = (time.perf_counter() - t) * 1000
        if code != 200:
            raise lib.Failure(f"login answered {code}: {body[:160]!r}", "read the app log")
        return ms

    try:
        recs.append(lib.measure(
            ctx, SUITE, "app_login_latency", login_fn, n=ctx.n(20, 5), unit="ms",
            note="POST /api/auth/login for the perf user, sequential, one keep-alive connection, the release "
                 "binary (one argon2id check each; the loopback client is exempt from the rate limit)"))
    finally:
        conn.close()
        srv.stop()


# ---------------------------------------------------------------------------
# Phase 4: live data (PERF_LIVE=1)
# ---------------------------------------------------------------------------

LIVE_RESOURCE = "Note body:text --live"
LIVE_FEED = "notes"
LIVE_TIMEOUT = "60s"
LIVE_INFO = "a single run of scripts/perf/livegen with the client on the same machine; noisy on a shared machine"
# (metric, unit, better, class, informational reason)
LIVE_METRICS = [
    ("live_fanout_p50_ms", "ms", "lower", "informational", LIVE_INFO),
    ("live_fanout_p99_ms", "ms", "lower", "primary", None),
    ("live_stream_kib", "KiB", "lower", "primary", None),
    ("live_resume_ready_s", "s", "lower", "informational", LIVE_INFO),
    ("live_resume_sync_count", "count", "lower", "informational", LIVE_INFO),
    ("live_resume_503_count", "count", "lower", "informational", LIVE_INFO),
    ("live_drain_s", "s", "lower", "informational", LIVE_INFO),
    ("live_drain_exit_s", "s", "lower", "informational", LIVE_INFO),
]


def phases(environ=None):
    """The phases of the app suite. The live phase runs only when PERF_LIVE is "1"."""
    environ = os.environ if environ is None else environ
    base = (dev_phase, release_phase, runtime_phase)
    return base + (live_phase,) if environ.get("PERF_LIVE") == "1" else base


def _live_record(metric, value, note):
    unit, better, cls, info = next((u, b, c, i) for m, u, b, c, i in LIVE_METRICS if m == metric)
    if value is None:
        rec = lib.skipped(SUITE, metric, unit, note, better=better, informational=info, metric_class=cls)
    else:
        rec = lib.record(SUITE, metric, unit, [value], better=better, note=note, informational=info,
                         metric_class=cls)
    rec["category"] = "live"
    return rec


def live_records(res, clients, patches):
    """Turns the livegen JSON into records. Returns (records, failures).

    A scenario that failed gives records with no samples (never a zero), and its failure
    goes in the failures list. A failed conformance check fails every scenario.
    """
    size = f"N={clients} streams of one feed, P={patches} patches"
    failures = []
    conf = res.get("conformance") or {}
    if not conf.get("ok"):
        why = conf.get("error") or "no conformance result"
        failures.append(f"conformance: {why}")
        return [_live_record(m, None, f"the conformance check failed: {why}") for m, *_ in LIVE_METRICS], failures

    def scenario(name, check=None):
        obj = res.get(name)
        if obj is None:
            failures.append(f"{name}: livegen wrote no result")
            return None, f"livegen wrote no {name} result"
        why = obj.get("error") or (check(obj) if check else None)
        if why:
            failures.append(f"{name}: {why}")
            return None, f"the {name} scenario failed: {why}"
        return obj, None

    def fan_check(o):
        bad = {k: o.get(k, 0) for k in ("missed", "duplicates", "out_of_order", "mismatched")}
        if any(bad.values()) or not o.get("order_ok"):
            return ", ".join(f"{v} {k}" for k, v in bad.items()) + " patches"
        return None

    recs = []
    fan, why = scenario("fanout", fan_check)
    lat = (fan or {}).get("latency_ms", {})
    fan_note = (f"{size}; one POST /api/{LIVE_FEED} for each patch; the time from the end of the POST to the "
                f"patch frame on each client ({(fan or {}).get('samples', 0)} samples, "
                f"{(fan or {}).get('early', 0)} frames came before the POST ended and count as 0)")
    recs.append(_live_record("live_fanout_p50_ms", lat.get("p50") if fan else None, why or fan_note))
    recs.append(_live_record("live_fanout_p99_ms", lat.get("p99") if fan else None, why or fan_note))

    mem, mwhy = scenario("memory")
    if mem and mem.get("per_stream_kib") is None:
        mem, mwhy = None, "no per_stream_kib"
    mem_note = (f"{size}; (server RSS after ready on all - RSS before the streams) / N, read with ps "
                f"({(mem or {}).get('before_kib')} KiB -> {(mem or {}).get('after_kib')} KiB)")
    recs.append(_live_record("live_stream_kib", mem.get("per_stream_kib") if mem else None, mwhy or mem_note))

    rs, rwhy = scenario("resume")
    rs_note = (f"{size}; all streams closed at one time, then opened again at one time with since; "
               f"{(rs or {}).get('resumed')} resumed, {(rs or {}).get('status_429')} answers 429, "
               f"{(rs or {}).get('retries')} retries (jitter 0 to 3 s), most attempts {(rs or {}).get('max_attempts')}")
    recs.append(_live_record("live_resume_ready_s", rs.get("ready_s") if rs else None, rwhy or rs_note))
    recs.append(_live_record("live_resume_sync_count", rs.get("sync") if rs else None, rwhy or rs_note))
    recs.append(_live_record("live_resume_503_count", rs.get("status_503") if rs else None, rwhy or rs_note))

    dr, dwhy = scenario("drain")
    dr_note = (f"N={clients} open streams; SIGTERM to the API; {(dr or {}).get('bye')} streams got bye "
               f"(reason drain)")
    recs.append(_live_record("live_drain_s", dr.get("ended_s") if dr else None,
                             dwhy or dr_note + "; the time until the last stream ended"))
    recs.append(_live_record("live_drain_exit_s", dr.get("exit_s") if dr else None,
                             dwhy or dr_note + "; the time until the process exited (polled with ps)"))
    return recs, failures


def build_livegen(ctx):
    """Builds scripts/perf/livegen next to the load generator. Returns its path."""
    out = ctx.scratch / "bin" / "livegen"
    out.parent.mkdir(exist_ok=True)
    lib.run(["go", "build", "-o", str(out), "./scripts/perf/livegen"], cwd=ctx.repo, env=ctx.env, timeout=600,
            capture=True)
    return out


def live_phase(ctx, st, recs):
    """The live data phase: a live app, its release binary, and one livegen run."""
    clients, patches = ctx.n(1000, 100), ctx.n(50, 10)
    parent = ctx.tmp("live")
    db_name = None
    srv = None
    try:
        tool = build_livegen(ctx)
        app = lib.new_app(ctx, parent, modules=lib.APP_MODULES + ",live", api_only=False)
        lib.gen_resources(ctx, app, [LIVE_RESOURCE])
        binary = ctx.tmp("live-bin") / "api"
        lib.run(["go", "build", "-trimpath", "-o", str(binary), "./cmd/api"], cwd=Path(app) / "api",
                env=ctx.cmd_env(CGO_ENABLED="0"), timeout=3600, capture=True)
        db_name, url = lib.create_db(ctx, "live")
        env = ctx.cmd_env(DATABASE_URL=url, SPINUP_ENV="production", LOG_LEVEL="warn")
        env.pop("SPINUP_KIT_REPLACE", None)
        cwd = ctx.tmp("live-cwd")
        lib.run([binary, "migrate", "up"], cwd=cwd, env=env, timeout=300, capture=True)
        # One test user opens every stream, so the limit for each user must allow them all,
        # also while the resume storm overlaps old and new streams. LIVE_MAX_STREAMS keeps
        # its default (2000), because the phase checks that default.
        live_env = {"LIVE_MAX_STREAMS_PER_USER": 2 * clients + 16}
        # Register on a first server, so the argon2id buffer is not in the measured RSS.
        srv = Server(ctx, binary, url, cwd, extra_env=live_env)
        try:
            srv.wait_ready()
            _, cookie = sign_in(srv.port, register=True)
        finally:
            srv.stop()
        srv = Server(ctx, binary, url, cwd, extra_env=live_env)
        srv.wait_ready()
        pid = srv.proc.pid
        lib.load_gate(ctx, "live")
        argv = [tool, "-base", f"http://127.0.0.1:{srv.port}", "-feed", LIVE_FEED, "-cookie", cookie,
                "-clients", clients, "-patches", patches, "-publish-url", f"http://127.0.0.1:{srv.port}/api/notes",
                "-publish-body", json.dumps({"body": "live perf"}), "-server-pid", pid, "-resume",
                "-drain-pid", pid, "-timeout", LIVE_TIMEOUT]
        rc, so, se = lib.run(argv, timeout=900, capture=True, check=False)
        try:
            res = json.loads(so)
        except ValueError:
            res = {"conformance": {"ok": False, "error": f"livegen printed no JSON (exit code {rc}): "
                                   + " ".join((so + se).splitlines()[-3:])[:300]}}
        new, failures = live_records(res, clients, patches)
        recs.extend(new)
        if rc != 0 and not failures:
            failures.append(f"livegen exited with code {rc}: {se.strip()[:300]}")
        if failures:
            raise lib.Failure("live phase: " + "; ".join(failures) + f"\n{srv.tail()}",
                              "read the app log above and run scripts/perf/livegen by hand against the app")
    except Exception:
        if not any(r.get("category") == "live" for r in recs):
            recs.extend(_live_record(m, None, "the live phase stopped before livegen ran") for m, *_ in LIVE_METRICS)
        raise
    finally:
        if srv:
            srv.stop()
        if db_name:
            lib.drop_db(ctx, db_name)
        rmtree(parent)


# ---------------------------------------------------------------------------
# Entry
# ---------------------------------------------------------------------------


def run(ctx):
    recs = ctx.live
    parent = ctx.tmp("app")
    try:
        st = generate_app(ctx, parent)
    except Exception as e:
        msg = f"could not create the scenario app: {str(e).splitlines()[0][:300]}"
        for m, u, b, info in BUILD_METRICS + RUNTIME_METRICS:
            recs.append(lib.skipped(SUITE, m, u, msg, better=b, informational=info))
        raise
    failed = []
    try:
        for phase in phases():
            try:
                phase(ctx, st, recs)
            except Exception as e:  # keep the finished phases, and run the next one
                traceback.print_exc()
                first = str(e).splitlines()[0][:200] if str(e) else ""
                ctx.notes.append(f"app suite: {phase.__name__} stopped early with {type(e).__name__}: {first}. "
                                 f"Its finished metrics are kept.")
                failed.append(f"{phase.__name__}: {first}")
            lib.write_results(ctx)
    finally:
        rmtree(parent)
    if failed:
        raise lib.Failure("app suite phases failed: " + "; ".join(failed), "read the traceback above")
    return recs
