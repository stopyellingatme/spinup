"""Shared helpers of the spinup performance loop (scripts/perf).

Python 3 standard library only. Run the loop as `python3 -I scripts/perf/perf.py`.

Public API (the suites use these):
  Ctx                                   the run context (paths, env, records)
  record(...)                           build one metric record
  skipped(...)                          a record with no samples and a note
  measure(ctx, suite, metric, fn, ...)  gated, warmed-up timing of one series
  cmd_ms(argv, env, cwd)                run a command, return wall ms (raises on failure)
  run(argv, cwd, env, ...)              run a command in its own process group, tracked for cleanup
  peak_rss_mib(argv, env, cwd)          peak resident set size of a child in MiB
  load_gate(ctx, label)                 wait while the 1-minute load average is high
  create_db / drop_db / psql            temporary Postgres databases
  build_tools(ctx)                      build the CLI and the load generator once
  new_app / gen_resources / gen_app     generate the scenario app with the CLI
"""

import atexit
import datetime
import json
import math
import os
import platform
import shlex
import shutil
import signal
import statistics
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent

# The scenario. The cli suite generates an API-only app. The app suite needs the auth module
# for the login and the session list, and auth needs the web directory, so the app suite
# generates without --api-only (the web directory is never built).
CLI_MODULES = "ci,jobs,mail"
APP_MODULES = "ci,auth,jobs,mail"
APP_MODULE = "example.com/app"
RESOURCES_FILE = HERE / "resources.txt"
DEFAULT_DATABASE_URL = "postgres://postgres:postgres@127.0.0.1:55433/postgres?sslmode=disable"
FORBIDDEN_PORT = 55432
BUILD_JOBS = 4
NICE = 10
GATE_MAX_S = 900
GATE_MAX_S_QUICK = 30
GATE_BUDGET_S = 3600  # total load-gate wait of one run; after it the gates pass at once
GATE_BUDGET_S_QUICK = 120
MIN_FREE_GIB = {"cli": 2, "app": 6, "micro": 1}


def default_load_limit():
    """The load gate: wait while load1 is at or above 45 percent of the cores (4.5 on 10 cores)."""
    return round(max(2.0, 0.45 * (os.cpu_count() or 4)), 2)


class Failure(Exception):
    """A failed step. The message says what failed and why. The hint is the Try: line."""

    def __init__(self, message, hint="read the output above"):
        super().__init__(message)
        self.hint = hint


def log(msg):
    print(f"perf: {msg}", file=sys.stderr, flush=True)


def now_iso():
    return datetime.datetime.now().astimezone().isoformat(timespec="seconds")


def stamp():
    """A file-name timestamp, such as 20261105-215107."""
    return datetime.datetime.now().strftime("%Y%m%d-%H%M%S")


# ---------------------------------------------------------------------------
# Processes and cleanup
# ---------------------------------------------------------------------------

_CHILDREN = set()
_CHILD_LOCK = threading.Lock()


def _kill_group(pid, sig=signal.SIGTERM):
    try:
        os.killpg(pid, sig)
    except (ProcessLookupError, PermissionError):
        pass


def track(pid):
    """Tracks a child process group, so cleanup kills it."""
    with _CHILD_LOCK:
        _CHILDREN.add(pid)


def untrack(pid):
    """Stops tracking a child process group that has exited."""
    with _CHILD_LOCK:
        _CHILDREN.discard(pid)


def kill_children():
    """Terminates every tracked child process group (then kills the ones that stay)."""
    with _CHILD_LOCK:
        pids = list(_CHILDREN)
    for pid in pids:
        _kill_group(pid, signal.SIGTERM)
    deadline = time.monotonic() + 5
    while pids and time.monotonic() < deadline:
        pids = [p for p in pids if _alive(p)]
        if pids:
            time.sleep(0.1)
    for pid in pids:
        _kill_group(pid, signal.SIGKILL)


def _alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    # A zombie of ours still answers kill(0). waitpid reaps it if it is a child.
    try:
        done, _ = os.waitpid(pid, os.WNOHANG)
        return done == 0
    except ChildProcessError:
        return True


