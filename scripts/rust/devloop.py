#!/usr/bin/env python3
"""Re-measure the R0.11 dev loop on a generated app with the real kit (R2 gate item 4).

Usage (make rs-devloop runs it):
  python3 -I scripts/rust/devloop.py --spinup <bin> --kit <dir> --resources <file> \
      --edits N --series N --limit-ms MS --out <json>

The script:
  1. Runs `spinup new` with the kit through SPINUP_KIT_REPLACE and the modules
     ci, auth, jobs, and mail, then one `spinup g resource` for each line of
     --resources. Generation runs without a database (the no-database rule).
  2. Adds a hand route `GET /api/greeting` to api/src/routes.rs. It runs a
     `query_scalar!` count and answers with a marker, such as `mark-0003`.
  3. Creates a new database from DATABASE_URL, runs `spinup db prepare`
     (migrations and the .sqlx cache), and builds the API with `cargo build`
     (the shipped dev profile of api/Cargo.toml).
  4. Measures each series with the R0.11 own driver, inline: after the edit it
     waits 10 ms (the supervisor debounce), runs `cargo build`, stops the old
     app, starts the new binary, and polls /healthz every 2 ms.

A sample starts just before the script writes the edited file. It ends at the
first 200 answer from /api/greeting whose body has the new marker. A second
thread polls every 5 ms with a new connection. Each series has 1 warm-up edit
and N measured edits, with 1.5 s of quiet between edits. Before each series the
script waits while the 1-minute load average is 4 or more (at most 20 minutes),
and it records `uptime` before and after each series.

Environment: DATABASE_URL (the server for the new database, never port 55432)
and CARGO_TARGET_DIR (the build directory of the app). The script removes the
app process, the temporary app directory, and the database on exit and on
Ctrl-C. It uses the Python standard library only.
"""

import argparse
import http.client
import json
import os
import platform
import re
import shlex
import shutil
import signal
import socket
import statistics
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
from pathlib import Path

MODULES = "ci,auth,jobs,mail"
GREETING = "/api/greeting"
MARK = re.compile(r"mark-\d{4}")
WARMUP = 1
QUIET_S = 1.5
DEBOUNCE_S = 0.010
POLL_S = 0.005
HEALTH_POLL_S = 0.002
SAMPLE_TIMEOUT_S = 300.0
FORBIDDEN_PORT = 55432

ROUTES = """\
//! The application routes.

use axum::Router;
use axum::extract::State;
use axum::routing::get;
use kit::db::PgPool;
use kit::httpx::ApiResult;

/// Registers the application routes.
pub fn routes() -> Router<PgPool> {
    // Add application routes here. Generators never edit this file.
    Router::new().route("/api/greeting", get(greeting))
}

/// Answers the dev loop probe of scripts/rust/devloop.py. The script edits the marker.
async fn greeting(State(db): State<PgPool>) -> ApiResult<String> {
    let count = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM TABLE"#)
        .fetch_one(&db)
        .await
        .map_err(anyhow::Error::from)?;
    Ok(format!("{count} rows in TABLE, mark-0000"))
}
"""


class Failure(Exception):
    """A failed step. The message names what failed and why, the hint is the Try: line."""

    def __init__(self, message, hint):
        super().__init__(message)
        self.hint = hint


def log(msg):
    print(f"devloop: {msg}", flush=True)


def load1():
    return os.getloadavg()[0]


def uptime():
    return subprocess.run(["uptime"], capture_output=True, text=True).stdout.strip()


def wait_idle(limit, max_wait):
    start = time.monotonic()
    while load1() >= limit and time.monotonic() - start < max_wait:
        log(f"load {load1():.2f} >= {limit}, waiting")
        time.sleep(min(15.0, max(0.0, max_wait - (time.monotonic() - start))) or 0.1)
    return time.monotonic() - start


