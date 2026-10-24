#!/usr/bin/env python3
"""Run the registration matrix. Do not run beside another Cargo command here."""
import argparse
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import time

ROOT = Path(__file__).resolve().parent
TARGET = Path(os.environ["CARGO_TARGET_DIR"]).resolve()
WIRE = ROOT / "app/src/wire_gen.rs"
EXPECTED = (ROOT / "app/tests/expected_registry.txt").read_text()
RECORDS = []


def command(args):
    load_before = os.getloadavg()[0] if hasattr(os, "getloadavg") else None
    uptime = (subprocess.check_output(["uptime"], text=True).strip()
              if shutil.which("uptime") else "not measured: uptime is unavailable")
    start = time.monotonic()
    result = subprocess.run(args, cwd=ROOT, text=True, capture_output=True)
    record = dict(command=args, elapsed_s=round(time.monotonic() - start, 3),
                  load1_before=load_before,
                  load1_after=os.getloadavg()[0] if hasattr(os, "getloadavg") else None,
                  uptime=uptime, status=result.returncode,
                  stdout=result.stdout, stderr=result.stderr)
    RECORDS.append(record)
    print(f"{result.returncode}: {' '.join(args)}", flush=True)
    return result


def cargo(action, backend, profile, *extra):
    args = ["cargo", action, "--offline", "--locked", "--profile", profile,
            "--no-default-features", "--features", backend, *extra]
    return command(args)


def binary(profile):
    suffix = ".exe" if platform.system() == "Windows" else ""
    return TARGET / ("debug" if profile == "dev" else profile) / f"app{suffix}"


def native():
    original = WIRE.read_text()
    sizes = []
    try:
        for profile in ("dev", "release", "fat"):
            baseline = command(["cargo", "build", "-p", "app", "--offline", "--locked",
                                "--profile", profile, "--no-default-features"])
            assert baseline.returncode == 0, baseline.stderr
            baseline_bytes = binary(profile).stat().st_size
            for backend in ("linkme", "inventory"):
                WIRE.write_text(original)
                result = cargo("test", backend, profile, "--workspace")
                assert result.returncode == 0, result.stdout + result.stderr
                assert command([str(binary(profile))]).stdout == EXPECTED
                wired_bytes = binary(profile).stat().st_size
                for removed in (("reg_a",), ("reg_b",), ("reg_a", "reg_b")):
                    WIRE.write_text("".join(line for line in original.splitlines(True)
                                            if not any(f"use {name} as _;" == line.strip()
                                                       for name in removed)))
                    result = cargo("test", backend, profile, "-p", "app", "--test", "registry_ids")
                    assert result.returncode == 101, result.stdout + result.stderr
                    assert "registry IDs differ from expected_registry.txt" in result.stdout
                    wanted = "".join(line + "\n" for line in EXPECTED.splitlines()
                                     if line.replace("-", "_") not in removed)
                    actual = command([str(binary(profile))])
                    assert actual.returncode == 0 and actual.stdout == wanted
                unwired_bytes = binary(profile).stat().st_size
                WIRE.write_text(original)
                result = cargo("test", backend, profile, "-p", "app", "--test", "registry_ids")
                assert result.returncode == 0, result.stdout + result.stderr
                sizes.append(dict(profile=profile, backend=backend, baseline_bytes=baseline_bytes,
                                  wired_bytes=wired_bytes, unwired_bytes=unwired_bytes,
                                  total_cost_bytes=wired_bytes - baseline_bytes,
                                  registration_cost_bytes=wired_bytes - unwired_bytes))
    finally:
        WIRE.write_text(original)
    return sizes


def cross():
    for target in ("x86_64-unknown-linux-musl", "x86_64-pc-windows-gnu"):
        for profile in ("dev", "release", "fat"):
            for backend in ("linkme", "inventory"):
                result = cargo("build", backend, profile, "-p", "app", "--target", target)
                if result.returncode:
                    # Check the Rust compilation when the native linker cannot link.
                    result = cargo("check", backend, profile, "--workspace", "--all-targets", "--target", target)
                    assert result.returncode == 0, result.stderr


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cross", action="store_true")
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    report = dict(machine=platform.platform(), rustc=subprocess.check_output(
        ["rustc", "-Vv"], text=True), rustflags=os.environ.get("RUSTFLAGS", ""),
        target_linkers={key: value for key, value in os.environ.items()
                        if key.startswith("CARGO_TARGET_") and key.endswith("_LINKER")})
    try:
        report["sizes"] = [] if args.cross else native()
        if args.cross:
            cross()
    finally:
        report["commands"] = RECORDS
        args.output.write_text(json.dumps(report, indent=2) + "\n")
