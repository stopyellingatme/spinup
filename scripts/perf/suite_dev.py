"""The dev suite (ADR 0034 §6.5): the edit-to-response times of `spinup dev`.

The suite is opt-in. Run it with `make perf SUITES=dev` (add QUICK=1 for one edit of each kind).
It is the Go half of scripts/bench/suite_dev.py of the branch `bench`.

The scenario app: `spinup new` with the modules of the cli suite and the web directory (the
OpenAPI edit regenerates the web client), then the resources of scripts/perf/resources.txt.
The suite adds two probe routes, a probe query, and a marker in the first OpenAPI schema file.
The dev command is `spinup dev --no-web --no-infra --no-worker`: no Vite server, no Docker,
and no jobs worker. The client post-step of the dev watcher still runs (it needs only the web
dependencies, which the suite installs offline).

Records (all primary unless the list says informational):
  dev_cold_start                    spawn `spinup dev` -> first 200 from /healthz (ms)
  dev_handler_edit                  edit a handler body -> first 200 with the new marker (ms)
  dev_breakdown_edit_to_ready       the edit -> the "HTTP server listening" line (ms, informational)
  dev_breakdown_ready_to_response   that line -> the first answer with the marker (ms, informational)
  dev_query_edit                    edit a query file (sqlc) -> first 200 with the new marker (ms)
  dev_openapi_edit                  edit an OpenAPI schema -> the regenerated client file has the marker (ms)
  dev_compile_error_feedback        write a type error -> the error line in the dev output (ms)
  dev_compile_error_recovery        fix the error -> first 200 with the new marker (ms)

Every process that the suite starts stops at the end, also on a failure. The app directory and
the database are removed.
"""

import http.client
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time
import traceback
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import lib  # noqa: E402

SUITE = "dev"
HEALTHZ = "/healthz"
GREETING = "/api/perf-greeting"
QUERY = "/api/perf-query"
MARK = re.compile(r"mark-\d{4}")
QUIET_S = 1.5
POLL_S = 0.005
HEALTH_POLL_S = 0.002
START_TIMEOUT_S = 300.0
SAMPLE_TIMEOUT_S = 120.0
ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")
ERROR_LINE = re.compile(r"routes\.go:\d+:\d+:")
DEV_ARGS = ["dev", "--no-web", "--no-infra", "--no-worker"]

ROUTES_GO = """\
// Package routes registers the application routes.
package routes

import (
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/app/api/internal/store"
)

// Deps contains the dependencies for route handlers.
type Deps struct {
	DB *pgxpool.Pool
}

// Register adds generated routes and application routes to mux.
func Register(mux *http.ServeMux, d Deps) {
	registerGenerated(mux, d)

	// The probes of the dev suite of the perf loop (scripts/perf/suite_dev.py).
	mux.HandleFunc("GET /api/perf-greeting", func(w http.ResponseWriter, r *http.Request) {
		var n int64
		if err := d.DB.QueryRow(r.Context(), "SELECT 1").Scan(&n); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "%d, mark-0000", n)
	})
	mux.HandleFunc("GET /api/perf-query", func(w http.ResponseWriter, r *http.Request) {
		mark, err := store.New(d.DB).PerfMark(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, mark)
	})
}
"""

PERF_SQL = """\
-- The probe query of the dev suite of the perf loop (scripts/perf/suite_dev.py).

-- name: PerfMark :one
SELECT 'mark-0000'::text AS mark;
"""

# The compile error: one line goes in before the anchor line of the greeting handler.
ERR_ANCHOR = "\t\tvar n int64\n"
ERR_LINE = "\t\tvar _ int = \"oops-{i}\"\n"


# ---------------------------------------------------------------------------
# Small helpers (private: lib.py has no HTTP poll or process-tree helper)
# ---------------------------------------------------------------------------


def _free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _http_get(port, path, timeout=2.0):
    """GET with a new connection. Returns (status, body), or None when the request fails."""
    c = http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)
    try:
        c.request("GET", path, headers={"Connection": "close"})
        r = c.getresponse()
        return r.status, r.read().decode("utf-8", "replace")
    except (OSError, http.client.HTTPException):
        return None
    finally:
        c.close()


