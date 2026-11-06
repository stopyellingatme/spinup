#!/usr/bin/env python3
"""The spinup performance loop.

  python3 -I scripts/perf/perf.py run [--suites cli,app,micro] [--quick] [--out <file>]
  python3 -I scripts/perf/perf.py compare <base.json> <new.json> [--threshold 0.10] [--md <file>]
  python3 -I scripts/perf/perf.py report <result.json> [--md <file>]

`run` builds the CLI and the load generator once, then runs the suites in the order cli, app,
micro. It writes the JSON after each suite (and after each phase of the app suite), so partial
results survive a crash or Ctrl-C. A suite that fails keeps its partial records, the next
suite still runs, and `run` exits 1. The result goes to perf/results/<timestamp>.json by
default, with the Markdown table next to it. The whole process runs at nice 10, and every go
command runs with GOFLAGS=-p=4. Python 3 standard library only.

`compare` prints one table and exits 1 when a primary metric got worse by more than the
threshold (a fraction: 0.10 is 10 percent).
"""

import argparse
import importlib
import os
import sys
import time
import traceback
from pathlib import Path

sys.dont_write_bytecode = True  # keep __pycache__ out of the repository
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import lib  # noqa: E402

SUITE_ORDER = ["cli", "app", "micro"]


def parse_args(argv):
    ap = argparse.ArgumentParser(prog="perf.py", description="Measure spinup, and compare two measurements.")
    sub = ap.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("run", help="build the CLI and run the suites")
    r.add_argument("--suites", default=",".join(SUITE_ORDER), help="comma list of cli,app,micro (default all)")
    r.add_argument("--quick", action="store_true", help="fewest samples and short load runs")
    r.add_argument("--out", default=None,
                   help="the JSON result file (default perf/results/<timestamp>.json in the repository)")
    r.add_argument("--database-url", default=None,
                   help=f"the Postgres server for the app and cli suites (default $DATABASE_URL or "
                        f"{lib.DEFAULT_DATABASE_URL})")
    r.add_argument("--load-limit", type=float, default=None,
                   help=f"wait while load1 is at or above this (default 45 percent of the cores: "
                        f"{lib.default_load_limit()})")
    r.add_argument("--gate-max-s", type=float, default=None,
                   help=f"longest load wait for one series (default {lib.GATE_MAX_S}, quick {lib.GATE_MAX_S_QUICK})")
    r.add_argument("--gate-budget-s", type=float, default=None,
                   help=f"longest total load wait of the run (default {lib.GATE_BUDGET_S}, quick "
                        f"{lib.GATE_BUDGET_S_QUICK}); after it the gates pass at once")
    r.add_argument("--min-free-gb", type=float, default=None, help="free disk needed to start (0 skips the check)")
    c = sub.add_parser("compare", help="compare two results")
    c.add_argument("base")
    c.add_argument("new")
    c.add_argument("--threshold", type=float, default=0.10, help="the allowed regression, a fraction (default 0.10)")
    c.add_argument("--md", default=None, help="also write the table as Markdown to this file")
    p = sub.add_parser("report", help="turn a result JSON into Markdown")
    p.add_argument("json")
    p.add_argument("--md", default=None, help="write the Markdown to this file (it is also printed)")
    return ap.parse_args(argv)


def cmd_run(a):
    suites = [s.strip() for s in a.suites.split(",") if s.strip()]
    bad = [s for s in suites if s not in SUITE_ORDER]
    if bad or not suites:
        print(f"perf: unknown suite {bad[0] if bad else '(none)'!r}\nTry: --suites cli,app,micro", file=sys.stderr)
        return 2
    suites = [s for s in SUITE_ORDER if s in suites]
    out = Path(a.out) if a.out else lib.REPO / "perf" / "results" / f"{lib.stamp()}.json"
    try:
        os.nice(lib.NICE)
    except OSError:
        pass
    try:
        ctx = lib.Ctx(lib.REPO, out, quick=a.quick, load_limit=a.load_limit, gate_max_s=a.gate_max_s,
                      gate_budget_s=a.gate_budget_s, database_url=a.database_url)
    except lib.Failure as e:
        print(f"perf: {e}\nTry: {e.hint}", file=sys.stderr)
        return 1
    lib.install_cleanup(ctx)
    rc = 0
    try:
        lib.collect_meta(ctx, suites, args_repr=" ".join(sys.argv[1:]))
        lib.check_disk(ctx, suites, a.min_free_gb)
        lib.build_tools(ctx)
        lib.write_results(ctx)
        for name in suites:
            mod = importlib.import_module(f"suite_{name}")
            meta = ctx.suite_meta.setdefault(name, {})
            meta.update({"uptime_before": lib.uptime(), "load1_before": round(lib.load1(), 2)})
            t = time.monotonic()
            ctx.suite_records()
            failed_before = len(ctx.failures)
            lib.log(f"suite {name}: start")
            try:
                mod.run(ctx)
                if len(ctx.failures) > failed_before:
                    meta["error"] = "series stopped on a failed sample: " + "; ".join(ctx.failures[failed_before:])
                    rc = 1
            except lib.Failure as e:
                traceback.print_exc()
                meta["error"] = str(e)
                rc = 1
            except Exception as e:  # keep the partial records and run the next suite
                traceback.print_exc()
                meta["error"] = f"{type(e).__name__}: {e}"
                rc = 1
            except BaseException:  # Ctrl-C and SIGTERM: save what the suite measured, then stop
                meta["error"] = "interrupted"
                ctx.end_suite()
                lib.write_results(ctx)
                raise
            meta.update({"uptime_after": lib.uptime(), "load1_after": round(lib.load1(), 2),
                         "duration_s": round(time.monotonic() - t, 1)})
            count = len(ctx.live)
            ctx.end_suite()
            lib.write_results(ctx)
            lib.log(f"suite {name}: done in {meta['duration_s']} s, {count} records"
                    + (f", ERROR: {meta['error'].splitlines()[0][:200]}" if meta.get("error") else ""))
    except lib.Failure as e:
        print(f"perf: {e}\nTry: {e.hint}", file=sys.stderr)
        rc = 1
    except KeyboardInterrupt:
        print("perf: interrupted", file=sys.stderr)
        rc = 130
    except Exception:
        traceback.print_exc()
        rc = 1
    finally:
        try:
            ctx.meta["finished"] = lib.now_iso()
            lib.write_results(ctx)
            import report
            report.main(str(ctx.out), str(ctx.out.with_suffix(".md")), echo=False)
        except Exception:
            traceback.print_exc()
        ctx.cleanup()
    print(f"perf: results in {ctx.out} and {ctx.out.with_suffix('.md')}", file=sys.stderr)
    if rc == 1:
        print("perf: one or more suites failed (see the errors above)\n"
              "Try: read the suite error in the Markdown result, then run that suite again", file=sys.stderr)
    return rc


def main(argv):
    a = parse_args(argv)
    if a.cmd == "compare":
        import compare
        return compare.main(a.base, a.new, a.threshold, a.md)
    if a.cmd == "report":
        import report
        return report.main(a.json, a.md)
    return cmd_run(a)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
