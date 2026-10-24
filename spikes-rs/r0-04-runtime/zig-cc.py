#!/usr/bin/env python3
"""Translate cc-rs target flags for the locally installed Zig compiler."""
import os
import sys

args = []
source = iter(sys.argv[1:])
for arg in source:
    if arg in ("-target", "--target"):
        next(source)
    elif not arg.startswith("--target="):
        args.append(arg)
os.execvp("zig", ["zig", "cc", "-target", os.environ["R04_ZIG_TARGET"], *args])
