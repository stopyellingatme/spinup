#!/usr/bin/env python3
"""R0.12 measurements: binary size, start time, RSS, and image numbers.

Usage:
  measure.py host   --bin <dir> --out <json>   # the host binaries in <dir>
  measure.py images --bin <dir> --out <json> --vendor <dir> --ctx <dir>

`host` expects these files in --bin (see the result file for the build
commands): rs-spinup, go-spinup, go-spinup-stripped, rs-api,
rs-api-mimalloc, rs-api-noauth, go-api-stripped, go-api-base. It reads R12_RS_DATABASE_URL and
R12_GO_DATABASE_URL (migrated databases for the two APIs).

`images` builds the musl image (host cargo zigbuild, then distroless static),
the glibc image (a build in rust:1.99.1-slim-trixie with --network=none and
the vendored crates, then distroless cc), and the Go comparison image. It
records cold and warm build times, image sizes, and the read-only compose
check. --ctx is a scratch copy of this directory (the script edits
todo-api/src/todos.rs there for the warm builds).

Every timing records the 1-minute load average before and after. The script
waits until the 1-minute load average is below 2 before each group.
"""

import argparse
import json
import os
import shutil
import socket
import statistics
import subprocess
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
LOAD_LIMIT = 2.0


def load1():
    return os.getloadavg()[0]


def wait_idle(label, limit=LOAD_LIMIT, timeout=900):
    start = time.time()
    while load1() >= limit:
        if time.time() - start > timeout:
            print(f"{label}: load {load1():.2f} stayed above {limit}", file=sys.stderr)
            break
        time.sleep(5)
    return round(load1(), 2)


def stats_ms(xs):
    xs = sorted(xs)
    p90 = xs[min(len(xs) - 1, int(round(0.9 * (len(xs) - 1))))]
    return {
        "n": len(xs),
        "median_ms": round(statistics.median(xs) * 1000, 2),
        "p90_ms": round(p90 * 1000, 2),
        "min_ms": round(xs[0] * 1000, 2),
        "max_ms": round(xs[-1] * 1000, 2),
    }


def run_timed(argv, n=50, warmup=5, env=None):
    for _ in range(warmup):
        subprocess.run(argv, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env, check=False)
    xs = []
    for _ in range(n):
        t = time.perf_counter()
        subprocess.run(argv, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env, check=True)
        xs.append(time.perf_counter() - t)
    return stats_ms(xs)


def max_rss_bytes(argv, env=None):
    """macOS /usr/bin/time -l reports the maximum resident set size in bytes."""
    p = subprocess.run(["/usr/bin/time", "-l", *argv], capture_output=True, text=True, env=env, check=True)
    for line in p.stderr.splitlines():
        if "maximum resident set size" in line:
            return int(line.split()[0])
    raise RuntimeError(p.stderr)