def _port_open(port):
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.settimeout(0.2)
        return s.connect_ex(("127.0.0.1", port)) == 0


def _wait_for(pred, timeout, interval):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if pred():
            return True
        time.sleep(interval)
    return False


def _descendants(pid):
    """The pids of every descendant of pid (from one `ps` snapshot)."""
    try:
        out = subprocess.run(["ps", "-A", "-o", "pid=,ppid="], capture_output=True, text=True).stdout
    except OSError:
        return []
    children = {}
    for line in out.splitlines():
        parts = line.split()
        if len(parts) == 2 and parts[0].isdigit() and parts[1].isdigit():
            children.setdefault(int(parts[1]), []).append(int(parts[0]))
    found, todo = [], [pid]
    while todo:
        for c in children.get(todo.pop(), []):
            found.append(c)
            todo.append(c)
    return found


def _kill(pid, group=False):
    try:
        if group:
            os.killpg(pid, signal.SIGKILL)
        else:
            os.kill(pid, signal.SIGKILL)
    except (ProcessLookupError, PermissionError):
        pass


def _pnpm():
    return shutil.which("pnpm") or str(lib.REPO / "scripts" / "bin" / "pnpm")


# ---------------------------------------------------------------------------
# The scenario app and the dev loop
# ---------------------------------------------------------------------------