def run(argv, cwd=None, env=None, timeout=900, capture=False, check=True, stdin=subprocess.DEVNULL):
    """Runs argv in its own process group. Returns (returncode, stdout, stderr).

    With capture=False the output goes to /dev/null. A non-zero exit raises Failure when
    check is true. The group is killed on timeout and on interpreter exit.
    """
    p = subprocess.Popen([str(a) for a in argv], cwd=cwd, env=env, stdin=stdin,
                         stdout=subprocess.PIPE if capture else subprocess.DEVNULL,
                         stderr=subprocess.PIPE if capture else subprocess.DEVNULL,
                         start_new_session=True, text=True, errors="replace")
    track(p.pid)
    try:
        try:
            so, se = p.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            _kill_group(p.pid, signal.SIGKILL)
            p.communicate()
            raise Failure(f"{shlex.join(str(a) for a in argv)} did not finish in {timeout} s",
                          "check the machine load, then run the loop again")
        except BaseException:
            _kill_group(p.pid, signal.SIGKILL)
            p.wait()
            raise
    finally:
        untrack(p.pid)
    if check and p.returncode != 0:
        tail = "\n".join(((so or "") + (se or "")).splitlines()[-15:])
        raise Failure(f"{shlex.join(str(a) for a in argv)} failed with exit code {p.returncode}"
                      + (f"\n{tail}" if tail else ""), "run the command by hand in the same directory")
    return p.returncode, so or "", se or ""


def cmd_ms(argv, env=None, cwd=None, timeout=900):
    """Runs argv and returns the wall time in ms. Raises Failure on a non-zero exit."""
    t = time.perf_counter()
    run(argv, cwd=cwd, env=env, timeout=timeout, capture=True)
    return (time.perf_counter() - t) * 1000


def peak_rss_mib(argv, env=None, cwd=None, timeout=900):
    """Peak resident set size of argv (and its children) in MiB, or None when unmeasurable.

    macOS: /usr/bin/time -l (bytes). Linux: /usr/bin/time -v (KiB).
    """
    tm = "/usr/bin/time"
    if not os.access(tm, os.X_OK):
        return None
    if sys.platform == "darwin":
        flag = "-l"
    elif sys.platform.startswith("linux"):
        flag = "-v"
    else:
        return None
    _, _, err = run([tm, flag, *argv], cwd=cwd, env=env, timeout=timeout, capture=True)
    for line in err.splitlines():
        s = line.strip()
        if flag == "-l" and "maximum resident set size" in s:
            return int(s.split()[0]) / (1024 * 1024)
        if flag == "-v" and s.startswith("Maximum resident set size"):
            return int(s.rsplit(":", 1)[1]) / 1024
    return None


# ---------------------------------------------------------------------------
# Statistics and records
# ---------------------------------------------------------------------------


def median_p90(xs):
    """(median, p90) with nearest-rank p90 (the value at rank ceil(0.9 n)). (None, None) if empty."""
    xs = sorted(xs)
    if not xs:
        return None, None
    r = max(1, math.ceil(0.9 * len(xs)))
    return statistics.median(xs), xs[r - 1]


def _r(x, digits=3):
    return None if x is None else round(x, digits)


def record(suite, metric, unit, samples, better="lower", note="", warmup=0, informational=None):
    """One metric record (the shape that report.py and compare.py read).

    better is "lower" or "higher". informational is None for a primary metric, or the reason
    why compare does not fail on the metric (it repeats another measurement, or it is noisy
    by nature). compare fails only on a primary metric.
    """
    samples = [_r(s) for s in samples]
    med, p90 = median_p90(samples)
    rec = {
        "suite": suite, "metric": metric, "unit": unit,
        "samples": samples, "n": len(samples), "warmup_discarded": warmup,
        "median": _r(med), "p90": _r(p90), "better": better, "note": note,
        "informational": informational is not None,
    }
    if informational:
        rec["informational_reason"] = informational
    return rec


def skipped(suite, metric, unit, note, better="lower", informational=None):
    """A metric that could not be measured: no samples and the reason in the note."""
    return record(suite, metric, unit, [], better=better, note=note, informational=informational)


