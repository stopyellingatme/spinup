#!/usr/bin/env python3
"""Run the R0.4 measurements offline, one build at a time."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parent
RESULT = ROOT / "measurements.json"
TARGET = Path(os.environ["CARGO_TARGET_DIR"])
TARGET.mkdir(parents=True, exist_ok=True)
LOGS = TARGET / "r04-measure-logs"
LOGS.mkdir(exist_ok=True)


def save(data):
    RESULT.write_text(json.dumps(data, indent=2) + "\n")


def run(command, env, label):
    with (LOGS / (label + ".log")).open("w") as log:
        return subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT).returncode


def release(data, packages=("sync-cli", "async-cli")):
    data["machine"] = {
        "uname": subprocess.check_output(["uname", "-a"], text=True).strip(),
        "rustc": subprocess.check_output(["rustc", "--version"], text=True).strip(),
        "jobs": os.environ.get("CARGO_BUILD_JOBS"),
        "rustc_wrapper": os.environ.get("RUSTC_WRAPPER", "unset"),
    }
    data.setdefault("release", {})
    for package in packages:
        entry = {}
        with tempfile.TemporaryDirectory(prefix=package + "-release-", dir=TARGET) as build_dir:
            env = dict(os.environ, CARGO_TARGET_DIR=build_dir)
            command = ["cargo", "build", "--release", "-p", package, "--bin", package, "--offline", "--locked"]
            source = ROOT / package / "src/main.rs"
            original = source.read_bytes()
            for mode in ["clean", "warm"]:
                if mode == "warm":
                    source.write_bytes(original + b"\n")
                try:
                    before = subprocess.check_output(["uptime"], text=True).strip()
                    start = time.perf_counter()
                    code = run(command, env, package + "-" + mode)
                    seconds = time.perf_counter() - start
                    after = subprocess.check_output(["uptime"], text=True).strip()
                    entry[mode] = dict(seconds=round(seconds, 3), uptime_before=before, uptime_after=after, exit_code=code, command=" ".join(command))
                    data["release"][package] = entry
                    save(data)
                    print(package, mode, entry[mode], flush=True)
                    if code:
                        raise RuntimeError("build failed: " + str(LOGS / (package + "-" + mode + ".log")))
                finally:
                    if mode == "warm":
                        source.write_bytes(original)
            binary = Path(build_dir) / "release" / package
            entry["stripped_bytes"] = binary.stat().st_size
            entry["file"] = subprocess.check_output(["file", str(binary)], text=True).strip().split(": ", 1)[-1]
            # Mach-O retains only the dynamic symbol table after symbol stripping.
            entry["nm_defined_lines"] = len(subprocess.check_output(["nm", "-U", str(binary)], text=True, stderr=subprocess.DEVNULL).splitlines())
            save(data)


def cross(data):
    data["cross"] = []
    targets = ["aarch64-apple-darwin", "x86_64-apple-darwin", "x86_64-unknown-linux-musl", "aarch64-unknown-linux-musl", "x86_64-pc-windows-gnu", "aarch64-pc-windows-gnullvm"]
    for target in targets:
        for package in ["sync-cli", "async-cli"]:
            command = ["cargo", "check", "-p", package, "--target", target, "--offline", "--locked"]
            label = target + "-" + package
            code = run(command, os.environ, label)
            lines = (LOGS / (label + ".log")).read_text().splitlines()
            errors = [s for s in lines if "error" in s.lower() or "failed to find" in s.lower()]
            item = dict(target=target, package=package, exit_code=code, command=" ".join(command), errors=[s[:500] for s in errors[-12:]])
            data["cross"].append(item)
            save(data)
            print(target, package, code, "\n".join(s[:500] for s in errors[-4:]), flush=True)


def cross_zig(data):
    data["cross_zig"] = []
    targets = {
        "x86_64-unknown-linux-musl": "x86_64-linux-musl",
        "aarch64-unknown-linux-musl": "aarch64-linux-musl",
        "x86_64-pc-windows-gnu": "x86_64-windows-gnu",
    }
    for target, zig_target in targets.items():
        key = target.replace("-", "_")
        env = dict(os.environ)
        env["CC_" + key] = str(ROOT / "zig-cc.py")
        env["R04_ZIG_TARGET"] = zig_target
        env["AR_" + key] = "zig ar"
        env["ZIG_GLOBAL_CACHE_DIR"] = str(TARGET / "zig-global-cache")
        env["ZIG_LOCAL_CACHE_DIR"] = str(TARGET / "zig-local-cache")
        for package in ["sync-cli", "async-cli"]:
            command = ["cargo", "check", "-p", package, "--target", target, "--offline", "--locked"]
            label = "zig-" + target + "-" + package
            code = run(command, env, label)
            rows = (LOGS / (label + ".log")).read_text().splitlines()
            errors = [s for s in rows if "error" in s.lower() or "failed to find" in s.lower()]
            item = dict(target=target, package=package, exit_code=code, command=" ".join(command), cc=env["CC_" + key], ar=env["AR_" + key], zig_target=zig_target, errors=[s[:500] for s in errors[-12:]])
            data["cross_zig"].append(item)
            save(data)
            print("zig", target, package, code, "\n".join(s[:500] for s in errors[-4:]), flush=True)


def lines(data):
    data["lines"] = {}
    for member in ["common", "sync-cli", "async-cli"]:
        source = "".join(p.read_text() for p in sorted((ROOT / member / "src").glob("*.rs")))
        rows = source.splitlines()
        data["lines"][member] = dict(physical=len(rows), nonblank_noncomment=sum(bool(s.strip()) and not s.lstrip().startswith("//") for s in rows))
    save(data)
    print(data["lines"], flush=True)


if __name__ == "__main__":
    data = json.loads(RESULT.read_text()) if RESULT.exists() else {}
    for mode in sys.argv[1:]:
        {"release": release, "release-async": lambda data: release(data, ("async-cli",)), "cross": cross, "cross-zig": cross_zig, "lines": lines}[mode](data)