def healthz_ok(port):
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=0.2) as s:
            s.sendall(b"GET /healthz HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
            data = s.recv(64)
            return data.startswith(b"HTTP/1.1 200")
    except OSError:
        return False


def rss_kib(pid):
    out = subprocess.run(["ps", "-o", "rss=", "-p", str(pid)], capture_output=True, text=True).stdout
    return int(out.strip())


def footprint_kib(pid):
    """The macOS physical footprint (vmmap), the memory that Activity Monitor
    shows. ps RSS also counts freed pages that the allocator keeps."""
    out = subprocess.run(["vmmap", "-summary", str(pid)], capture_output=True, text=True).stdout
    for line in out.splitlines():
        if line.startswith("Physical footprint:"):
            v = line.split()[-1]
            unit = {"K": 1, "M": 1024, "G": 1024 * 1024}[v[-1]]
            return round(float(v[:-1]) * unit)
    return None


def api_start(argv, env, port, n=20):
    """Spawn to the first 200 from /healthz, n times. Idle RSS of the last run
    after 3 s, and after 2000 /healthz requests."""
    xs = []
    idle = burst = idle_fp = burst_fp = None
    for i in range(n):
        t = time.perf_counter()
        p = subprocess.Popen(argv, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        while not healthz_ok(port):
            if p.poll() is not None:
                raise RuntimeError(f"{argv[0]} exited with {p.returncode}")
            if time.perf_counter() - t > 10:
                p.kill()
                raise RuntimeError(f"{argv[0]}: no /healthz answer in 10 s")
            time.sleep(0.0005)
        xs.append(time.perf_counter() - t)
        if i == n - 1:
            time.sleep(3)
            idle = rss_kib(p.pid)
            idle_fp = footprint_kib(p.pid)
            for _ in range(2000):
                healthz_ok(port)
            time.sleep(1)
            burst = rss_kib(p.pid)
            burst_fp = footprint_kib(p.pid)
        p.terminate()
        p.wait(timeout=30)
    return {
        "healthz": stats_ms(xs),
        "idle_rss_kib": idle,
        "idle_footprint_kib": idle_fp,
        "rss_after_2000_requests_kib": burst,
        "footprint_after_2000_requests_kib": burst_fp,
    }


def host(args):
    b = Path(args.bin)
    res = {"machine": machine(), "binaries": {}, "cli": {}, "api": {}}
    for f in sorted(b.iterdir()):
        if f.is_file() and os.access(f, os.X_OK):
            res["binaries"][f.name] = f.stat().st_size
    for name in ["rs-spinup", "go-spinup", "go-spinup-stripped"]:
        exe = str(b / name)
        l0 = wait_idle(name)
        entry = {
            "help": run_timed([exe, "--help"]),
            "version": run_timed([exe, "version"]),
            "help_max_rss_bytes": max_rss_bytes([exe, "--help"]),
        }
        if name == "rs-spinup":
            entry["cache_list"] = run_timed([exe, "cache", "list"], n=20)
            entry["cache_list_max_rss_bytes"] = max_rss_bytes([exe, "cache", "list"])
        entry["load1_before"] = l0
        entry["load1_after"] = round(load1(), 2)
        res["cli"][name] = entry
        print(name, json.dumps(entry), flush=True)
    apis = [
        ("rs-api", os.environ["R12_RS_DATABASE_URL"], ["serve"]),
        ("rs-api-mimalloc", os.environ["R12_RS_DATABASE_URL"], ["serve"]),
        ("rs-api-noauth", os.environ["R12_RS_DATABASE_URL"], ["serve"]),
        ("go-api-stripped", os.environ["R12_GO_DATABASE_URL"], []),
        ("go-api-base", os.environ["R12_GO_DATABASE_URL"], []),
    ]
    for i, (name, db, sub) in enumerate(apis):
        port = 18200 + i
        env = {**os.environ, "DATABASE_URL": db, "PORT": str(port), "SPINUP_ENV": "dev", "LOG_LEVEL": "info"}
        env.pop("OTEL_EXPORTER_OTLP_ENDPOINT", None)
        l0 = wait_idle(name)
        entry = api_start([str(b / name), *sub], env, port)
        entry["load1_before"] = l0
        entry["load1_after"] = round(load1(), 2)
        res["api"][name] = entry
        print(name, json.dumps(entry), flush=True)
    Path(args.out).write_text(json.dumps(res, indent=2) + "\n")


def sh(argv, cwd=None, env=None):
    t = time.perf_counter()
    l0 = round(load1(), 2)
    p = subprocess.run(argv, cwd=cwd, env=env, capture_output=True, text=True)
    if p.returncode != 0:
        print(p.stdout[-3000:], p.stderr[-3000:], file=sys.stderr)
        raise RuntimeError(f"failed: {' '.join(argv)}")
    return {"s": round(time.perf_counter() - t, 1), "load1_before": l0, "load1_after": round(load1(), 2)}


def image_size(tag):
    out = subprocess.run(["docker", "image", "inspect", "-f", "{{.Size}}", tag], capture_output=True, text=True, check=True)
    return int(out.stdout.strip())


def warm_edit(ctx, n):
    f = Path(ctx) / "todo-api/src/todos.rs"
    f.write_text(f.read_text() + f"// R0.12 warm edit {n}\n")


def images(args):
    ctx = Path(args.ctx)
    if ctx.exists():
        shutil.rmtree(ctx)
    shutil.copytree(HERE, ctx, ignore=shutil.ignore_patterns("target"))
    target = Path(os.environ["CARGO_TARGET_DIR"])
    musl = "aarch64-unknown-linux-musl"
    res = {"machine": machine(), "musl": {}, "glibc": {}, "go": {}, "compose": {}}

    # musl: host zigbuild (the R0.14 command), then distroless static.
    zig = ["nice", "-n", "10", "cargo", "zigbuild", "--release", "--locked", "--offline", "-p", "todo-api", "--target", musl]
    bindir = target / musl / "release"
    dmusl = ["docker", "build", "-f", "docker/Dockerfile.musl", "--build-context", f"bin={bindir}", "-t", "r0-12/api-musl", "."]
    shutil.rmtree(target / musl, ignore_errors=True)
    res["musl"]["load1_wait"] = wait_idle("musl cold")
    res["musl"]["cold_binary"] = sh(zig, cwd=ctx)
    res["musl"]["cold_image"] = sh([*dmusl[:2], "--no-cache", *dmusl[2:]], cwd=ctx)
    res["musl"]["binary_bytes"] = (bindir / "todo-api").stat().st_size
    warm = []
    for n in range(3):
        wait_idle("musl warm")
        warm_edit(ctx, n)
        w = {"binary": sh(zig, cwd=ctx), "image": sh(dmusl, cwd=ctx)}
        warm.append(w)
    res["musl"]["warm"] = warm
    res["musl"]["image_bytes"] = image_size("r0-12/api-musl")
    print("musl", json.dumps(res["musl"]), flush=True)

    # glibc: the build runs in the Rust image, offline, with vendored crates.
    dglibc = ["docker", "build", "--network=none", "-f", "docker/Dockerfile.glibc", "--build-context", f"vendor={args.vendor}", "-t", "r0-12/api-glibc", "."]
    res["glibc"]["load1_wait"] = wait_idle("glibc cold")
    res["glibc"]["cold_image"] = sh([*dglibc[:2], "--no-cache", *dglibc[2:]], cwd=ctx)
    warm = []
    for n in range(3, 6):
        wait_idle("glibc warm")
        warm_edit(ctx, n)
        warm.append({"image": sh(dglibc, cwd=ctx)})
    res["glibc"]["warm"] = warm
    res["glibc"]["image_bytes"] = image_size("r0-12/api-glibc")
    out = subprocess.run(["docker", "run", "--rm", "--entrypoint", "/api", "r0-12/api-glibc", "--help"], capture_output=True, text=True)
    res["glibc"]["runs"] = out.returncode == 0
    print("glibc", json.dumps(res["glibc"]), flush=True)

    # Go comparison image.
    gobin = Path(args.bin) / "go-linux"
    gobin.mkdir(exist_ok=True)
    shutil.copy2(Path(args.bin) / "go-api-linux-arm64", gobin / "api")
    dgo = ["docker", "build", "--no-cache", "-f", "docker/Dockerfile.goapi", "--build-context", f"bin={gobin}", "-t", "r0-12/api-go", "."]
    res["go"]["image"] = sh(dgo, cwd=ctx)
    res["go"]["image_bytes"] = image_size("r0-12/api-go")
    res["go"]["binary_bytes"] = (gobin / "api").stat().st_size

    # Linux RSS: each Linux binary runs `serve` in the Rust image (it has a
    # shell and glibc). VmRSS after 3 s idle, and VmHWM (the peak).
    lin = Path(args.bin) / "linux"
    lin.mkdir(exist_ok=True)
    shutil.copy2(bindir / "todo-api", lin / "rs-musl")
    cid = subprocess.run(["docker", "create", "r0-12/api-glibc"], capture_output=True, text=True, check=True).stdout.strip()
    subprocess.run(["docker", "cp", f"{cid}:/api", str(lin / "rs-glibc")], check=True, capture_output=True)
    subprocess.run(["docker", "rm", cid], check=True, capture_output=True)
    shutil.copy2(Path(args.bin) / "go-api-linux-arm64", lin / "go-static")
    res["glibc"]["binary_bytes"] = (lin / "rs-glibc").stat().st_size
    res["linux_rss"] = {}
    for name, rs in [("rs-musl", True), ("rs-glibc", True), ("go-static", False)]:
        wait_idle(name)
        db = os.environ.get("R12_RS_DOCKER_DATABASE_URL" if rs else "R12_GO_DOCKER_DATABASE_URL")
        sub = "serve" if rs else ""
        script = f"/b/{name} {sub} >/dev/null 2>&1 & pid=$!; sleep 3; grep -E 'VmRSS|VmHWM' /proc/$pid/status; kill $pid"
        out = subprocess.run(
            ["docker", "run", "--rm", "-v", f"{lin}:/b:ro", "-e", f"DATABASE_URL={db}", "-e", "PORT=8080", "-e", "SPINUP_ENV=dev",
             "rust:1.99.1-slim-trixie", "sh", "-c", script],
            capture_output=True, text=True,
        ).stdout
        vals = {line.split(":")[0]: int(line.split()[1]) for line in out.splitlines() if line.startswith("Vm")}
        res["linux_rss"][name] = {"vmrss_kib": vals.get("VmRSS"), "vmhwm_kib": vals.get("VmHWM")}
        print(name, json.dumps(res["linux_rss"][name]), flush=True)

    # The read-only compose check: time to healthy, the health state, the
    # read-only flag, a /healthz answer, and the container memory.
    for svc, port in [("api-musl", 18091), ("api-glibc", 18092)]:
        cmd = ["docker", "compose", "-f", "docker/compose.yml", "-p", "r0-12"]
        wait_idle(svc)
        r = {"up": sh([*cmd, "up", "-d", "--wait", svc], cwd=ctx)}
        cid = subprocess.run([*cmd, "ps", "-q", svc], cwd=ctx, capture_output=True, text=True).stdout.strip()
        fmt = "{{.State.Health.Status}} {{.HostConfig.ReadonlyRootfs}}"
        health, ro = subprocess.run(["docker", "inspect", "-f", fmt, cid], capture_output=True, text=True).stdout.split()
        r["health"] = health
        r["read_only"] = ro == "true"
        r["healthz_from_host"] = healthz_ok(port)
        time.sleep(3)
        r["mem_usage"] = subprocess.run(["docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", cid], capture_output=True, text=True).stdout.strip()
        sh([*cmd, "down"], cwd=ctx)
        res["compose"][svc] = r
        print(svc, json.dumps(r), flush=True)
    Path(args.out).write_text(json.dumps(res, indent=2) + "\n")


def machine():
    def out(argv):
        return subprocess.run(argv, capture_output=True, text=True).stdout.strip()

    return {
        "cpu": out(["sysctl", "-n", "machdep.cpu.brand_string"]),
        "ncpu": int(out(["sysctl", "-n", "hw.ncpu"])),
        "mem_bytes": int(out(["sysctl", "-n", "hw.memsize"])),
        "os": out(["sw_vers", "-productVersion"]),
        "uptime": out(["uptime"]),
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("mode", choices=["host", "images"])
    ap.add_argument("--bin", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--vendor")
    ap.add_argument("--ctx")
    args = ap.parse_args()
    {"host": host, "images": images}[args.mode](args)


if __name__ == "__main__":
    main()