# ---------------------------------------------------------------------------
# Load gate and timing
# ---------------------------------------------------------------------------


def load1():
    return os.getloadavg()[0]


def uptime():
    try:
        return subprocess.run(["uptime"], capture_output=True, text=True).stdout.strip()
    except OSError:
        return ""


def load_gate(ctx, label=""):
    """Waits while the 1-minute load average is at or above ctx.load_limit.

    Waits at most ctx.gate_max_s for one call, and at most ctx.gate_budget_s over the whole
    run (the load average also counts the load of this run and lags about a minute, so a
    busy machine would otherwise stall every series for the full limit). Returns
    {"waited_s", "timed_out", "budget_exhausted", "load1"}.
    """
    start = time.monotonic()
    last_log = 0.0
    timed_out = False
    limit = min(ctx.gate_max_s, max(0.0, ctx.gate_budget_s - ctx.gate_spent))
    while load1() >= ctx.load_limit:
        waited = time.monotonic() - start
        if waited >= limit:
            timed_out = True
            break
        if waited - last_log >= 30 or last_log == 0.0:
            log(f"load gate{' (' + label + ')' if label else ''}: load {load1():.2f} >= "
                f"{ctx.load_limit}, waited {waited:.0f} s of {limit:.0f} s")
            last_log = max(waited, 0.001)
        time.sleep(min(5.0, max(0.1, limit - waited)))
    waited = time.monotonic() - start
    ctx.gate_spent += waited
    res = {"waited_s": round(waited, 1), "timed_out": timed_out,
           "budget_exhausted": timed_out and ctx.gate_spent >= ctx.gate_budget_s - 0.5,
           "load1": round(load1(), 2)}
    ctx.gate_events.append({"label": label, **res})
    return res


def measure(ctx, suite, metric, fn, n, unit="ms", better="lower", warmup=1, note="", gate=True,
            informational=None):
    """Times one series and returns its record.

    fn(i, is_warmup) returns a number in `unit`. Samples 0..warmup-1 are warm-ups and are
    discarded. An exception of fn stops the series: the record keeps the samples so far and
    the failure goes in the note. The load gate runs once before the series.
    """
    notes = []
    if gate:
        g = load_gate(ctx, metric)
        if g["timed_out"]:
            notes.append("load_gate_budget_exhausted" if g["budget_exhausted"] else "load_gate_timeout")
    before = round(load1(), 2)
    samples = []
    for i in range(warmup + n):
        is_warm = i < warmup
        try:
            v = fn(i, is_warm)
        except Exception as e:  # Failure, a dropped connection, a missing JSON key, ...
            lines = str(e).splitlines() or [""]
            what = lines[0][:200] if isinstance(e, Failure) else f"{type(e).__name__}: {lines[0][:200]}"
            if len(lines) > 1:
                what += f" (last output line: {lines[-1].strip()[:200]})"
            notes.append(f"failed at sample {i}: {what}")
            ctx.failures.append(f"{suite}/{metric}: {what}")
            log(f"{suite}/{metric}: sample {i} failed: {str(e)[:1500]}")
            break
        if not is_warm and v is not None:
            samples.append(v)
    after = round(load1(), 2)
    parts = [p for p in [note, *notes] if p]
    parts.append(f"n={len(samples)} after {warmup} warm-up discarded; load {before} -> {after}")
    rec = record(suite, metric, unit, samples, better=better, note="; ".join(parts), warmup=warmup,
                 informational=informational)
    log(f"{suite}/{metric}: median {rec['median']} {unit} (n={rec['n']})")
    return rec


# ---------------------------------------------------------------------------
# Context
# ---------------------------------------------------------------------------


