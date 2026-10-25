#!/usr/bin/env python3
"""Spike r0-13: the lane and gate load test.

Run it from the lane environment (lane-env.sh sets CARGO_HOME, the offline
flags, and CARGO_TARGET_DIR). Everything this script builds lives under
$CARGO_TARGET_DIR/lanes. Results go to spikes-rs/r0-13-lanes/measurements/.

Commands:
  measure.py run <label> --profile plain|sccache|clone [--fresh-cache]
        Two lanes and the gate at the same time. Lane A checks spinup-kit
        and spinup-gen-app, lane B checks spinup-deploy and spinup-cli. Each
        lane has its own worktree copy and CARGO_TARGET_DIR, runs the cold
        lane check (fmt, clippy, nextest, doc tests), then warm edit cycles
        until the gate ends. The gate builds 8 apps (--all-targets) under one
        shared CARGO_TARGET_DIR, one musl release build (cargo zigbuild), one
        Docker image, and the rs-ref-check workload, then the same steps
        again after a kit edit. A sampler records du, the 1-minute load
        average, and swap use every 5 s.
  measure.py run ... [--lines N] [--lane-jobs J] [--gate-jobs J] [--test-threads T]
        [--slots] [--basedirs]
        --lines sets the size of the generated CLI workspace. The jobs flags
        set CARGO_BUILD_JOBS per lane and for the gate, and
        NEXTEST_TEST_THREADS. --slots puts each target dir at a fixed path
        that every run reuses. --basedirs sets SCCACHE_BASEDIRS.
  measure.py seed [--lines N]   a warm target dir of the whole CLI workspace (for clone)
  measure.py single <label> --lines N   lane B alone (cold and 2 warm cycles),
                            then clippy and nextest --no-run for the whole workspace
  measure.py pathkey        the sccache key across worktree paths
  measure.py appprofile     one app cold: shipped dev profile against the default
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import threading
import time
from pathlib import Path

SPIKE = Path(__file__).resolve().parent.parent
REPO = SPIKE.parent.parent
GEN = SPIKE / "tools" / "gen_lanes.py"
OUT = SPIKE / "measurements"
BASE = Path(os.environ["CARGO_TARGET_DIR"]) / "lanes"
CARGO_HOME = Path(os.environ["CARGO_HOME"])
PACKS = REPO / "internal" / "templates" / "packs"
SCCACHE_DIR = BASE / "sccache"
LINES = 22000  # --lines for gen_lanes.py: about 27k CLI lines in total

LANES = {
    "lane-a": {"crates": ["spinup-kit", "spinup-gen-app"], "edit": ["spinup-kit/src/lib.rs", "spinup-gen-app/src/m00.rs"]},
    "lane-b": {"crates": ["spinup-deploy", "spinup-cli"], "edit": ["spinup-deploy/src/m00.rs", "spinup-cli/src/m00.rs"]},
}
APPS = ["api", "auth", "jobs", "mail", "res2", "res5", "res10", "corpus"]
NICE = ["nice", "-n", "10"]


def uptime():
    return subprocess.run(["uptime"], capture_output=True, text=True).stdout.strip()


def load1():
    return round(os.getloadavg()[0], 2)


def swap_used_mb():
    out = subprocess.run(["sysctl", "-n", "vm.swapusage"], capture_output=True, text=True).stdout
    m = re.search(r"used = ([0-9.]+)M", out)
    return float(m.group(1)) if m else -1.0


def disk_free_gb():
    st = os.statvfs(str(BASE))
    return st.f_bavail * st.f_frsize / 1e9


def du_kb(paths):
    paths = [str(p) for p in paths if Path(p).exists()]
    if not paths:
        return {}
    out = subprocess.run(["du", "-sk", *paths], capture_output=True, text=True).stdout
    res = {}
    for ln in out.splitlines():
        kb, p = ln.split("\t", 1)
        res[p] = int(kb)
    return res


def wait_for_load(limit=4.0, max_wait=1800):
    """Waits while the 1-minute load average is `limit` or more."""
    t0 = time.time()
    while load1() >= limit and time.time() - t0 < max_wait:
        time.sleep(15)
    return {"waited_s": round(time.time() - t0), "load1": load1(), "uptime": uptime()}


class Sampler(threading.Thread):
    def __init__(self, dirs, path, interval=5.0):
        super().__init__(daemon=True)
        self.dirs, self.path, self.interval = dirs, path, interval
        self.stop_ev = threading.Event()
        self.samples = []

    def run(self):
        t0 = time.time()
        with open(self.path, "w") as f:
            while not self.stop_ev.is_set():
                ts = time.time()
                sizes = du_kb(self.dirs)
                s = {
                    "t": round(ts - t0, 1),
                    "load1": load1(),
                    "swap_mb": swap_used_mb(),
                    "free_gb": round(disk_free_gb(), 2),
                    "du_kb": sizes,
                    "total_gb": round(sum(sizes.values()) / 1e6, 3),
                    "du_s": round(time.time() - ts, 2),
                }
                self.samples.append(s)
                f.write(json.dumps(s) + "\n")
                f.flush()
                self.stop_ev.wait(max(0.0, self.interval - (time.time() - ts)))


def run_step(log, name, cmd, cwd, env, logdir):
    lf = logdir / f"{name}.log"
    rec = {"step": name, "cmd": " ".join(cmd), "load1_start": load1(), "start": time.time()}
    with open(lf, "w") as f:
        p = subprocess.run(NICE + cmd, cwd=cwd, env=env, stdout=f, stderr=subprocess.STDOUT)
    rec["secs"] = round(time.time() - rec["start"], 1)
    rec["rc"] = p.returncode
    rec["load1_end"] = load1()
    log.append(rec)
    print(f"  {name}: {rec['secs']} s rc={p.returncode} load {rec['load1_start']}->{rec['load1_end']}", flush=True)
    return rec


def base_env(target, wrapper):
    env = dict(os.environ)
    env["CARGO_TARGET_DIR"] = str(target)
    env["SPINUP_PACKS_DIR"] = str(PACKS)
    env["SQLX_OFFLINE"] = "true"
    env.pop("RUSTC_WRAPPER", None)
    if wrapper:
        env["RUSTC_WRAPPER"] = "sccache"
        env["SCCACHE_DIR"] = str(SCCACHE_DIR)
        if BASEDIRS["on"]:
            env["SCCACHE_BASEDIRS"] = str(BASE)
    return env


# --basedirs: SCCACHE_BASEDIRS is the common parent of every worktree and
# target dir, so the key does not depend on those paths (see pathkey).
BASEDIRS = {"on": False}


def set_mark(path, n):
    txt = path.read_text()
    txt2 = re.sub(r"EDIT_MARK: u32 = \d+;", f"EDIT_MARK: u32 = {n};", txt)
    assert txt2 != txt or f"= {n};" in txt, path
    path.write_text(txt2)


# A lane starts a warm cycle at most every PACE_S seconds (a unit edits,
# then runs the lane check), until the gate ends.
PACE_S = 30
JOBS = {"lane": None, "gate": None, "test_threads": None}


def lane_job(name, ws, target, wrapper, logdir, gate_done, result):
    cfg = LANES[name]
    env = base_env(target, wrapper)
    if JOBS["lane"]:
        env["CARGO_BUILD_JOBS"] = str(JOBS["lane"])
    if JOBS["test_threads"]:
        env["NEXTEST_TEST_THREADS"] = str(JOBS["test_threads"])
    p = [x for c in cfg["crates"] for x in ("-p", c)]
    cmds = [
        ("fmt", ["cargo", "fmt", "--check", *p]),
        ("clippy", ["cargo", "clippy", "--locked", "--offline", *p, "--all-targets", "--", "-D", "warnings"]),
        ("nextest", ["cargo", "nextest", "run", "--locked", "--offline", *p]),
        ("doctest", ["cargo", "test", "--doc", "--locked", "--offline", *p]),
    ]
    log = []
    t0 = time.time()
    for s, c in cmds:
        run_step(log, f"{name}-cold-{s}", c, ws, env, logdir)
    result["cold_s"] = round(time.time() - t0, 1)
    warm = []
    n = 0
    while n < 2 or not gate_done.is_set():
        n += 1
        if n > 1:
            # Pace the cycles; stop waiting when the gate ends.
            while time.time() - t1 < PACE_S and (n <= 2 or not gate_done.is_set()):
                time.sleep(0.5)
            if n > 2 and gate_done.is_set():
                break
        set_mark(ws / cfg["edit"][n % 2], n)
        t1 = time.time()
        for s, c in cmds:
            run_step(log, f"{name}-warm{n}-{s}", c, ws, env, logdir)
        warm.append({"cycle": n, "edit": cfg["edit"][n % 2], "secs": round(time.time() - t1, 1)})
    result["warm"] = warm
    lst = subprocess.run(
        ["cargo", "nextest", "list", "--locked", "--offline", *p, "--list-type", "binaries-only", "--message-format", "json"],
        cwd=ws, env=env, capture_output=True, text=True,
    )
    try:
        result["test_binaries"] = len(json.loads(lst.stdout)["rust-binaries"])
    except (ValueError, KeyError):
        result["test_binaries"] = None
    result["steps"] = log


def count_test_bins(logfile):
    bins = set()
    for ln in Path(logfile).read_text().splitlines():
        if not ln.startswith("{"):
            continue
        try:
            m = json.loads(ln)
        except ValueError:
            continue
        if m.get("reason") == "compiler-artifact" and m.get("executable") and m.get("profile", {}).get("test"):
            bins.add(m["executable"])
    return len(bins)


def gate_job(gate, target, wrapper, logdir, label, result, passes=2):
    env = base_env(target, wrapper)
    if JOBS["gate"]:
        env["CARGO_BUILD_JOBS"] = str(JOBS["gate"])
    log = []
    passes_out = []
    for ps in range(passes):
        if ps:
            set_mark(gate / "kit" / "src" / "lib.rs", ps)
        t0 = time.time()
        tbins = 0
        tag = "cold" if ps == 0 else f"warm{ps}"
        for a in APPS:
            r = run_step(
                log, f"gate-{tag}-build-{a}",
                ["cargo", "build", "--locked", "--offline", "--all-targets", "--message-format=json-render-diagnostics"],
                gate / "apps" / a, env, logdir,
            )
            tbins += count_test_bins(logdir / f"{r['step']}.log")
        run_step(
            log, f"gate-{tag}-musl",
            ["cargo", "zigbuild", "--release", "--locked", "--offline", "--target", "aarch64-unknown-linux-musl"],
            gate / "apps" / "api", env, logdir,
        )
        run_step(
            log, f"gate-{tag}-docker",
            ["docker", "build", "-f", str(gate / "docker" / "Dockerfile"),
             "--build-context", f"bin={target}/aarch64-unknown-linux-musl/release",
             "-t", f"r0-13/api-musl:{label}", str(gate / "docker")],
            gate, env, logdir,
        )
        run_step(log, f"gate-{tag}-refcheck", ["cargo", "check", "--locked", "--offline"], gate / "ref" / "todo-rs", env, logdir)
        passes_out.append({"pass": tag, "secs": round(time.time() - t0, 1), "test_binaries_built": tbins})
    result["passes"] = passes_out
    result["steps"] = log
    img = subprocess.run(["docker", "image", "inspect", "-f", "{{.Size}}", f"r0-13/api-musl:{label}"], capture_output=True, text=True)
    result["image_bytes"] = int(img.stdout.strip()) if img.returncode == 0 and img.stdout.strip() else None


def sccache(*args, env=None):
    e = dict(os.environ if env is None else env)
    e["SCCACHE_DIR"] = str(SCCACHE_DIR)
    e["SCCACHE_CACHE_SIZE"] = "100G"
    e["SCCACHE_IDLE_TIMEOUT"] = "0"
    if BASEDIRS["on"] and env is None:
        e["SCCACHE_BASEDIRS"] = str(BASE)
    return subprocess.run(["sccache", *args], env=e, capture_output=True, text=True)


def sccache_stats():
    r = sccache("--show-stats", "--stats-format", "json")
    try:
        return json.loads(r.stdout)
    except ValueError:
        return {"raw": r.stdout + r.stderr}


def gen(what, out, lines=None):
    lines = LINES if lines is None else lines
    args = [sys.executable, str(GEN), what, "--out", str(out)]
    if what == "cli":
        args += ["--lines", str(lines)]
    subprocess.run(args, check=True)


def clone(src, dst):
    t0 = time.time()
    subprocess.run(["cp", "-c", "-R", str(src), str(dst)], check=True)
    return round(time.time() - t0, 1)


def peaks(samples, swap_base):
    if not samples:
        return {}
    return {
        "peak_total_gb": max(s["total_gb"] for s in samples),
        "load1_max": max(s["load1"] for s in samples),
        "swap_abs_peak_mb": max(s["swap_mb"] for s in samples),
        "swap_baseline_mb": swap_base,
        "swap_increase_mb": round(max(s["swap_mb"] for s in samples) - swap_base, 1),
        "free_drop_gb": round(samples[0]["free_gb"] - min(s["free_gb"] for s in samples), 2),
        "samples": len(samples),
        "du_s_max": max(s["du_s"] for s in samples),
    }


SLOTS = {"on": False}


def tgt(root, name):
    """The target dir of a lane or the gate. With --slots it is a fixed path
    per slot that every run reuses (deleted before the run), so the sccache
    key, which holds the target path, stays the same across worktrees."""
    if SLOTS["on"]:
        return BASE / "slots" / name / "target"
    return root / name / "target"


def cmd_run(a):
    SLOTS["on"] = a.slots
    label = a.label
    root = BASE / f"run-{label}"
    if root.exists():
        shutil.rmtree(root)
    logdir = BASE / "logs" / label
    if logdir.exists():
        shutil.rmtree(logdir)
    logdir.mkdir(parents=True)
    wrapper = a.profile == "sccache"
    JOBS.update(lane=a.lane_jobs, gate=a.gate_jobs, test_threads=a.test_threads)
    BASEDIRS["on"] = a.basedirs
    res = {"label": label, "profile": a.profile, "lines_arg": LINES, "jobs": dict(JOBS), "pace_s": PACE_S, "basedirs": a.basedirs, "slots": a.slots,
           "cargo_build_jobs_env": os.environ.get("CARGO_BUILD_JOBS")}
    # Prepare the worktrees and the gate tree (not timed).
    for ln in LANES:
        gen("cli", root / ln / "ws")
    gen("gate", root / "gate")
    clone_s = {}
    if a.profile == "clone":
        for ln in LANES:
            clone_s[ln] = clone(BASE / "seed" / "target", root / ln / "target")
        clone_s["gate"] = clone(BASE / "gate-seed", root / "gate" / "target")
    res["clone_s"] = clone_s
    if wrapper:
        sccache("--stop-server")
        if a.fresh_cache and SCCACHE_DIR.exists():
            shutil.rmtree(SCCACHE_DIR)
        SCCACHE_DIR.mkdir(parents=True, exist_ok=True)
        r = sccache("--start-server")
        res["sccache_start"] = (r.stdout + r.stderr).strip()
        sccache("--zero-stats")
    dirs = [root / "lane-a", root / "lane-b", root / "gate", CARGO_HOME]
    if a.slots:
        for n in ("lane-a", "lane-b", "gate"):
            if tgt(root, n).exists():
                shutil.rmtree(tgt(root, n))
            tgt(root, n).parent.mkdir(parents=True, exist_ok=True)
            dirs.append(tgt(root, n))
    if wrapper:
        dirs.append(SCCACHE_DIR)
    if a.profile == "clone":
        dirs += [BASE / "seed", BASE / "gate-seed"]
    res["dirs"] = [str(d) for d in dirs]
    res["registry_home_kb"] = du_kb([Path.home() / ".cargo" / "registry"])
    res["wait"] = wait_for_load()
    res["uptime_start"] = uptime()
    res["swap_baseline_mb"] = swap_used_mb()
    res["free_gb_start"] = round(disk_free_gb(), 2)
    print(f"run {label}: {res['uptime_start']} swap {res['swap_baseline_mb']} MB", flush=True)
    sampler = Sampler(dirs, logdir / "samples.jsonl")
    sampler.start()
    gate_done = threading.Event()
    results = {ln: {} for ln in LANES}
    gres = {}
    t0 = time.time()
    threads = [
        threading.Thread(target=lane_job, args=(ln, root / ln / "ws", tgt(root, ln), wrapper, logdir, gate_done, results[ln]))
        for ln in LANES
    ]

    def gate_wrap():
        try:
            gate_job(root / "gate", tgt(root, "gate"), wrapper, logdir, label, gres)
        finally:
            gate_done.set()

    threads.append(threading.Thread(target=gate_wrap))
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    res["wall_s"] = round(time.time() - t0, 1)
    time.sleep(5)
    sampler.stop_ev.set()
    sampler.join()
    res["uptime_end"] = uptime()
    res["lanes"] = results
    res["gate"] = gres
    res["peaks"] = peaks(sampler.samples, res["swap_baseline_mb"])
    final = du_kb([tgt(root, "lane-a"), tgt(root, "lane-b"), tgt(root, "gate"), SCCACHE_DIR if wrapper else root])
    res["final_du_kb"] = final
    if wrapper:
        res["sccache_stats"] = sccache_stats()
    OUT.mkdir(exist_ok=True)
    (OUT / f"run-{label}.json").write_text(json.dumps(res, indent=1) + "\n")
    print(json.dumps(res["peaks"], indent=1))
    if a.keep_gate:
        dst = BASE / "gate-seed"
        if dst.exists():
            shutil.rmtree(dst)
        (root / "gate" / "target").rename(dst)
    if not a.keep:
        shutil.rmtree(root)
        if a.slots:
            shutil.rmtree(BASE / "slots")
    subprocess.run(["docker", "rmi", "-f", f"r0-13/api-musl:{label}"], capture_output=True)


def cmd_seed(a):
    root = BASE / "seed"
    if root.exists():
        shutil.rmtree(root)
    gen("cli", root / "ws")
    env = base_env(root / "target", False)
    logdir = BASE / "logs" / "seed"
    logdir.mkdir(parents=True, exist_ok=True)
    res = {"uptime_start": uptime(), "wait": wait_for_load()}
    log = []
    run_step(log, "seed-clippy", ["cargo", "clippy", "--locked", "--offline", "--workspace", "--all-targets", "--", "-D", "warnings"], root / "ws", env, logdir)
    run_step(log, "seed-nextest", ["cargo", "nextest", "run", "--locked", "--offline", "--workspace"], root / "ws", env, logdir)
    run_step(log, "seed-doctest", ["cargo", "test", "--doc", "--locked", "--offline", "--workspace"], root / "ws", env, logdir)
    lst = subprocess.run(["cargo", "nextest", "list", "--locked", "--offline", "--workspace", "--list-type", "binaries-only", "--message-format", "json"],
                         cwd=root / "ws", env=env, capture_output=True, text=True)
    res["test_binaries_workspace"] = len(json.loads(lst.stdout)["rust-binaries"])
    res["steps"] = log
    res["uptime_end"] = uptime()
    res["target_kb"] = du_kb([root / "target"])
    res["lines_arg"] = LINES
    (OUT / f"seed-{LINES}.json").write_text(json.dumps(res, indent=1) + "\n")
    print(json.dumps(res, indent=1))


def cmd_single(a):
    root = BASE / f"single-{a.label}"
    if root.exists():
        shutil.rmtree(root)
    gen("cli", root / "ws", a.lines)
    logdir = BASE / "logs" / f"single-{a.label}"
    if logdir.exists():
        shutil.rmtree(logdir)
    logdir.mkdir(parents=True)
    res = {"label": a.label, "lines_arg": a.lines}
    out = subprocess.run(["find", str(root / "ws"), "-name", "*.rs", "-not", "-path", "*/spinup-kit/*"], capture_output=True, text=True).stdout.split()
    res["cli_lines"] = sum(Path(p).read_text().count("\n") for p in out)
    res["wait"] = wait_for_load()
    res["uptime_start"] = uptime()
    res["swap_baseline_mb"] = swap_used_mb()
    sampler = Sampler([root], logdir / "samples.jsonl")
    sampler.start()
    ev = threading.Event()
    ev.set()
    lr = {}
    lane_job("lane-b", root / "ws", root / "target", False, logdir, ev, lr)
    # The whole workspace in the same target dir: the gate rs-build form.
    log = []
    env = base_env(root / "target", False)
    run_step(log, "workspace-clippy", ["cargo", "clippy", "--locked", "--offline", "--workspace", "--all-targets", "--", "-D", "warnings"], root / "ws", env, logdir)
    run_step(log, "workspace-nextest", ["cargo", "nextest", "run", "--locked", "--offline", "--workspace", "--no-run"], root / "ws", env, logdir)
    sampler.stop_ev.set()
    sampler.join()
    res["lane"] = lr
    res["workspace_steps"] = log
    res["uptime_end"] = uptime()
    res["peaks"] = peaks(sampler.samples, res["swap_baseline_mb"])
    res["target_kb"] = du_kb([root / "target"])
    (OUT / f"single-{a.label}.json").write_text(json.dumps(res, indent=1) + "\n")
    print(json.dumps({k: v for k, v in res.items() if k != "lane"}, indent=1))
    shutil.rmtree(root)


def cmd_pathkey(a):
    """The sccache key across paths. Each run builds spinup-render and
    spinup-upgrade (with their registry dependencies) in a target dir that
    does not exist yet, with CARGO_INCREMENTAL=0, so path crates can cache
    too. The runs change the worktree path, the target path, or both."""
    global SCCACHE_DIR
    SCCACHE_DIR = BASE / "sccache-pathkey"
    root = BASE / "pathkey"
    for d in (root, SCCACHE_DIR):
        if d.exists():
            shutil.rmtree(d)
    x, y = root / "x" / "ws", root / "elsewhere" / "deeper" / "ws"
    gen("cli", x)
    gen("cli", y)
    logdir = BASE / "logs" / "pathkey"
    logdir.mkdir(parents=True, exist_ok=True)
    res = {"uptime_start": uptime(), "wait": wait_for_load(), "runs": []}
    t1, t2, t3 = root / "t1" / "target", root / "t2" / "target", root / "other" / "t3" / "target"
    z, w = root / "z" / "ws", root / "w" / "ws"
    t4, t5 = root / "t4" / "target", root / "t5" / "target"
    gen("cli", z)
    gen("cli", w)
    # The basedirs runs use paths that no earlier run used, so a hit can
    # only come from the basedirs normalization.
    plan = [
        ("x-t1-first", x, t1, "0", None),
        ("x-t1-same-paths", x, t1, "0", None),
        ("x-t2-other-target", x, t2, "0", None),
        ("y-t1-other-worktree", y, t1, "0", None),
        ("y-t3-both-other", y, t3, "0", None),
        ("z-t4-basedirs-first", z, t4, "0", str(root)),
        ("w-t5-basedirs-other-paths", w, t5, "0", str(root)),
        ("z-t4-basedirs-same-paths", z, t4, "0", str(root)),
        ("x-t1-default-incremental", x, t1, None, None),
    ]
    SCCACHE_DIR.mkdir(parents=True)
    for name, ws, tgt, inc, basedirs in plan:
        e = dict(os.environ)
        e.pop("SCCACHE_BASEDIRS", None)
        if basedirs:
            e["SCCACHE_BASEDIRS"] = basedirs
        sccache("--stop-server", env=e)
        sccache("--start-server", env=e)
        sccache("--zero-stats", env=e)
        env = base_env(tgt, True)
        # sccache 0.19 refuses CARGO_INCREMENTAL=1. None keeps the cargo
        # default (incremental for workspace crates in the dev profile).
        env.pop("CARGO_INCREMENTAL", None)
        if inc is not None:
            env["CARGO_INCREMENTAL"] = inc
        if basedirs:
            env["SCCACHE_BASEDIRS"] = basedirs
        if tgt.exists():
            shutil.rmtree(tgt)
        log = []
        r = run_step(log, f"pathkey-{name}", ["cargo", "build", "--locked", "--offline", "-p", "spinup-render", "-p", "spinup-upgrade"], ws, env, logdir)
        st = sccache_stats().get("stats", {})
        hits = st.get("cache_hits", {}).get("counts", {})
        miss = st.get("cache_misses", {}).get("counts", {})
        res["runs"].append({
            "name": name, "ws": str(ws), "target": str(tgt), "incremental": inc, "basedirs": basedirs,
            "secs": r["secs"], "rc": r["rc"], "rust_hits": hits.get("Rust", 0), "rust_misses": miss.get("Rust", 0),
            "c_hits": hits.get("C/C++", 0), "c_misses": miss.get("C/C++", 0), "not_cached": st.get("not_cached"),
        })
        print(res["runs"][-1], flush=True)
        if tgt.exists():
            shutil.rmtree(tgt)
    res["uptime_end"] = uptime()
    res["sccache_dir_kb"] = du_kb([SCCACHE_DIR])
    sccache("--stop-server")
    (OUT / "pathkey.json").write_text(json.dumps(res, indent=1) + "\n")
    shutil.rmtree(root)
    shutil.rmtree(SCCACHE_DIR)


def cmd_appprofile(a):
    root = BASE / "appprofile"
    if root.exists():
        shutil.rmtree(root)
    gen("gate", root / "gate")
    app = root / "gate" / "apps" / "auth"
    logdir = BASE / "logs" / "appprofile"
    logdir.mkdir(parents=True, exist_ok=True)
    res = {"runs": []}
    for name in ("shipped", "default"):
        if name == "default":
            t = (app / "Cargo.toml").read_text()
            t = re.sub(r"# The dev profile that the generated app ships \(R0.11\).\n\[profile.dev\]\ndebug = 0\nstrip = \"symbols\"\n\n\[profile.dev.package.\"\*\"\]\nopt-level = 1\n", "", t)
            assert "opt-level = 1" not in t
            (app / "Cargo.toml").write_text(t)
        tgt = root / f"target-{name}"
        env = base_env(tgt, False)
        w = wait_for_load()
        log = []
        r = run_step(log, f"app-{name}", ["cargo", "build", "--locked", "--offline", "--all-targets"], app, env, logdir)
        res["runs"].append({"name": name, "secs": r["secs"], "rc": r["rc"], "wait": w, "uptime_end": uptime(), "target_kb": du_kb([tgt])})
        shutil.rmtree(tgt)
    (OUT / "appprofile.json").write_text(json.dumps(res, indent=1) + "\n")
    print(json.dumps(res, indent=1))
    shutil.rmtree(root)


def main():
    global LINES
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("run")
    r.add_argument("label")
    r.add_argument("--profile", choices=["plain", "sccache", "clone"], required=True)
    r.add_argument("--fresh-cache", action="store_true")
    r.add_argument("--keep", action="store_true")
    r.add_argument("--keep-gate", action="store_true", help="keep the gate target as the clone seed")
    r.add_argument("--lane-jobs", type=int)
    r.add_argument("--basedirs", action="store_true")
    r.add_argument("--slots", action="store_true")
    r.add_argument("--lines", type=int, default=LINES)
    r.add_argument("--gate-jobs", type=int)
    r.add_argument("--test-threads", type=int)
    sd = sub.add_parser("seed")
    sd.add_argument("--lines", type=int, default=LINES)
    s = sub.add_parser("single")
    s.add_argument("label")
    s.add_argument("--lines", type=int, default=LINES)
    sub.add_parser("pathkey")
    sub.add_parser("appprofile")
    a = ap.parse_args()
    if a.cmd in ("run", "seed"):
        LINES = a.lines
    BASE.mkdir(parents=True, exist_ok=True)
    {"run": cmd_run, "seed": cmd_seed, "single": cmd_single, "pathkey": cmd_pathkey, "appprofile": cmd_appprofile}[a.cmd](a)


if __name__ == "__main__":
    main()