class DevApp:
    """The scenario app with its probes, its database, and one running `spinup dev`."""

    def __init__(self, ctx, parent):
        self.ctx = ctx
        self.parent = Path(parent)
        self.app = None
        self.api = None
        self.routes = None
        self.query = None
        self.schema = None
        self.client = None
        self.db_name = None
        self.db_url = None
        self.proc = None
        self.port = None
        self.reader = None
        self.log_path = self.parent / "dev.log"
        self.lines = []  # (monotonic time, line) of the dev output, without the request log lines
        self.lock = threading.Lock()
        self.marks = 0

    def env(self, **extra):
        return self.ctx.cmd_env(DATABASE_URL=self.db_url, NO_COLOR="1", **extra)

    # Setup.

    def setup(self):
        ctx = self.ctx
        self.db_name, self.db_url = lib.create_db(ctx, "dev")
        self.app = lib.new_app(ctx, self.parent, modules=lib.CLI_MODULES, api_only=False)
        lib.gen_resources(ctx, self.app)
        self.api = self.app / "api"
        self._set_env_db()
        self.routes = self.api / "internal" / "routes" / "routes.go"
        self.query = self.api / "db" / "queries" / "perf_mark.sql"
        schemas = sorted((self.api / "openapi" / "schemas").glob("*.yaml"))
        if not schemas:
            raise lib.Failure(f"the generated app has no schema file in {self.api / 'openapi' / 'schemas'}",
                              "check that the scenario has at least one resource")
        self.schema = schemas[0]
        self.client = self.app / "web" / "src" / "lib" / "api" / "schema.d.ts"
        self.routes.write_text(ROUTES_GO)
        self.query.write_text(PERF_SQL)
        self._mark_schema()
        env = self.env()
        lib.log("dev: installing the web dependencies offline (the client post-step needs them)")
        lib.run([_pnpm(), "install", "--frozen-lockfile", "--offline"], cwd=self.app / "web", env=env,
                timeout=900, capture=True)
        lib.run(["go", "tool", "-modfile=tools.mod", "sqlc", "generate"], cwd=self.api, env=env, timeout=900,
                capture=True)
        lib.run([ctx.cli, "g", "client", "--non-interactive"], cwd=self.app, env=env, timeout=900, capture=True)
        if not self.client.is_file() or "mark-0000" not in self.client.read_text():
            raise lib.Failure(f"`spinup g client` wrote no marker into {self.client}",
                              "check that openapi-typescript keeps schema descriptions")
        # Warm the build cache, so every start compiles only the app packages that changed.
        lib.run(["go", "build", "-o", str(self.api / "tmp" / "api"), "./cmd/api"], cwd=self.api, env=env,
                timeout=1800, capture=True)

    def _set_env_db(self):
        """Writes DATABASE_URL into the .env files of the app (temp dir only)."""
        for e in (self.app / ".env", self.api / ".env"):
            if e.is_file():
                text = e.read_text()
                if re.search(r"(?m)^DATABASE_URL=", text):
                    e.write_text(re.sub(r"(?m)^DATABASE_URL=.*$", lambda _m: f"DATABASE_URL={self.db_url}", text))

    def _mark_schema(self):
        """Adds a description with the marker to the first schema of the schema file."""
        text = self.schema.read_text()
        m = re.search(r"(?m)^([A-Za-z][\w]*):\n", text)
        if not m:
            raise lib.Failure(f"{self.schema} has no top-level schema name", "check the OpenAPI schema layout")
        self.schema.write_text(text[:m.end()] + "  description: perf mark-0000\n" + text[m.end():])

    # Process control.

    def start(self):
        """Spawns `spinup dev`. Returns the monotonic spawn time."""
        self.port = _free_port()
        with self.lock:
            self.lines = []
        argv = [str(self.ctx.cli), *DEV_ARGS, "--api-port", str(self.port)]
        t0 = time.monotonic()
        self.proc = subprocess.Popen(argv, cwd=self.app, env=self.env(), stdin=subprocess.DEVNULL,
                                     stdout=subprocess.PIPE, stderr=subprocess.STDOUT, start_new_session=True,
                                     text=True, errors="replace", bufsize=1)
        lib.track(self.proc.pid)
        self.reader = threading.Thread(target=self._read, args=(self.proc,), daemon=True)
        self.reader.start()
        return t0

    def _read(self, proc):
        with open(self.log_path, "a") as f:
            for line in proc.stdout:
                if "HTTP request" in line:  # the 5 ms polls: keep them out of the log
                    continue
                clean = ANSI.sub("", line)
                with self.lock:
                    self.lines.append((time.monotonic(), clean))
                f.write(clean)

    def tail(self, n=25):
        with self.lock:
            return "".join(line for _, line in self.lines[-n:])

    def stop(self):
        """Stops `spinup dev` like Ctrl-C, then kills every process that it left."""
        p, self.proc = self.proc, None
        if p is None:
            return
        kids = _descendants(p.pid)
        if p.poll() is None:
            try:
                p.send_signal(signal.SIGINT)
            except ProcessLookupError:
                pass
            try:
                p.wait(timeout=20)
            except subprocess.TimeoutExpired:
                _kill(p.pid, group=True)
                p.wait()
        # The dev command starts air and the API in their own process groups. Kill what stays.
        _kill(p.pid, group=True)
        for pid in kids:
            _kill(pid, group=True)
            _kill(pid)
        lib.untrack(p.pid)
        if self.reader is not None:
            self.reader.join(5)
            self.reader = None
        if self.port is not None and not _wait_for(lambda: not _port_open(self.port), 8, 0.1):
            r = subprocess.run(["lsof", "-nP", f"-iTCP:{self.port}", "-sTCP:LISTEN", "-t"], capture_output=True,
                               text=True)
            for pid in r.stdout.split():
                if pid.isdigit():
                    _kill(int(pid))

    def close(self):
        try:
            self.stop()
        finally:
            if self.db_name:
                lib.drop_db(self.ctx, self.db_name)
                self.db_name = None

    def _alive(self):
        if self.proc is not None and self.proc.poll() is not None:
            raise lib.Failure(f"spinup dev exited with code {self.proc.returncode}\n{self.tail()}",
                              "run `spinup dev --no-web --no-infra` in the app and read the output")

    def ready(self, timeout=START_TIMEOUT_S):
        """Waits for the first 200 from /healthz. Returns the monotonic time of that answer."""
        done = {}

        def ok():
            self._alive()
            r = _http_get(self.port, HEALTHZ, timeout=0.5)
            if r is not None and r[0] == 200:
                done["t"] = time.monotonic()
                return True
            return False

        if not _wait_for(ok, timeout, HEALTH_POLL_S):
            raise lib.Failure(f"spinup dev did not answer {HEALTHZ} in {timeout:.0f} s\n{self.tail()}",
                              "run `spinup dev --no-web --no-infra` in the app and read the output")
        return done["t"]

    # Conformance.

    def conform(self):
        """Checks the probes before any timing. A failure stops the suite (ADR 0034 §6.3)."""
        for path in (GREETING, QUERY):
            r = _http_get(self.port, path, timeout=10)
            if r is None or r[0] != 200 or not MARK.search(r[1]):
                raise lib.Failure(f"the probe {path} answered {r!r}, not 200 with a marker\n{self.tail()}",
                                  "check the probe routes of scripts/perf/suite_dev.py")
        if not MARK.search(self.client.read_text()):
            raise lib.Failure(f"{self.client} has no marker", "run `spinup g client` in the app")

    # Edits.

    def next_mark(self):
        self.marks += 1
        return f"mark-{self.marks:04d}"

    def wait_body(self, path, mark, t0, what):
        """Polls path until a 200 answer has mark. Returns the ms since t0."""
        def ok():
            self._alive()
            r = _http_get(self.port, path)
            return r is not None and r[0] == 200 and mark in r[1]

        if not _wait_for(ok, SAMPLE_TIMEOUT_S, POLL_S):
            raise lib.Failure(f"{what}: {path} did not show {mark} in {SAMPLE_TIMEOUT_S:.0f} s\n{self.tail()}",
                              "read the dev output above")
        return (time.monotonic() - t0) * 1000

    def listening_after(self, t0):
        """The time of the first "HTTP server listening" line after t0, or None."""
        with self.lock:
            for t, line in self.lines:
                if t >= t0 and "HTTP server listening" in line:
                    return t
        return None

    def handler_edit(self):
        """One handler edit. Returns {"ms", "ready_ms", "response_ms"} (the last two when known)."""
        mark = self.next_mark()
        src = MARK.sub(mark, self.routes.read_text())
        t0 = time.monotonic()
        self.routes.write_text(src)
        ms = self.wait_body(GREETING, mark, t0, "handler edit")
        rec = {"ms": ms}
        t1 = t0 + ms / 1000
        ready = self.listening_after(t0)
        if ready is not None:
            ready = min(ready, t1)
            rec["ready_ms"] = (ready - t0) * 1000
            rec["response_ms"] = (t1 - ready) * 1000
        return rec

    def query_edit(self):
        mark = self.next_mark()
        src = MARK.sub(mark, self.query.read_text())
        t0 = time.monotonic()
        self.query.write_text(src)
        # air builds on the .sql change itself. When that build starts before the dev watcher has
        # run sqlc, the new api/internal/store code never reaches a build, because .air.toml
        # excludes internal/store. The sample then times out, and the message names the race.
        try:
            return self.wait_body(QUERY, mark, t0, "query edit")
        except lib.Failure as e:
            store = self.api / "internal" / "store" / "perf_mark.sql.go"
            try:
                generated = mark in store.read_text()
            except OSError:
                generated = False
            if not generated:
                raise
            raise lib.Failure(f"{e}\nsqlc wrote {mark} into {store.name}, but no air build picked it up: air "
                              "built on the .sql change before sqlc finished, and api/.air.toml excludes "
                              "internal/store", "remove internal/store from exclude_dir in api/.air.toml") from e

    def openapi_edit(self):
        mark = self.next_mark()
        src = MARK.sub(mark, self.schema.read_text())
        t0 = time.monotonic()
        self.schema.write_text(src)

        def ok():
            self._alive()
            try:
                return mark in self.client.read_text()
            except OSError:
                return False

        if not _wait_for(ok, SAMPLE_TIMEOUT_S, POLL_S):
            raise lib.Failure(f"OpenAPI edit: {self.client} did not show {mark} in {SAMPLE_TIMEOUT_S:.0f} s\n"
                              + self.tail(), "read the dev output above")
        return (time.monotonic() - t0) * 1000

    def compile_error(self, i):
        """Writes a type error, waits for the error line, then fixes it. Returns (feedback ms, recovery ms, line)."""
        original = self.routes.read_text()
        if ERR_ANCHOR not in original:
            raise lib.Failure(f"the anchor line of the compile error is missing in {self.routes}",
                              "the route template of suite_dev.py changed")
        broken = original.replace(ERR_ANCHOR, ERR_LINE.format(i=i) + ERR_ANCHOR, 1)
        try:
            t0 = time.monotonic()
            self.routes.write_text(broken)
            found = {}

            def ok():
                self._alive()
                with self.lock:
                    for t, line in self.lines:
                        if t >= t0 and ERROR_LINE.search(line):
                            found["t"], found["line"] = t, line.strip()
                            return True
                return False

            if not _wait_for(ok, SAMPLE_TIMEOUT_S, POLL_S):
                raise lib.Failure(f"compile error: no error line in the dev output in {SAMPLE_TIMEOUT_S:.0f} s\n"
                                  + self.tail(), "read the dev output above")
            feedback = (found["t"] - t0) * 1000
        except BaseException:
            self.routes.write_text(original)
            raise
        mark = self.next_mark()
        fixed = MARK.sub(mark, original)
        t1 = time.monotonic()
        self.routes.write_text(fixed)
        recovery = self.wait_body(GREETING, mark, t1, "compile error recovery")
        return feedback, recovery, found["line"]