def stats(xs):
    """Median, p90 (nearest rank: the value at rank ceil(0.9 n)), min, and max in ms."""
    xs = sorted(xs)
    if not xs:
        return {"n": 0}
    rank = max(1, -(-9 * len(xs) // 10))
    return {
        "n": len(xs),
        "median_ms": round(statistics.median(xs), 1),
        "p90_ms": round(xs[rank - 1], 1),
        "min_ms": round(xs[0], 1),
        "max_ms": round(xs[-1], 1),
    }


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def get(port, path, timeout=2.0):
    """GET path on the app with a new connection. Returns (status, body) or None."""
    c = http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)
    try:
        c.request("GET", path, headers={"Connection": "close"})
        r = c.getresponse()
        return r.status, r.read().decode("utf-8", "replace")
    except (OSError, http.client.HTTPException):
        return None
    finally:
        c.close()


def wait_for(pred, timeout, interval):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if pred():
            return True
        time.sleep(interval)
    return False


def tail(path, lines=30):
    try:
        return "\n".join(Path(path).read_text(errors="replace").splitlines()[-lines:])
    except OSError:
        return ""


def database_url(base, name):
    """Returns base with the database name replaced. Fails on port 55432."""
    u = urllib.parse.urlsplit(base)
    if u.scheme not in ("postgres", "postgresql"):
        raise Failure(f"DATABASE_URL has the scheme {u.scheme!r}, not postgres",
                      "export DATABASE_URL=postgres://user:pass@127.0.0.1:55433/postgres")
    if u.port == FORBIDDEN_PORT:
        raise Failure(f"DATABASE_URL uses port {FORBIDDEN_PORT}, which belongs to another project",
                      "point DATABASE_URL at the spinup test server (127.0.0.1:55433)")
    return urllib.parse.urlunsplit((u.scheme, u.netloc, "/" + name, u.query, u.fragment))


class Run:
    """The app, its database, and the processes. close() removes all of them."""

    def __init__(self, a):
        self.a = a
        self.db_name = f"spinup_devloop_{os.getpid()}"
        self.db_url = database_url(os.environ["DATABASE_URL"], self.db_name)
        self.work = Path(tempfile.mkdtemp(prefix="spinup-devloop-"))
        self.app = self.work / "app"
        self.api = self.app / "api"
        self.logs = self.work / "logs"
        self.logs.mkdir()
        self.db_created = False
        self.proc = None
        self.proc_log = None
        self.port = free_port()
        self.target = Path(os.environ["CARGO_TARGET_DIR"])
        self.binary = self.target / "debug" / "api"
        self.routes = self.api / "src" / "routes.rs"
        self.table = None

    # Environments.

    def base_env(self):
        env = dict(os.environ)
        for k in ("DATABASE_URL", "DATABASE_URL_FILE", "SQLX_OFFLINE"):
            env.pop(k, None)
        return env

    def db_env(self):
        env = self.base_env()
        env["DATABASE_URL"] = self.db_url
        return env

    def cargo_env(self):
        # SQLX_OFFLINE comes from api/.cargo/config.toml (the shipped dev setup).
        env = self.db_env()
        env["CARGO_TARGET_DIR"] = str(self.target)
        return env

    def app_env(self):
        env = self.db_env()
        env.update({"SPINUP_ENV": "dev", "PORT": str(self.port), "RUST_BACKTRACE": "0"})
        return env

    # Steps.

    def run(self, name, cmd, cwd, env, hint):
        path = self.logs / f"{name}.log"
        with open(path, "a") as f:
            f.write(f"$ {shlex.join(str(c) for c in cmd)}\n")
            f.flush()
            r = subprocess.run(cmd, cwd=cwd, env=env, stdout=f, stderr=subprocess.STDOUT,
                               stdin=subprocess.DEVNULL)
        if r.returncode != 0:
            print(tail(path), file=sys.stderr)
            raise Failure(f"{shlex.join(str(c) for c in cmd)} failed with exit code {r.returncode}", hint)

    def generate(self, resources):
        a = self.a
        env = self.base_env()
        env["SPINUP_KIT_REPLACE"] = str(a.kit)
        log(f"spinup new app --modules {MODULES} in {self.work}")
        self.run("spinup", [a.spinup, "new", "app", "--module", "example.com/app",
                            "--modules", MODULES, "--no-git", "--non-interactive"],
                 self.work, env, "run the spinup new command of the log by hand")
        # The resolver reads the .env files. Hide them, so generation needs no database.
        hidden = []
        for e in (self.app / ".env", self.api / ".env"):
            if e.exists():
                e.rename(e.with_name(e.name + ".devloop"))
                hidden.append(e)
        before = {p.name for p in (self.api / "migrations").glob("*.up.sql")}
        for line in resources:
            log(f"spinup g resource {line}")
            self.run("spinup", [a.spinup, "g", "resource", *shlex.split(line), "--non-interactive"],
                     self.app, env, "check the resource line and the generator output")
        for e in hidden:
            e.with_name(e.name + ".devloop").rename(e)
            text = e.read_text()
            if re.search(r"(?m)^DATABASE_URL=", text):
                e.write_text(re.sub(r"(?m)^DATABASE_URL=.*$", f"DATABASE_URL={self.db_url}", text))
        new = sorted(p for p in (self.api / "migrations").glob("*.up.sql") if p.name not in before)
        for p in new:
            m = re.search(r"CREATE TABLE (?:IF NOT EXISTS )?\"?(\w+)", p.read_text())
            if m:
                self.table = m.group(1)
                break
        if self.table is None:
            raise Failure("no generated migration creates a table",
                          "check that --resources names at least one resource")
        self.routes.write_text(ROUTES.replace("TABLE", self.table))

    def create_db(self):
        log(f"create database {self.db_name}")
        self.run("sqlx", ["sqlx", "database", "create", "--no-dotenv", "-D", self.db_url],
                 self.work, self.base_env(), "check that DATABASE_URL reaches the server and may create databases")
        self.db_created = True

    def drop_db(self):
        if not self.db_created:
            return
        r = subprocess.run(["sqlx", "database", "drop", "-y", "--force", "--no-dotenv", "-D", self.db_url],
                           cwd=self.work, env=self.base_env(), capture_output=True, text=True)
        if r.returncode == 0:
            self.db_created = False
        else:
            print(f"devloop: drop database {self.db_name} failed: {r.stderr.strip()}", file=sys.stderr)
            print(f"Try: sqlx database drop -y --force -D <server>/{self.db_name}", file=sys.stderr)

    def prepare(self):
        log("spinup db prepare (migrations and .sqlx)")
        env = self.cargo_env()
        self.run("prepare", [self.a.spinup, "db", "prepare"], self.app, env,
                 "run spinup db prepare in the app by hand")

    def build(self):
        """Runs cargo build in api/. Returns the time in ms."""
        t = time.monotonic()
        self.run("build", ["cargo", "build", "--offline", "--locked"], self.api, self.cargo_env(),
                 "fix the build error in the log, then run the script again")
        return (time.monotonic() - t) * 1000

    def start(self):
        if self.proc_log is None:
            self.proc_log = open(self.logs / "app.log", "a")
        self.proc = subprocess.Popen([str(self.binary), "serve"], cwd=self.api, env=self.app_env(),
                                     stdout=self.proc_log, stderr=subprocess.STDOUT,
                                     stdin=subprocess.DEVNULL, start_new_session=True)

    def stop(self):
        p, self.proc = self.proc, None
        if p is None or p.poll() is not None:
            return
        try:
            os.killpg(p.pid, signal.SIGTERM)
        except ProcessLookupError:
            return
        try:
            p.wait(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(p.pid, signal.SIGKILL)
            p.wait()

    def ready(self, timeout):
        def ok():
            if self.proc is not None and self.proc.poll() is not None:
                raise Failure(f"the app stopped with exit code {self.proc.returncode}",
                              "read the app log above")
            r = get(self.port, "/healthz", timeout=0.5)
            return r is not None and r[0] == 200

        try:
            return wait_for(ok, timeout, HEALTH_POLL_S)
        except Failure:
            print(tail(self.logs / "app.log"), file=sys.stderr)
            raise

    def close(self):
        self.stop()
        if self.proc_log is not None:
            self.proc_log.close()
        self.drop_db()
        shutil.rmtree(self.work, ignore_errors=True)

    # Measurement.

    def sample(self, src, i):
        """One edit. Returns (src, record)."""
        mark = f"mark-{i:04d}"
        src = MARK.sub(mark, src)
        done = {}
        stop = threading.Event()

        def poll():
            while not stop.is_set():
                r = get(self.port, GREETING)
                if r is not None and r[0] == 200 and mark in r[1]:
                    done["t1"] = time.monotonic()
                    return
                time.sleep(POLL_S)

        t0 = time.monotonic()
        self.routes.write_text(src)
        poller = threading.Thread(target=poll, daemon=True)
        poller.start()
        try:
            time.sleep(DEBOUNCE_S)
            build_ms = self.build()
            t = time.monotonic()
            self.stop()
            stop_ms = (time.monotonic() - t) * 1000
            spawned = time.monotonic()
            self.start()
            if not self.ready(60):
                print(tail(self.logs / "app.log"), file=sys.stderr)
                raise Failure(f"edit {i}: the new app did not answer /healthz in 60 s", "read the app log above")
            ready_t = time.monotonic()
            poller.join(SAMPLE_TIMEOUT_S)
        finally:
            stop.set()
        if "t1" not in done:
            print(tail(self.logs / "app.log"), file=sys.stderr)
            raise Failure(f"edit {i}: {GREETING} did not show {mark} in {SAMPLE_TIMEOUT_S:.0f} s",
                          "read the app log above")
        t1 = done["t1"]
        rec = {
            "ms": round((t1 - t0) * 1000, 1),
            "build_ms": round(build_ms, 1),
            "stop_ms": round(stop_ms, 1),
            "ready_ms": round((ready_t - spawned) * 1000, 1),
            "response_ms": round(max(0.0, t1 - ready_t) * 1000, 1),
            "load1": round(load1(), 2),
        }
        return src, rec

    def series(self, n):
        a = self.a
        waited = wait_idle(a.load_limit, a.max_wait_s)
        before, load_before = uptime(), load1()
        original = self.routes.read_text()
        samples = []
        try:
            self.start()
            if not self.ready(120) or not wait_for(
                    lambda: (r := get(self.port, GREETING)) is not None and r[0] == 200, 60, 0.02):
                print(tail(self.logs / "app.log"), file=sys.stderr)
                raise Failure(f"series {n}: the app did not answer {GREETING}", "read the app log above")
            time.sleep(QUIET_S)
            src = original
            for i in range(1, WARMUP + a.edits + 1):
                src, rec = self.sample(src, i)
                kind = "warm-up" if i <= WARMUP else "edit"
                log(f"series {n} {kind} {i}: {rec['ms']:.0f} ms (build {rec['build_ms']:.0f}, "
                    f"stop {rec['stop_ms']:.0f}, ready {rec['ready_ms']:.0f}, "
                    f"response {rec['response_ms']:.0f}, load {rec['load1']})")
                if i > WARMUP:
                    samples.append(rec)
                time.sleep(QUIET_S)
        finally:
            self.stop()
            self.routes.write_text(original)
        after, load_after = uptime(), load1()
        # A rebuild with the original source, so the next series starts warm.
        self.build()
        s = stats([r["ms"] for r in samples])
        steps = {k: round(statistics.median(r[k] for r in samples), 1)
                 for k in ("build_ms", "stop_ms", "ready_ms", "response_ms")} if samples else {}
        return {
            "series": n,
            **s,
            "step_medians": steps,
            "samples": samples,
            "waited_for_idle_s": round(waited, 1),
            "uptime_before": before,
            "uptime_after": after,
            "load1_before": round(load_before, 2),
            "load1_after": round(load_after, 2),
            "limit_ms": a.limit_ms,
            "pass": bool(samples) and s["median_ms"] <= a.limit_ms,
        }


def read_resources(path):
    lines = []
    for raw in Path(path).read_text().splitlines():
        line = raw.strip()
        if line and not line.startswith("#"):
            lines.append(line)
    if not lines:
        raise Failure(f"{path} has no resource lines", "add lines such as: todo title:string done:bool")
    return lines


def parse_args(argv):
    ap = argparse.ArgumentParser(description="Re-measure the dev loop on a generated app (R2 gate item 4).")
    ap.add_argument("--spinup", required=True, help="the spinup binary")
    ap.add_argument("--kit", required=True, help="the kit directory (crates/spinup-kit)")
    ap.add_argument("--resources", required=True, help="one `g resource` argument line per resource")
    ap.add_argument("--edits", type=int, default=10, help="measured edits per series (after 1 warm-up)")
    ap.add_argument("--series", type=int, default=2, help="the number of series")
    ap.add_argument("--limit-ms", type=float, default=5000, help="the limit of each series median")
    ap.add_argument("--out", required=True, help="the JSON result file")
    ap.add_argument("--load-limit", type=float, default=4.0, help="wait while the 1-minute load is this or more")
    ap.add_argument("--max-wait-s", type=float, default=1200, help="the longest load wait per series")
    a = ap.parse_args(argv)
    if a.edits < 1 or a.series < 1:
        raise Failure("--edits and --series must be 1 or more", "pass --edits 10 --series 2")
    a.spinup = str(Path(a.spinup).resolve())
    a.kit = str(Path(a.kit).resolve())
    a.out = Path(a.out).resolve()
    if not Path(a.spinup).is_file():
        raise Failure(f"the spinup binary {a.spinup} does not exist", "run cargo build --locked -p spinup")
    if not (Path(a.kit) / "Cargo.toml").is_file():
        raise Failure(f"the kit directory {a.kit} has no Cargo.toml", "pass --kit crates/spinup-kit")
    for k in ("DATABASE_URL", "CARGO_TARGET_DIR"):
        if not os.environ.get(k):
            raise Failure(f"{k} is empty", f"export {k}=... (make rs-devloop sets CARGO_TARGET_DIR)")
    return a


def on_signal(signum, _frame):
    raise KeyboardInterrupt(f"signal {signum}")


def main(argv):
    # A background job starts with SIGINT ignored. The cleanup must run on SIGINT anyway.
    signal.signal(signal.SIGINT, signal.default_int_handler)
    signal.signal(signal.SIGTERM, on_signal)
    signal.signal(signal.SIGHUP, on_signal)
    try:
        a = parse_args(argv)
        resources = read_resources(a.resources)
    except Failure as e:
        print(f"devloop: {e}\nTry: {e.hint}", file=sys.stderr)
        return 1
    run = None
    try:
        run = Run(a)
        run.generate(resources)
        run.create_db()
        run.prepare()
        log("first build")
        t = time.monotonic()
        run.build()
        first_build_s = (time.monotonic() - t)
        results = []
        for n in range(1, a.series + 1):
            rec = run.series(n)
            results.append(rec)
            log(f"series {n}: median {rec.get('median_ms')} ms, p90 {rec.get('p90_ms')} ms, "
                f"steps {rec['step_medians']}")
        out = {
            "unit": "R2 gate item 4: dev loop re-measure",
            "method": "spinup new + g resource per line, GET /api/greeting with query_scalar!, "
                      "own driver inline (10 ms debounce, cargo build, SIGTERM, start, /healthz every 2 ms), "
                      "sample = edit write to first 200 with the new marker, 5 ms polls, new connection",
            "modules": MODULES,
            "resources": len(resources),
            "resource_lines": resources,
            "table": run.table,
            "edits": a.edits,
            "warmup": WARMUP,
            "limit_ms": a.limit_ms,
            "first_build_s": round(first_build_s, 1),
            "binary_bytes": run.binary.stat().st_size,
            "machine": f"{platform.machine()} {platform.system()} {platform.release()}",
            "cargo_build_jobs": os.environ.get("CARGO_BUILD_JOBS", ""),
            "date": time.strftime("%Y-%m-%d"),
            "series": results,
            "pass": all(r["pass"] for r in results),
        }
        a.out.parent.mkdir(parents=True, exist_ok=True)
        a.out.write_text(json.dumps(out, indent=1) + "\n")
        log(f"wrote {a.out}")
        bad = [r for r in results if not r["pass"]]
        if bad:
            for r in bad:
                print(f"devloop: series {r['series']} median {r.get('median_ms')} ms is above the limit of "
                      f"{a.limit_ms:.0f} ms", file=sys.stderr)
            print("Try: check the load in the JSON, run make rs-devloop on an idle machine, "
                  "and open an R0.11 re-run if it still fails", file=sys.stderr)
            return 1
        return 0
    except Failure as e:
        print(f"devloop: {e}\nTry: {e.hint}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("devloop: interrupted; removing the app and the database", file=sys.stderr)
        return 130
    finally:
        # A second Ctrl-C must not stop the cleanup.
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        if run is not None:
            run.close()


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
