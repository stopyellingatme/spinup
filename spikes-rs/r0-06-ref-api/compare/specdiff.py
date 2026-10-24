"""Lists the semantic differences between the Go bundle and the Rust spec.

Usage: python3 -I specdiff.py go.bundle.gen.yaml rust.openapi.yaml

It compares the parsed documents, so key order and quoting do not count.
Each line is one difference: a JSON pointer, the Go value, and the Rust value.
"""

import json
import sys

import yaml


def walk(path, a, b, out):
    if isinstance(a, dict) and isinstance(b, dict):
        for k in sorted(set(a) | set(b), key=str):
            p = f"{path}/{str(k).replace('~', '~0').replace('/', '~1')}"
            if k not in a:
                out.append((p, "(absent)", b[k]))
            elif k not in b:
                out.append((p, a[k], "(absent)"))
            else:
                walk(p, a[k], b[k], out)
    elif isinstance(a, list) and isinstance(b, list) and len(a) == len(b):
        for i, (x, y) in enumerate(zip(a, b)):
            walk(f"{path}/{i}", x, y, out)
    elif a != b:
        out.append((path, a, b))


def main():
    with open(sys.argv[1]) as f:
        go = yaml.safe_load(f)
    with open(sys.argv[2]) as f:
        rust = yaml.safe_load(f)
    out = []
    walk("", go, rust, out)
    for p, a, b in out:
        print(f"{p}: go={json.dumps(a)} rust={json.dumps(b)}")
    print(f"{len(out)} differences", file=sys.stderr)


if __name__ == "__main__":
    main()