class Ctx:
    """The run context.

    Fields:
      repo               Path of the repository root (read only)
      quick              bool: fewest samples
      out                Path of the JSON result file
      scratch            Path of the temp dir of this run (removed on exit)
      cli, loadgen       Paths of the built tools (set by build_tools)
      cli_size           bytes of the stripped CLI (set by build_tools)
      env                the process env of every command (kit replace, GOFLAGS, GOPROXY)
      kit                Path of the kit directory
      resources          the resource lines of the scenario
      database_url       the server URL (database name ignored); never port 55432
      load_limit, gate_max_s, gate_budget_s, gate_spent, gate_events
      records            the finished suites' metric records
      live               the running suite's records so far (written with every write_results,
                         so a crash keeps the partial suite)
      meta               dict written to the JSON (machine, toolchain, sha, parameters)
      suite_meta         {suite: {"uptime_before", "load1_before", ...}}
      notes              run-wide notes shown in the report
      dbs                set of temp databases to drop on exit
      failures           the series that stopped on a failed sample (perf.py then exits 1)
    """

    def __init__(self, repo, out, quick=False, load_limit=None, gate_max_s=None, gate_budget_s=None,
                 database_url=None):
        self.repo = Path(repo).resolve()
        self.out = Path(out).resolve()
        self.quick = quick
        self.load_limit = load_limit if load_limit is not None else default_load_limit()
        if gate_max_s is None:
            gate_max_s = GATE_MAX_S_QUICK if quick else GATE_MAX_S
        self.gate_max_s = gate_max_s
        if gate_budget_s is None:
            gate_budget_s = GATE_BUDGET_S_QUICK if quick else GATE_BUDGET_S
        self.gate_budget_s = gate_budget_s
        self.gate_spent = 0.0
        # Read the resource file before the scratch dir exists, so a failure leaks nothing.
        self.resources = load_resources()
        self.database_url = database_url or os.environ.get("DATABASE_URL") or DEFAULT_DATABASE_URL
        _server_url(self)  # fail early on a bad URL
        self.scratch = Path(tempfile.mkdtemp(prefix="spinup-perf-"))
        self.cli = None
        self.loadgen = None
        self.cli_size = None
        self.kit = self.repo / "kit"
        self.gate_events = []
        self.records = []
        self.live = []
        self.meta = {}
        self.suite_meta = {}
        self.notes = []
        self.dbs = set()
        self.failures = []
        self._db_counter = 0
        self.env = self._base_env()

    def suite_records(self):
        """A new list for the records of the running suite."""
        self.live = []
        return self.live

    def end_suite(self):
        """Moves the records of the running suite to ctx.records."""
        self.records.extend(self.live)
        self.live = []

    def _base_env(self):
        env = dict(os.environ)
        for k in ("DATABASE_URL", "DATABASE_URL_FILE", "GOFLAGS", "SPINUP_KIT_REPLACE", "GOWORK",
                  "OTEL_EXPORTER_OTLP_ENDPOINT", "SPINUP_TEMPLATES_DIR"):
            env.pop(k, None)
        env["SPINUP_KIT_REPLACE"] = str(self.kit)
        env["GOTOOLCHAIN"] = "local"
        env["GOFLAGS"] = f"-p={BUILD_JOBS}"
        env["GOPROXY"] = "off"
        return env

    def cmd_env(self, **extra):
        """A copy of the run env, with extra variables added."""
        env = dict(self.env)
        env.update({k: str(v) for k, v in extra.items()})
        return env

    def tmp(self, prefix="d"):
        """A new empty directory inside the scratch dir."""
        return Path(tempfile.mkdtemp(prefix=prefix + "-", dir=self.scratch))

    def n(self, full, quick):
        """The sample count: `full` normally, `quick` in quick mode."""
        return quick if self.quick else full

    def cleanup(self):
        kill_children()
        for name in list(self.dbs):
            drop_db(self, name)
        shutil.rmtree(self.scratch, ignore_errors=True)


def install_cleanup(ctx):
    """Cleans up on exit and on SIGINT, SIGTERM, and SIGHUP (processes, databases, temp dirs)."""
    done = []

    def once():
        if not done:
            done.append(1)
            ctx.cleanup()

    atexit.register(once)

    def on_signal(signum, _frame):
        raise KeyboardInterrupt(f"signal {signum}")

    signal.signal(signal.SIGINT, signal.default_int_handler)
    signal.signal(signal.SIGTERM, on_signal)
    signal.signal(signal.SIGHUP, on_signal)


