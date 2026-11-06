"""The micro suite: the Go benchmarks of the repository (`go test -bench`).

The suite finds every package with a `func Benchmark...` in a _test.go file, in the root
module, kit/, and plugin-sdk/. It runs them once for each module:

  go test -run '^$' -bench . -benchmem -count N -benchtime T <packages>

Each benchmark gives one record for each unit (ns/op, B/op, allocs/op, and any custom
b.ReportMetric unit). The samples are the N runs. The metric name is
"<package> <benchmark> <unit>", with the GOMAXPROCS suffix removed, so results from machines
with other core counts compare by name.
"""

import os
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import lib  # noqa: E402

SUITE = "micro"
ROOT_MODULE = "github.com/stopyellingatme/spinup"
MODULE_DIRS = ("", "kit", "plugin-sdk")
SKIP_DIRS = {"testdata", "node_modules", "vendor"}
BENCH_FUNC = re.compile(r"(?m)^func Benchmark\w*\(")
BENCH_LINE = re.compile(r"^(Benchmark\S*)\s+(\d+)\s+(.+)$")
PAIR = re.compile(r"([-+0-9.eE]+)\s+(\S+)")


def find_packages(repo):
    """Returns {module dir (relative, "" for the root): [./pkg, ...]} of the packages with benchmarks."""
    repo = Path(repo)
    roots = {repo / m if m else repo: m for m in MODULE_DIRS}
    found = {m: set() for m in MODULE_DIRS}
    for dirpath, dirnames, filenames in os.walk(repo):
        d = Path(dirpath)
        if d != repo and "go.mod" in filenames and d not in roots:
            dirnames[:] = []  # another module (a spike, an example, a template): not ours
            continue
        dirnames[:] = sorted(n for n in dirnames if not n.startswith(".") and not n.startswith("_")
                             and n not in SKIP_DIRS)
        if not any(_has_bench(d / f) for f in filenames if f.endswith("_test.go")):
            continue
        mod = _module_of(d, roots)
        if mod is None:
            continue
        mod_dir = repo / mod if mod else repo
        rel = d.relative_to(mod_dir).as_posix()
        found[mod].add("./" + rel if rel != "." else ".")
    return {m: sorted(p) for m, p in found.items() if p}


def _has_bench(path):
    try:
        return bool(BENCH_FUNC.search(path.read_text(errors="replace")))
    except OSError:
        return False


def _module_of(d, roots):
    best = None
    for root, mod in roots.items():
        if d == root or root in d.parents:
            if best is None or len(root.parts) > len(best[0].parts):
                best = (root, mod)
    return best[1] if best else None


def _procs():
    try:
        return int(os.environ.get("GOMAXPROCS") or 0) or os.cpu_count() or 1
    except ValueError:
        return os.cpu_count() or 1


def parse(output, procs=None):
    """Parses `go test -bench` output. Returns {(package, benchmark, unit): [value, ...]} in order."""
    procs = procs or _procs()
    suffix = f"-{procs}"
    out = {}
    pkg = ""
    for line in output.splitlines():
        if line.startswith("pkg: "):
            pkg = line[5:].strip()
            continue
        m = BENCH_LINE.match(line.strip())
        if not m:
            continue
        name = m.group(1)
        if procs > 1 and name.endswith(suffix):
            name = name[: -len(suffix)]
        for value, unit in PAIR.findall(m.group(3)):
            try:
                v = float(value)
            except ValueError:
                continue
            out.setdefault((pkg, name, unit), []).append(v)
    return out


def short_pkg(pkg):
    if pkg == ROOT_MODULE:
        return "."
    return pkg[len(ROOT_MODULE) + 1:] if pkg.startswith(ROOT_MODULE + "/") else pkg


def run(ctx):
    recs = ctx.live
    count = ctx.n(6, 2)
    benchtime = "1s" if not ctx.quick else "200ms"
    pkgs = find_packages(ctx.repo)
    if not pkgs:
        ctx.notes.append("micro suite: no package has a Benchmark function, so the suite measured nothing.")
        return recs
    ctx.suite_meta.setdefault(SUITE, {})["packages"] = {m or ".": p for m, p in pkgs.items()}
    for mod, plist in pkgs.items():
        cwd = ctx.repo / mod if mod else ctx.repo
        argv = ["go", "test", "-run", "^$", "-bench", ".", "-benchmem", "-count", str(count),
                "-benchtime", benchtime, "-timeout", "30m", *plist]
        lib.load_gate(ctx, f"micro {mod or '.'}")
        before = round(lib.load1(), 2)
        lib.log(f"micro: go test -bench in {mod or '.'} for {len(plist)} packages")
        try:
            _, so, se = lib.run(argv, cwd=cwd, env=ctx.cmd_env(), timeout=3600, capture=True)
        except lib.Failure as e:
            ctx.notes.append(f"micro suite: the benchmarks of module {mod or '.'} failed: "
                             f"{str(e).splitlines()[0][:300]}")
            for p in plist:
                recs.append(lib.skipped(SUITE, f"{(mod + '/' if mod else '')}{p[2:] or '.'} (all)", "ns/op",
                                        f"go test -bench failed: {str(e)[:300]}"))
            continue
        after = round(lib.load1(), 2)
        for (pkg, name, unit), vals in parse(so + "\n" + se).items():
            better = "higher" if unit.endswith("/s") else "lower"
            recs.append(lib.record(
                SUITE, f"{short_pkg(pkg)} {name} {unit}", unit, vals, better=better,
                note=f"go test -bench -benchmem -count {count} -benchtime {benchtime}; one sample per count; "
                     f"load {before} -> {after}"))
        lib.write_results(ctx)
    return recs