# ---------------------------------------------------------------------------
# The suite
# ---------------------------------------------------------------------------

DEV_NOTE = ("`spinup dev --no-web --no-infra --no-worker` (preflight, migrate up, then air with delay 300 ms; "
            "the dev watcher polls api/db/queries and api/openapi every 500 ms with a 300 ms debounce)")

METRICS = [
    ("dev_cold_start", None), ("dev_handler_edit", None),
    ("dev_breakdown_edit_to_ready", "a phase of dev_handler_edit"),
    ("dev_breakdown_ready_to_response", "a phase of dev_handler_edit"),
    ("dev_query_edit", None), ("dev_openapi_edit", None),
    ("dev_compile_error_feedback", None), ("dev_compile_error_recovery", None),
]


def _cold_start(ctx, dev, recs):
    def fn(i, warm):
        try:
            t0 = dev.start()
            t1 = dev.ready()
            return (t1 - t0) * 1000
        finally:
            dev.stop()

    recs.append(lib.measure(
        ctx, SUITE, "dev_cold_start", fn, n=ctx.n(3, 1), unit="ms", warmup=0,
        note=f"{DEV_NOTE}; spawn to the first 200 from {HEALTHZ} (2 ms polls, new connection). The Go build "
             "cache is warm and the database is migrated, so a start runs the preflight, `go run ./cmd/api "
             "migrate up`, and the air build of the API. No warm-up: setup built the API once"))


