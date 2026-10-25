#!/usr/bin/env python3
"""R0.11 dev loop measurements.

Usage:
  measure.py prepare <variant>               # build todo-api and the supervisor for a variant
  measure.py series <name> [--edits N]       # one series of SERIES (see below)
  measure.py client [--runs N]               # client regeneration (openapi-typescript)
  measure.py migration [--runs N]            # migration edit: migrate, prepare, build, restart
  measure.py cold <variant>                  # cold build plus the first response
  measure.py go <app-dir> [--edits N]        # the Go baseline with air

Every series appends one JSON record to measurements/results.jsonl. Each
record has the 1-minute load average before and after (`uptime`).

A sample is the time from the file write to the first HTTP response whose
body shows the edit. The client polls the edited endpoint every 5 ms with a
new connection. Before each series the script waits while the 1-minute load
average is 4 or more (the R0.11 brief), at most 20 minutes.

Environment: DATABASE_URL (a migrated database, the R0.11 default is
spinup_r11 on 127.0.0.1:55433), CARGO_TARGET_DIR (each variant builds in its
own subdirectory), and the offline cargo settings of the lane.
"""

import argparse
import http.client
import json
import os
import re
import shutil
import signal
import statistics
import subprocess
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
OUT = HERE / "measurements"
TODOS = HERE / "todo-api" / "src" / "handlers" / "todos.rs"
SPEC = HERE / "todo-api" / "openapi.gen.yaml"
PORT = 18311
LOAD_WAIT = 4.0
SYSROOT = subprocess.run(
    ["rustc", "--print", "sysroot"], capture_output=True, text=True, check=True
).stdout.strip()
LLD = f"{SYSROOT}/lib/rustlib/aarch64-apple-darwin/bin/gcc-ld/ld64.lld"
BASE_TARGET = Path(os.environ["CARGO_TARGET_DIR"])
DB = os.environ.get(
    "R11_DATABASE_URL",
    "postgres://postgres:postgres@127.0.0.1:55433/spinup_r11?sslmode=disable",
)

# Build variants: cargo profile settings through the environment or
# --config, each in its own target directory.
VARIANTS = {
    "default": {"env": {}, "config": []},
    "unpacked": {"env": {"CARGO_PROFILE_DEV_SPLIT_DEBUGINFO": "unpacked"}, "config": []},
    "line-tables": {"env": {"CARGO_PROFILE_DEV_DEBUG": "line-tables-only"}, "config": []},
    "nodebug": {"env": {"CARGO_PROFILE_DEV_DEBUG": "0"}, "config": []},
    "lld": {"env": {"RUSTFLAGS": f"-C link-arg=-fuse-ld={LLD}"}, "config": []},
    "deps-opt1": {"env": {}, "config": ['profile.dev.package."*".opt-level=1']},
    "lt-lld": {
        "env": {
            "CARGO_PROFILE_DEV_DEBUG": "line-tables-only",
            "RUSTFLAGS": f"-C link-arg=-fuse-ld={LLD}",
        },
        "config": [],
    },
    # Smaller binaries: macOS scans each new executable on its first start,
    # about 17 ms per MB on this machine.
    "lean": {
        "env": {
            "CARGO_PROFILE_DEV_DEBUG": "line-tables-only",
            "RUSTFLAGS": f"-C link-arg=-fuse-ld={LLD}",
        },
        "config": ['profile.dev.package."*".opt-level=1'],
    },
    "lean-strip": {
        "env": {
            "CARGO_PROFILE_DEV_DEBUG": "0",
            "CARGO_PROFILE_DEV_STRIP": "symbols",
            "RUSTFLAGS": f"-C link-arg=-fuse-ld={LLD}",
        },
        "config": ['profile.dev.package."*".opt-level=1'],
    },
    # The same without rust-lld (rust-lld made the warm build slower).
    "ltd": {
        "env": {"CARGO_PROFILE_DEV_DEBUG": "line-tables-only"},
        "config": ['profile.dev.package."*".opt-level=1'],
    },
    "ltd-stripdbg": {
        "env": {"CARGO_PROFILE_DEV_DEBUG": "0", "CARGO_PROFILE_DEV_STRIP": "debuginfo"},
        "config": ['profile.dev.package."*".opt-level=1'],
    },
    "ltd-strip": {
        "env": {"CARGO_PROFILE_DEV_DEBUG": "0", "CARGO_PROFILE_DEV_STRIP": "symbols"},
        "config": ['profile.dev.package."*".opt-level=1'],
    },
    "nodebug-lld": {
        "env": {"CARGO_PROFILE_DEV_DEBUG": "0", "RUSTFLAGS": f"-C link-arg=-fuse-ld={LLD}"},
        "config": [],
    },
}