def check_disk(ctx, suites, min_free_gib=None):
    """Fails when the free disk of the scratch dir is too small for the suites.

    min_free_gib overrides the estimate (0 turns the check off).
    """
    need = max([MIN_FREE_GIB.get(s, 1) for s in suites] or [1])
    if ctx.quick:
        need = max(1, need // 2)
    if min_free_gib is not None:
        need = min_free_gib
    if need <= 0:
        return
    free = shutil.disk_usage(ctx.scratch).free / 2**30
    if free < need:
        raise Failure(f"only {free:.1f} GiB are free on the disk of the scratch dir ({ctx.scratch}), and the "
                      f"suites {','.join(suites)} need about {need} GiB",
                      "free disk space, or pass --min-free-gb 0 to skip this check")
    ctx.meta.setdefault("preflight", {})["disk_need_gib"] = need


# ---------------------------------------------------------------------------
# Machine, toolchain, repository
# ---------------------------------------------------------------------------


def _sh(argv, env=None, cwd=None):
    try:
        r = subprocess.run(argv, capture_output=True, text=True, env=env, cwd=cwd, timeout=60)
        return (r.stdout or r.stderr).strip()
    except (OSError, subprocess.SubprocessError):
        return ""


def machine_info():
    cpu = ram = ""
    if sys.platform == "darwin":
        cpu = _sh(["sysctl", "-n", "machdep.cpu.brand_string"])
        mem = _sh(["sysctl", "-n", "hw.memsize"])
        ram = f"{int(mem) / 2**30:.0f} GiB" if mem.isdigit() else ""
        os_name = f"macOS {platform.mac_ver()[0]} ({platform.machine()})"
    else:
        try:
            for line in Path("/proc/cpuinfo").read_text().splitlines():
                if line.startswith("model name"):
                    cpu = line.split(":", 1)[1].strip()
                    break
            for line in Path("/proc/meminfo").read_text().splitlines():
                if line.startswith("MemTotal"):
                    ram = f"{int(line.split()[1]) / 2**20:.0f} GiB"
                    break
        except OSError:
            pass
        os_name = platform.platform()
    return {"cpu": cpu, "cores": os.cpu_count(), "ram": ram, "os": os_name, "kernel": platform.release(),
            "hostname": platform.node()}


def repo_info(path):
    sha = _sh(["git", "-C", str(path), "rev-parse", "HEAD"])
    branch = _sh(["git", "-C", str(path), "rev-parse", "--abbrev-ref", "HEAD"])
    dirty = bool(_sh(["git", "-C", str(path), "status", "--porcelain"]))
    return {"path": str(path), "sha": sha, "short": sha[:12], "branch": branch, "dirty": dirty}


def toolchain_info(ctx):
    return {
        "go": _sh(["go", "version"], env=ctx.env),
        "python": platform.python_version(),
        "psql": _sh(["psql", "--version"]) if shutil.which("psql") else "missing",
        "goflags": ctx.env.get("GOFLAGS", ""),
        "goflags_caller": os.environ.get("GOFLAGS", ""),
        "gomaxprocs": os.environ.get("GOMAXPROCS", ""),
        "nice": f"os.nice({NICE}) for the whole perf process",
    }


def collect_meta(ctx, suites, args_repr=""):
    ctx.meta = {
        "schema": 1,
        "started": now_iso(),
        "machine": machine_info(),
        "toolchain": toolchain_info(ctx),
        "repo": repo_info(ctx.repo),
        "quick": ctx.quick,
        "suites": suites,
        "scenario": {
            "cli_modules": CLI_MODULES, "cli_api_only": True,
            "app_modules": APP_MODULES, "app_api_only": False,
            "resources": len(ctx.resources), "resources_file": str(RESOURCES_FILE.relative_to(REPO)),
        },
        "database_server": _redact(ctx.database_url),
        "load_limit": ctx.load_limit,
        "gate_max_s": ctx.gate_max_s,
        "gate_budget_s": ctx.gate_budget_s,
        "args": args_repr,
    }


def _redact(url):
    u = urllib.parse.urlsplit(url)
    host = u.hostname or ""
    port = f":{u.port}" if u.port else ""
    return urllib.parse.urlunsplit((u.scheme, host + port, u.path, "", ""))


# ---------------------------------------------------------------------------
# Databases
# ---------------------------------------------------------------------------


def _server_url(ctx):
    u = urllib.parse.urlsplit(ctx.database_url)
    if u.scheme not in ("postgres", "postgresql"):
        raise Failure(f"DATABASE_URL has the scheme {u.scheme!r}, not postgres",
                      f"export DATABASE_URL={DEFAULT_DATABASE_URL}")
    if u.port == FORBIDDEN_PORT:
        raise Failure(f"DATABASE_URL uses port {FORBIDDEN_PORT}, which belongs to another project",
                      "point DATABASE_URL at the spinup test server (127.0.0.1:55433)")
    return u


def db_url(ctx, name):
    """The URL of the database `name` on the server of ctx.database_url."""
    u = _server_url(ctx)
    return urllib.parse.urlunsplit((u.scheme, u.netloc, "/" + name, u.query, u.fragment))


def psql(url, sql, timeout=120):
    """Runs one SQL statement with psql against url."""
    if not shutil.which("psql"):
        raise Failure("psql is not on PATH, and the perf loop needs it to create databases",
                      "install the PostgreSQL client (brew install libpq) and put psql on PATH")
    run(["psql", url, "-v", "ON_ERROR_STOP=1", "-q", "-c", sql], timeout=timeout, capture=True)


def _admin_url(ctx):
    u = _server_url(ctx)
    return urllib.parse.urlunsplit((u.scheme, u.netloc, u.path if u.path not in ("", "/") else "/postgres",
                                    u.query, u.fragment))


def create_db(ctx, prefix="perf"):
    """Creates a new database and returns (name, url). It is dropped on exit or by drop_db."""
    ctx._db_counter += 1
    name = f"spinup_{prefix}_{os.getpid()}_{ctx._db_counter}"
    try:
        psql(_admin_url(ctx), f'CREATE DATABASE "{name}"')
    except Failure as e:
        raise Failure(f"cannot create the database {name} on {_redact(ctx.database_url)}: "
                      f"{str(e).splitlines()[-1]}",
                      "start the spinup test Postgres server, or set DATABASE_URL to a server you own") from e
    ctx.dbs.add(name)
    return name, db_url(ctx, name)


def drop_db(ctx, name):
    try:
        psql(_admin_url(ctx), f'DROP DATABASE IF EXISTS "{name}" WITH (FORCE)')
        ctx.dbs.discard(name)
    except Failure as e:
        print(f"perf: drop database {name} failed: {str(e).splitlines()[0]}\n"
              f"Try: psql <server> -c 'DROP DATABASE \"{name}\" WITH (FORCE)'", file=sys.stderr)


# ---------------------------------------------------------------------------
# Building the tools
# ---------------------------------------------------------------------------


def build_tools(ctx):
    """Builds the CLI (stripped, as released) and the load generator into the scratch dir.

    Sets ctx.cli, ctx.loadgen, and ctx.cli_size.
    """
    bindir = ctx.scratch / "bin"
    bindir.mkdir(exist_ok=True)
    cli = bindir / "spinup"
    log(f"building the CLI from {ctx.repo}")
    t = time.perf_counter()
    run(["go", "build", "-trimpath", "-ldflags", "-s -w", "-o", str(cli), "./cmd/spinup"],
        cwd=ctx.repo, env=ctx.env, timeout=1800, capture=True)
    cli_s = time.perf_counter() - t
    lg = bindir / "loadgen"
    run(["go", "build", "-o", str(lg), "./scripts/perf/loadgen"], cwd=ctx.repo, env=ctx.env, timeout=600,
        capture=True)
    ctx.cli, ctx.loadgen = cli, lg
    ctx.cli_size = cli.stat().st_size
    ctx.meta.setdefault("builds", {})["cli"] = {
        "build_s": round(cli_s, 1), "flags": 'go build -trimpath -ldflags "-s -w"',
        "note": "warm build cache: this is not a cold-build metric",
    }


# ---------------------------------------------------------------------------
# Generating the scenario app
# ---------------------------------------------------------------------------


def load_resources():
    try:
        lines = [line.strip() for line in RESOURCES_FILE.read_text().splitlines()]
    except OSError as e:
        raise Failure(f"cannot read {RESOURCES_FILE}: {e}", "restore scripts/perf/resources.txt from git")
    lines = [line for line in lines if line and not line.startswith("#")]
    if not lines:
        raise Failure(f"{RESOURCES_FILE} has no resource lines", "add lines such as: todo title:string done:bool")
    return lines


def new_app(ctx, parent, name="app", git=False, modules=CLI_MODULES, api_only=True, timed=False):
    """Runs `spinup new <name>` in `parent`. Returns the app dir, or (app dir, ms) when timed."""
    parent = Path(parent)
    argv = [ctx.cli, "new", name, "--module", APP_MODULE, "--modules", modules, "--non-interactive"]
    if api_only:
        argv.append("--api-only")
    if not git:
        argv.append("--no-git")
    t = time.perf_counter()
    run(argv, cwd=parent, env=ctx.cmd_env(), timeout=600, capture=True)
    ms = (time.perf_counter() - t) * 1000
    app = parent / name
    return (app, ms) if timed else app


def _hide_env(app):
    """Renames the .env files of a generated app (temp dir only), so a generator finds no
    database URL and skips its migrate post-step."""
    hidden = []
    for e in (app / ".env", app / "api" / ".env"):
        if e.exists():
            h = e.with_name(e.name + ".perf")
            e.rename(h)
            hidden.append((e, h))
    return hidden


def _restore_env(hidden):
    for e, h in hidden:
        h.rename(e)


def gen_resources(ctx, app, resources=None):
    """Runs `spinup g resource <line> --api-only` for each resource line in the app.

    Returns the time of each call in ms. The .env files are hidden during generation, and
    DATABASE_URL is unset, so no call runs a database post-step.
    """
    resources = ctx.resources if resources is None else resources
    app = Path(app)
    env = ctx.cmd_env()
    hidden = _hide_env(app)
    times = []
    try:
        for line in resources:
            argv = [ctx.cli, "g", "resource", *shlex.split(line), "--api-only", "--non-interactive"]
            t = time.perf_counter()
            run(argv, cwd=app, env=env, timeout=600, capture=True)
            times.append((time.perf_counter() - t) * 1000)
    finally:
        _restore_env(hidden)
    return times


def gen_app(ctx, parent, resources=None, name="app", git=False, modules=CLI_MODULES, api_only=True):
    """The scenario app: `new` with the modules, then each resource (g resource).

    Returns {"app": Path, "new_ms": float, "resource_ms": [float, ...]}.
    """
    app, new_ms = new_app(ctx, parent, name=name, git=git, modules=modules, api_only=api_only, timed=True)
    times = gen_resources(ctx, app, resources)
    return {"app": app, "new_ms": new_ms, "resource_ms": times}


def go_env_value(ctx, key):
    """`go env <key>` with the run env."""
    return _sh(["go", "env", key], env=ctx.env)


# ---------------------------------------------------------------------------
# Result file
# ---------------------------------------------------------------------------


def write_results(ctx):
    """Writes the JSON result file atomically (partial results survive a crash)."""
    data = {"meta": ctx.meta, "notes": ctx.notes, "suites": ctx.suite_meta,
            "gate_events": ctx.gate_events, "records": [*ctx.records, *ctx.live]}
    ctx.out.parent.mkdir(parents=True, exist_ok=True)
    tmp = ctx.out.with_name(ctx.out.name + ".tmp")
    tmp.write_text(json.dumps(data, indent=2) + "\n")
    os.replace(tmp, ctx.out)


def read_results(path):
    """Reads a result JSON. Raises Failure with a Try: hint when it is not one."""
    try:
        data = json.loads(Path(path).read_text())
    except OSError as e:
        raise Failure(f"cannot read {path}: {e.strerror}", "pass a result file from `perf.py run`") from e
    except ValueError as e:
        raise Failure(f"{path} is not JSON: {e}", "pass a result file from `perf.py run`") from e
    if not isinstance(data, dict) or not isinstance(data.get("records"), list):
        raise Failure(f"{path} has no records list, so it is not a perf result",
                      "pass a result file from `perf.py run`")
    return data
