#!/usr/bin/env python3
"""Embed dependency metadata in the artifact before GoReleaser packages it."""
import os
from pathlib import Path
import shutil
import subprocess
import sys

target, artifact = sys.argv[1:]
subprocess.run(["cargo", "auditable", "zigbuild", "--release", "--offline", "--locked",
                "--package=cli-skel", "--bin=spinup", "--target", target], check=True)
suffix = ".exe" if "windows" in target else ""
source = Path(os.environ.get("CARGO_TARGET_DIR", "target")) / target / "release" / f"spinup{suffix}"
shutil.copy2(source, artifact)