# Series: (variant, driver, edit kind, app environment).
SERIES = {
    "own-default": ("default", "own", "body", {}),
    "own-unpacked": ("unpacked", "own", "body", {}),
    "own-line-tables": ("line-tables", "own", "body", {}),
    "own-nodebug": ("nodebug", "own", "body", {}),
    "own-lld": ("lld", "own", "body", {}),
    "own-deps-opt1": ("deps-opt1", "own", "body", {}),
    "own-default-defer": ("default", "own", "body", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "listenfd-default": ("default", "listenfd", "body", {}),
    "bacon-default": ("default", "bacon", "body", {}),
    "watchexec-default": ("default", "watchexec", "body", {}),
    "own-lt-lld": ("lt-lld", "own", "body", {}),
    "own-lt-lld-defer": ("lt-lld", "own", "body", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "listenfd-lt-lld-defer": ("lt-lld", "listenfd", "body", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "own-nodebug-lld-defer": ("nodebug-lld", "own", "body", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "own-lean-defer": ("lean", "own", "body", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "listenfd-lean-defer": ("lean", "listenfd", "body", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "own-lean-strip-defer": ("lean-strip", "own", "body", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "listenfd-lean-strip-defer": ("lean-strip", "listenfd", "body", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "listenfd-ltd-defer": ("ltd", "listenfd", "body", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "listenfd-ltd-stripdbg-defer": ("ltd-stripdbg", "listenfd", "body", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "listenfd-ltd-strip-defer": ("ltd-strip", "listenfd", "body", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "own-ltd-strip": ("ltd-strip", "own", "body", {}),
    # A second series of the best configuration (the same settings).
    "best-repeat": ("ltd-strip", "listenfd", "body", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "best-signature": ("ltd-strip", "listenfd", "signature", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "best-utoipa": ("ltd-strip", "listenfd", "utoipa", {"AUTH_DEFER_DUMMY_HASH": "1"}),
    "default-signature": ("default", "own", "signature", {}),
    "default-utoipa": ("default", "own", "utoipa", {}),
}


def load1():
    return os.getloadavg()[0]


def uptime():
    return subprocess.run(["uptime"], capture_output=True, text=True).stdout.strip()


def wait_idle():
    start = time.time()
    while load1() >= LOAD_WAIT and time.time() - start < 1200:
        print(f"load {load1():.2f} >= {LOAD_WAIT}, waiting", flush=True)
        time.sleep(15)
    return time.time() - start


def target(variant):
    return BASE_TARGET / variant


def cargo_env(variant):
    env = dict(os.environ)
    env.update(VARIANTS[variant]["env"])
    env["CARGO_TARGET_DIR"] = str(target(variant))
    env["DATABASE_URL"] = DB
    return env


def build_cmd(variant, package="todo-api"):
    cmd = ["cargo", "build", "--offline", "--locked", "-p", package]
    for c in VARIANTS[variant]["config"]:
        cmd += ["--config", c]
    return cmd


def app_env(variant, extra):
    env = cargo_env(variant)
    env.update({"SPINUP_ENV": "dev", "PORT": str(PORT), "RUST_BACKTRACE": "0"})
    env.update(extra)
    return env


def stats(xs):
    xs = sorted(xs)
    if not xs:
        return {}
    # p90 by the nearest-rank method: the value at rank ceil(0.9 n).
    rank = max(1, -(-9 * len(xs) // 10))
    return {
        "n": len(xs),
        "median_ms": round(statistics.median(xs), 1),
        "p90_ms": round(xs[rank - 1], 1),
        "min_ms": round(xs[0], 1),
        "max_ms": round(xs[-1], 1),
    }


def record(rec):
    OUT.mkdir(exist_ok=True)
    with open(OUT / "results.jsonl", "a") as f:
        f.write(json.dumps(rec) + "\n")
    print(json.dumps(rec, indent=1), flush=True)


def get(path, timeout=30.0):
    """GET path on the app. Returns (status, body) or None."""
    try:
        c = http.client.HTTPConnection("127.0.0.1", PORT, timeout=timeout)
        c.request("GET", path, headers={"Connection": "close"})
        r = c.getresponse()
        body = r.read().decode("utf-8", "replace")
        c.close()
        return r.status, body
    except (OSError, http.client.HTTPException):
        return None


def wait_for(pred, timeout=600.0, interval=0.005):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if pred():
            return True
        time.sleep(interval)
    return False


def body_has(path, text):
    def pred():
        r = get(path)
        return r is not None and r[0] == 200 and text in r[1]

    return pred


def free_port():
    subprocess.run(["pkill", "-f", "todo-api serve"], capture_output=True)
    wait_for(lambda: get("/healthz", timeout=0.2) is None, timeout=10, interval=0.05)


# The edit kinds. Each returns (new source, marker that the check looks for,
# check function).
GREETING = "/api/todos/greeting"
SIG_A = "async fn todo_greeting(State(db): State<PgPool>) -> ApiResult<Json<Greeting>> {"
SIG_B = (
    "async fn todo_greeting(\n    State(db): State<PgPool>,\n    _headers: axum::http::HeaderMap,\n)"
    " -> ApiResult<Json<Greeting>> {"
)
SUMMARY = re.compile(r'summary = "Greet the todo list[^"]*"')


def edit(kind, src, i, tag):
    mark = f"mark-{i:04d}"
    src = re.sub(r"mark-\d{4}", mark, src)
    if kind == "body":
        return src, body_has(GREETING, mark)
    if kind == "signature":
        src = src.replace(SIG_B, SIG_A) if SIG_B in src else src.replace(SIG_A, SIG_B)
        return src, body_has(GREETING, mark)
    if kind == "utoipa":
        summary = f"Greet the todo list {tag}{i:04d}"
        src = SUMMARY.sub(f'summary = "{summary}"', src)

        def spec_has():
            try:
                return summary in SPEC.read_text()
            except OSError:
                return False

        return src, lambda: spec_has() and body_has(GREETING, mark)()
    raise ValueError(kind)


def start_driver(variant, driver, kind, extra, log):
    env = app_env(variant, extra)
    t = target(variant)
    bin_ = str(t / "debug" / "todo-api")
    sup = str(target("default") / "debug" / "supervisor")
    events = OUT / "events.jsonl"
    if driver in ("own", "listenfd"):
        cmd = [
            sup,
            "--watch", "todo-api/src",
            "--watch", "kit/src",
            "--build", " ".join(f"'{a}'" for a in build_cmd(variant)),
            "--port", str(PORT),
            "--events", str(events),
        ]
        if kind == "utoipa":
            cmd += [
                "--post-build",
                f"{bin_} openapi > todo-api/openapi.gen.yaml.tmp && mv todo-api/openapi.gen.yaml.tmp todo-api/openapi.gen.yaml",
            ]
        if driver == "listenfd":
            cmd.append("--listenfd")
        cmd += ["--", bin_, "serve"]
    elif driver == "bacon":
        cmd = ["bacon", "--headless", "--offline", "-j", "serve"]
    elif driver == "watchexec":
        cmd = [
            "watchexec", "--restart", "--debounce", "10ms",
            "--watch", "todo-api/src", "--watch", "kit/src", "--exts", "rs,sql",
            "--stop-signal", "SIGTERM", "--stop-timeout", "2s",
            "--", "cargo", "run", "--offline", "--locked", "-p", "todo-api", "--", "serve",
        ]
    else:
        raise ValueError(driver)
    p = subprocess.Popen(
        cmd, cwd=HERE, env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=True
    )
    return p, events


def stop_driver(p):
    try:
        os.killpg(p.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        p.wait(timeout=10)
    except subprocess.TimeoutExpired:
        os.killpg(p.pid, signal.SIGKILL)
        p.wait()
    free_port()


def read_events(path, since_ns):
    out = []
    try:
        for line in open(path):
            e = json.loads(line)
            if e["t"] >= since_ns:
                out.append(e)
    except OSError:
        pass
    return out


def breakdown(evs, t0_ns, t1_ns):
    """Splits one sample of the own supervisor into its steps (ms)."""
    first = {}
    for e in evs:
        first.setdefault(e["event"], e)
    ms = lambda a, b: round((b - a) / 1e6, 1)  # noqa: E731
    try:
        d = {
            "detect": ms(t0_ns, first["change"]["fs_t"]),
            "debounce": ms(first["change"]["t"], first["debounced"]["t"]),
            "build": ms(first["build_start"]["t"], first["build_end"]["t"]),
        }
        after_build = first.get("post_build_end", first["build_end"])["t"]
        if "post_build_end" in first:
            d["post_build"] = ms(first["build_end"]["t"], after_build)
        d["stop"] = ms(after_build, first["stopped"]["t"])
        d["spawn_to_ready"] = ms(first["spawned"]["t"], first["ready"]["t"])
        d["ready_to_response"] = ms(first["ready"]["t"], t1_ns)
        d["builds"] = sum(1 for e in evs if e["event"] == "build_start")
        return d
    except KeyError:
        return None


def series(name, edits, warmup=1):
    variant, driver, kind, extra = SERIES[name]
    OUT.mkdir(exist_ok=True)
    original = TODOS.read_text()
    spec_orig = SPEC.read_text() if SPEC.exists() else None
    waited = wait_idle()
    before = uptime()
    load_before = load1()
    log = open(OUT / f"log-{name}.txt", "w")
    free_port()
    p, events = start_driver(variant, driver, kind, extra, log)
    samples, details = [], []
    try:
        t_start = time.monotonic()
        if not wait_for(lambda: (r := get(GREETING, timeout=2)) is not None and r[0] == 200, 600, 0.02):
            raise SystemExit(f"{name}: the app did not start; see {log.name}")
        first_ready = time.monotonic() - t_start
        time.sleep(1.5)
        src = TODOS.read_text()
        tag = "".join(c for c in name if c.isalnum())[:6]
        for i in range(1, warmup + edits + 1):
            src, check = edit(kind, src, i, tag)
            t0_ns = time.time_ns()
            t0 = time.monotonic()
            TODOS.write_text(src)
            if not wait_for(check, timeout=300):
                raise SystemExit(f"{name}: edit {i} not served after 300 s; see {log.name}")
            t1 = time.monotonic()
            t1_ns = time.time_ns()
            ms = (t1 - t0) * 1000
            # Let the driver settle (a second event, a late rebuild).
            time.sleep(1.5)
            d = breakdown(read_events(events, t0_ns), t0_ns, t1_ns) if driver in ("own", "listenfd") else None
            if i > warmup:
                samples.append(ms)
                details.append({"ms": round(ms, 1), "load1": round(load1(), 2), "steps": d})
            print(f"{name} edit {i}: {ms:.0f} ms {d or ''}", flush=True)
    finally:
        stop_driver(p)
        log.close()
        TODOS.write_text(original)
        if spec_orig is not None:
            SPEC.write_text(spec_orig)
    rec = {
        "series": name,
        "variant": variant,
        "driver": driver,
        "kind": kind,
        "app_env": extra,
        "variant_env": VARIANTS[variant],
        "first_start_s": round(first_ready, 2),
        "waited_for_idle_s": round(waited, 1),
        "uptime_before": before,
        "uptime_after": uptime(),
        "load1_before": round(load_before, 2),
        "load1_after": round(load1(), 2),
        "stats": stats(samples),
        "samples": details,
    }
    record(rec)
    # A rebuild with the original source, so the next series starts warm.
    subprocess.run(build_cmd(variant), cwd=HERE, env=cargo_env(variant), capture_output=True)
    return rec


def prepare(variant):
    t = time.monotonic()
    before = uptime()
    r = subprocess.run(build_cmd(variant), cwd=HERE, env=cargo_env(variant))
    secs = time.monotonic() - t
    if r.returncode != 0:
        raise SystemExit(f"prepare {variant}: build failed")
    if variant == "default":
        subprocess.run(build_cmd("default", "supervisor"), cwd=HERE, env=cargo_env("default"), check=True)
    record({
        "series": f"prepare-{variant}",
        "build_s": round(secs, 1),
        "uptime_before": before,
        "uptime_after": uptime(),
        "binary_bytes": (target(variant) / "debug" / "todo-api").stat().st_size,
    })


def cold(variant):
    """A cold build in an empty target directory, then the first response."""
    waited = wait_idle()
    before = uptime()
    t = BASE_TARGET / f"cold-{variant}"
    shutil.rmtree(t, ignore_errors=True)
    env = cargo_env(variant)
    env["CARGO_TARGET_DIR"] = str(t)
    t0 = time.monotonic()
    subprocess.run(build_cmd(variant), cwd=HERE, env=env, check=True, capture_output=True)
    built = time.monotonic() - t0
    free_port()
    aenv = dict(env)
    aenv.update({"SPINUP_ENV": "dev", "PORT": str(PORT)})
    t1 = time.monotonic()
    p = subprocess.Popen([str(t / "debug" / "todo-api"), "serve"], env=aenv,
                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    wait_for(body_has(GREETING, "open todos"), 60, 0.002)
    started = time.monotonic() - t1
    total = time.monotonic() - t0
    p.terminate()
    p.wait()
    record({
        "series": f"cold-{variant}",
        "build_s": round(built, 1),
        "start_to_first_response_ms": round(started * 1000, 1),
        "total_s": round(total, 1),
        "waited_for_idle_s": round(waited, 1),
        "uptime_before": before,
        "uptime_after": uptime(),
        "target_dir_bytes": int(subprocess.run(["du", "-sk", str(t)], capture_output=True, text=True).stdout.split()[0]) * 1024,
    })
    shutil.rmtree(t, ignore_errors=True)


def client(runs):
    """openapi-typescript on the emitted 20-resource spec, as the client post-step."""
    waited = wait_idle()
    before = uptime()
    bin_ = target("default") / "debug" / "todo-api"
    tool = os.environ.get(
        "R11_OPENAPI_TYPESCRIPT", "/Users/king/Documents/code/todo/web/node_modules/.bin/openapi-typescript"
    )
    work = Path(os.environ.get("R11_SCRATCH", "/tmp")) / "r11-client"
    work.mkdir(parents=True, exist_ok=True)
    spec = work / "openapi.gen.yaml"
    emit = []
    gen = []
    for _ in range(runs):
        t = time.monotonic()
        out = subprocess.run([str(bin_), "openapi"], capture_output=True, text=True, check=True).stdout
        spec.write_text(out)
        emit.append((time.monotonic() - t) * 1000)
        t = time.monotonic()
        subprocess.run([tool, str(spec), "-o", str(work / "schema.d.ts")], check=True, capture_output=True)
        gen.append((time.monotonic() - t) * 1000)
    record({
        "series": "client",
        "spec_bytes": spec.stat().st_size,
        "schema_bytes": (work / "schema.d.ts").stat().st_size,
        "spec_emit": stats(emit),
        "openapi_typescript": stats(gen),
        "waited_for_idle_s": round(waited, 1),
        "uptime_before": before,
        "uptime_after": uptime(),
    })


def migration(runs, variant="default"):
    """A migration edit: write a new migration, then the R0.10 post-step
    order (sqlx migrate run, cargo sqlx prepare, cargo build), restart, and
    the first response."""
    waited = wait_idle()
    before = uptime()
    env = cargo_env(variant)
    mig = HERE / "todo-api" / "migrations"
    bin_ = target(variant) / "debug" / "todo-api"
    aenv = app_env(variant, {})
    free_port()
    app = subprocess.Popen([str(bin_), "serve"], env=aenv, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    wait_for(body_has(GREETING, "open todos"), 60, 0.01)
    rows = []
    try:
        for i in range(1, runs + 1):
            num = 100 + i
            up = mig / f"{num:05d}_r11_tweak_{i}.up.sql"
            down = mig / f"{num:05d}_r11_tweak_{i}.down.sql"
            t0 = time.monotonic()
            up.write_text(f"ALTER TABLE todos ADD COLUMN r11_tweak_{i} integer NOT NULL DEFAULT 0;\n")
            down.write_text(f"ALTER TABLE todos DROP COLUMN r11_tweak_{i};\n")
            steps = {}
            for label, cmd in [
                ("migrate", ["sqlx", "migrate", "run", "--source", "todo-api/migrations"]),
                ("prepare", ["cargo", "sqlx", "prepare", "--workspace"]),
                ("build", build_cmd(variant)),
            ]:
                t = time.monotonic()
                subprocess.run(cmd, cwd=HERE, env=env, check=True, capture_output=True)
                steps[label] = round((time.monotonic() - t) * 1000, 1)
            t = time.monotonic()
            app.terminate()
            app.wait()
            app = subprocess.Popen([str(bin_), "serve"], env=aenv, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            wait_for(body_has(GREETING, "open todos"), 60, 0.005)
            steps["restart_to_response"] = round((time.monotonic() - t) * 1000, 1)
            total = (time.monotonic() - t0) * 1000
            rows.append({"ms": round(total, 1), "steps": steps, "load1": round(load1(), 2)})
            print(f"migration {i}: {total:.0f} ms {steps}", flush=True)
    finally:
        app.terminate()
        app.wait()
        # Revert the tweaks and restore .sqlx and the build.
        for i in range(runs, 0, -1):
            num = 100 + i
            if (mig / f"{num:05d}_r11_tweak_{i}.up.sql").exists():
                subprocess.run(["sqlx", "migrate", "revert", "--source", "todo-api/migrations"], cwd=HERE, env=env, capture_output=True)
        for f in mig.glob("*_r11_tweak_*.sql"):
            f.unlink()
        subprocess.run(["cargo", "sqlx", "prepare", "--workspace"], cwd=HERE, env=env, capture_output=True)
        subprocess.run(build_cmd(variant), cwd=HERE, env=env, capture_output=True)
    record({
        "series": f"migration-{variant}",
        "stats": stats([r["ms"] for r in rows]),
        "samples": rows,
        "waited_for_idle_s": round(waited, 1),
        "uptime_before": before,
        "uptime_after": uptime(),
    })


def go(app_dir, edits, warmup=1):
    """The Go baseline: `go tool -modfile=tools.mod air` (the spinup dev
    command) in a copy of a Go app, with a handler-body edit in
    internal/handlers/greeting.go."""
    app_dir = Path(app_dir)
    f = app_dir / "internal" / "handlers" / "greeting.go"
    original = f.read_text()
    waited = wait_idle()
    before = uptime()
    load_before = load1()
    env = dict(os.environ)
    env.update({"DATABASE_URL": os.environ["R11_GO_DATABASE_URL"], "SPINUP_ENV": "dev", "PORT": str(PORT)})
    env["GOFLAGS"] = "-mod=readonly"
    free_port()
    log = open(OUT / "log-go-air.txt", "w")
    p = subprocess.Popen(["go", "tool", "-modfile=tools.mod", "air"], cwd=app_dir, env=env,
                         stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
    samples = []
    try:
        if not wait_for(lambda: (r := get("/api/greeting", timeout=2)) is not None and r[0] == 200, 600, 0.02):
            raise SystemExit("go: the app did not start; see log-go-air.txt")
        time.sleep(1.5)
        src = f.read_text()
        for i in range(1, warmup + edits + 1):
            mark = f"mark-{i:04d}"
            src = re.sub(r"mark-\d{4}", mark, src)
            t0 = time.monotonic()
            f.write_text(src)
            if not wait_for(body_has("/api/greeting", mark), timeout=300):
                raise SystemExit(f"go: edit {i} not served")
            ms = (time.monotonic() - t0) * 1000
            time.sleep(1.5)
            if i > warmup:
                samples.append(ms)
            print(f"go edit {i}: {ms:.0f} ms", flush=True)
    finally:
        os.killpg(p.pid, signal.SIGTERM)
        try:
            p.wait(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(p.pid, signal.SIGKILL)
        log.close()
        f.write_text(original)
        free_port()
    record({
        "series": "go-air",
        "app": str(app_dir),
        "stats": stats(samples),
        "samples": [round(s, 1) for s in samples],
        "waited_for_idle_s": round(waited, 1),
        "uptime_before": before,
        "uptime_after": uptime(),
        "load1_before": round(load_before, 2),
        "load1_after": round(load1(), 2),
    })


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("prepare")
    s.add_argument("variant", choices=VARIANTS)
    s = sub.add_parser("series")
    s.add_argument("name", nargs="+", choices=SERIES)
    s.add_argument("--edits", type=int, default=10)
    s = sub.add_parser("client")
    s.add_argument("--runs", type=int, default=5)
    s = sub.add_parser("migration")
    s.add_argument("--runs", type=int, default=3)
    s.add_argument("--variant", default="default", choices=VARIANTS)
    s = sub.add_parser("cold")
    s.add_argument("variant", choices=VARIANTS)
    s = sub.add_parser("go")
    s.add_argument("app")
    s.add_argument("--edits", type=int, default=10)
    a = ap.parse_args()
    os.chdir(HERE)
    if a.cmd == "prepare":
        prepare(a.variant)
    elif a.cmd == "series":
        for n in a.name:
            series(n, a.edits)
    elif a.cmd == "client":
        client(a.runs)
    elif a.cmd == "migration":
        migration(a.runs, a.variant)
    elif a.cmd == "cold":
        cold(a.variant)
    elif a.cmd == "go":
        go(a.app, a.edits)


if __name__ == "__main__":
    sys.exit(main())