def _edits(ctx, dev, recs):
    warm = ctx.n(1, 0)
    n = ctx.n(5, 1)
    dev.start()
    try:
        dev.ready()
        dev.conform()
        time.sleep(QUIET_S)

        def quiet(fn):
            def f(i, is_warm):
                try:
                    return fn(i, is_warm)
                finally:
                    time.sleep(QUIET_S)
            return f

        side = {"ready": [], "response": []}

        def handler(i, is_warm):
            r = dev.handler_edit()
            if not is_warm:
                if "ready_ms" in r:
                    side["ready"].append(r["ready_ms"])
                    side["response"].append(r["response_ms"])
            return r["ms"]

        base = f"{DEV_NOTE}; {QUIET_S} s quiet after each edit"
        primary = lib.measure(ctx, SUITE, "dev_handler_edit", quiet(handler), n=n, warmup=warm,
                              note=f"{base}; the file write of a new marker in the handler body of "
                                   f"api/internal/routes/routes.go to the first 200 from {GREETING} with that "
                                   "marker (5 ms polls, new connection)")
        recs.append(primary)
        fails = [p for p in primary["note"].split("; ") if p.startswith("failed at") or p.startswith("load_gate")]
        info = "a phase of dev_handler_edit"
        recs.append(lib.record(SUITE, "dev_breakdown_edit_to_ready", "ms", side["ready"], warmup=warm,
                               metric_class="informational", informational=info,
                               note="; ".join(["the edit to the 'HTTP server listening' line of the new API "
                                               "process (air delay, build, link, stop the old API, start the "
                                               "new one), read from the dev output pipe", *fails])))
        recs.append(lib.record(SUITE, "dev_breakdown_ready_to_response", "ms", side["response"], warmup=warm,
                               metric_class="informational", informational=info,
                               note="; ".join(["the listening line to the first answer with the new marker",
                                               *fails])))
        lib.write_results(ctx)

        recs.append(lib.measure(
            ctx, SUITE, "dev_query_edit", quiet(lambda i, w: dev.query_edit()), n=n, warmup=warm,
            note=f"{base}; the file write of a new marker in api/db/queries/perf_mark.sql to the first 200 from "
                 f"{QUERY} with that marker (the dev watcher runs sqlc, then air builds the API)"))
        lib.write_results(ctx)

        recs.append(lib.measure(
            ctx, SUITE, "dev_openapi_edit", quiet(lambda i, w: dev.openapi_edit()), n=n, warmup=warm,
            note=f"{base}; the file write of a new marker in the description of the first schema in "
                 f"api/openapi/schemas/{dev.schema.name} to that marker in web/src/lib/api/schema.d.ts (the dev "
                 "watcher bundles the OpenAPI, runs oapi-codegen, then openapi-typescript; 5 ms polls of the file)"))
        lib.write_results(ctx)

        side_r = []
        example = {}

        def comp(i, is_warm):
            fb, rc, line = dev.compile_error(dev.marks + 1)
            example.setdefault("line", line)
            if not is_warm:
                side_r.append(rc)
            return fb

        fb = lib.measure(ctx, SUITE, "dev_compile_error_feedback", quiet(comp), n=n, warmup=warm,
                         note=f"{base}; the file write of a type error in the greeting handler to the first "
                              "compiler error line (routes.go:<line>:<col>:) in the dev output, which air "
                              "prints from its failed build")
        if example.get("line"):
            fb["note"] += f"; the error line: {example['line'][:200]}"
        recs.append(fb)
        fails = [p for p in fb["note"].split("; ") if p.startswith("failed at") or p.startswith("load_gate")]
        recs.append(lib.record(SUITE, "dev_compile_error_recovery", "ms", side_r, warmup=warm,
                               note="; ".join([f"{base}; after the error line, the file write of the fixed "
                                               f"handler with a new marker to the first 200 from {GREETING} "
                                               "with that marker", *fails])))
    finally:
        dev.stop()


def run(ctx):
    recs = ctx.live
    parent = ctx.tmp("dev")
    dev = DevApp(ctx, parent)
    try:
        try:
            lib.log(f"dev: generating the scenario app ({len(ctx.resources)} resources) and building it")
            dev.setup()
        except Exception as e:
            msg = f"could not set up the scenario app: {str(e).splitlines()[0][:300] if str(e) else type(e).__name__}"
            for metric, info in METRICS:
                recs.append(lib.skipped(SUITE, metric, "ms", msg, informational=info))
            raise
        failed = []
        for part in (_cold_start, _edits):
            try:
                part(ctx, dev, recs)
            except Exception as e:  # keep the finished records, and run the next part
                traceback.print_exc()
                first = str(e).splitlines()[0][:200] if str(e) else ""
                ctx.notes.append(f"dev suite: {part.__name__} stopped early with {type(e).__name__}: {first}. "
                                 "Its finished metrics are kept.")
                failed.append(f"{part.__name__}: {first}")
            lib.write_results(ctx)
        if failed:
            raise lib.Failure("dev suite parts failed: " + "; ".join(failed), "read the traceback above")
    finally:
        dev.close()
        shutil.rmtree(parent, ignore_errors=True)
    return recs
