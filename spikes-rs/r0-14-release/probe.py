#!/usr/bin/env python3
"""Measure release commands with uptime samples and retain their complete output."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parent
TARGETS = [
    "x86_64-unknown-linux-musl",
    "aarch64-unknown-linux-musl",
    "x86_64-apple-darwin",
    "aarch64-apple-darwin",
    "x86_64-pc-windows-gnu",
    "aarch64-pc-windows-gnullvm",
]


def measure(label, command):
    evidence = ROOT / "evidence"
    evidence.mkdir(exist_ok=True)
    env = dict(os.environ, PWD=str(ROOT))
    env["PATH"] = str(ROOT / "tools") + os.pathsep + env["PATH"]
    target_dir = env.get("CARGO_TARGET_DIR")
    # GoReleaser 2.18.3 copies from ./target even with CARGO_TARGET_DIR set.
    if target_dir and not (ROOT / "target").exists():
        (ROOT / "target").symlink_to(Path(target_dir).resolve(), target_is_directory=True)
    before = subprocess.check_output(["uptime"], text=True).strip()
    start = time.monotonic()
    with (evidence / f"{label}.log").open("w") as log:
        result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
    seconds = round(time.monotonic() - start, 3)
    after = subprocess.check_output(["uptime"], text=True).strip()
    record = dict(command=command, exit=result.returncode, seconds=seconds,
                  uptime_before=before, uptime_after=after)
    (evidence / f"{label}.json").write_text(json.dumps(record, indent=2) + "\n")
    print(label, json.dumps(record), flush=True)
    print((evidence / f"{label}.log").read_text()[-3500:], flush=True)
    return result.returncode


def release(targets, label=None):
    if isinstance(targets, str):
        targets = [targets]
    label = label or targets[0]
    config = (ROOT / ".goreleaser.yaml").read_text()
    config = re.sub(r"    targets:\n(?:      - .*\n)+", f"    targets: [{', '.join(targets)}]\n", config)
    config = config.replace("dist: dist/rust", f"dist: dist/{label}")
    evidence = ROOT / "evidence"
    evidence.mkdir(exist_ok=True)
    path = evidence / f"{label}.yaml"
    path.write_text(config)
    return measure(label, ["goreleaser", "release", "--snapshot", "--clean", "--skip=publish,sign",
                           "--parallelism=1", "--config", str(path)])


if __name__ == "__main__":
    args = sys.argv[1:]
    if args == ["matrix"]:
        sys.exit(int(any([release(target) != 0 for target in TARGETS])))
    if args == ["installed"]:
        sys.exit(release(TARGETS[:-1], "installed"))
    if args and args[0] in TARGETS and len(args) == 1:
        sys.exit(release(args[0]))
    if len(args) > 1:
        sys.exit(measure(args[0], args[1:]))
    sys.exit("usage: probe.py matrix | installed | TARGET | LABEL COMMAND [ARG ...]")
