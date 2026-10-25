#!/usr/bin/env python3
"""Spike r0-13: prints the tables of the result file from measurements/*.json."""

import json
import statistics
import sys
from pathlib import Path

M = Path(__file__).resolve().parent.parent / "measurements"


# du -sk counts KiB. Every GB in the tables is 10^9 bytes (KiB * 1024 / 1e9).
KIB = 1024 / 1e9


def gb(kb):
    return f"{kb * KIB:.2f}"


def peak_gb(r):
    """The peak of the sampled dirs plus ~/.cargo/registry (not sampled: no
    build here writes it), in GB."""
    reg = sum(r.get("registry_home_kb", {}).values())
    return r["peaks"]["peak_total_gb"] * 1e6 * KIB + reg * KIB


def short(p):
    parts = Path(p).parts
    return "/".join(parts[-2:]) if parts[-1] == "target" else parts[-1]


def run_table(names):
    print("| Run | Lines | Jobs (lane/gate/tests) | Wall | Peak du + registry | load1 max | Swap baseline | Swap increase | Free-space drop | Samples |")
    print("|---|---:|---|---:|---:|---:|---:|---:|---:|---:|")
    for n in names:
        f = M / f"run-{n}.json"
        if not f.exists():
            continue
        r = json.loads(f.read_text())
        p = r["peaks"]
        j = r.get("jobs") or {}
        jobs = f"{j.get('lane') or 4}/{j.get('gate') or 4}/{j.get('test_threads') or 10}"
        lines = "53k" if r.get("lines_arg") == 47000 else "27k"
        print(
            f"| {n} | {lines} | {jobs} | {r['wall_s']:.0f} s | {peak_gb(r):.2f} GB | {p['load1_max']:.2f} | "
            f"{p['swap_baseline_mb'] / 1024:.2f} GiB | {p['swap_increase_mb'] / 1024:.2f} GiB | {p['free_drop_gb']:.2f} GB | {p['samples']} |"
        )
    print()
    print("| Run | Lane A cold | Lane A warm median (n) | Lane B cold | Lane B warm median (n) | Gate cold | Gate warm | uptime at start | uptime at end |")
    print("|---|---:|---:|---:|---:|---:|---:|---|---|")
    for n in names:
        f = M / f"run-{n}.json"
        if not f.exists():
            continue
        r = json.loads(f.read_text())
        la, lb = r["lanes"]["lane-a"], r["lanes"]["lane-b"]

        def med(lane):
            w = [x["secs"] for x in lane["warm"]]
            return f"{statistics.median(w):.1f} s ({len(w)})"

        g = r["gate"]["passes"]
        print(
            f"| {n} | {la['cold_s']:.0f} s | {med(la)} | {lb['cold_s']:.0f} s | {med(lb)} | {g[0]['secs']:.0f} s | {g[1]['secs']:.0f} s | "
            f"{r['uptime_start'].split('load averages: ')[1]} | {r['uptime_end'].split('load averages: ')[1]} |"
        )
    print()
    print("| Run | Gate step (cold pass) | Seconds |")
    print("|---|---|---:|")
    for n in names:
        f = M / f"run-{n}.json"
        if not f.exists():
            continue
        r = json.loads(f.read_text())
        steps = [s for s in r["gate"]["steps"] if "-cold-" in s["step"]]
        print(f"| {n} | " + ", ".join(f"{s['step'].split('-cold-')[1]} {s['secs']:.0f}" for s in steps) + " | " + f"{sum(s['secs'] for s in steps):.0f} |")
    print()
    print("| Run | Final du per dir (GB) | clone s | sccache Rust hits / misses | rc != 0 |")
    print("|---|---|---|---|---|")
    for n in names:
        f = M / f"run-{n}.json"
        if not f.exists():
            continue
        r = json.loads(f.read_text())
        fd = ", ".join(f"{short(k)} {gb(v)}" for k, v in r["final_du_kb"].items())
        st = r.get("sccache_stats", {}).get("stats", {})
        sc = "-"
        if st:
            sc = f"{st.get('cache_hits', {}).get('counts', {}).get('Rust', 0)} / {st.get('cache_misses', {}).get('counts', {}).get('Rust', 0)}"
        bad = [s["step"] for s in r["gate"]["steps"] + r["lanes"]["lane-a"]["steps"] + r["lanes"]["lane-b"]["steps"] if s["rc"]]
        print(f"| {n} | {fd} | {r.get('clone_s') or '-'} | {sc} | {len(bad)} |")
    print()
    r = json.loads((M / f"run-{names[0]}.json").read_text())
    print(f"test binaries: lane A {r['lanes']['lane-a']['test_binaries']}, lane B {r['lanes']['lane-b']['test_binaries']}, gate per pass {r['gate']['passes'][0]['test_binaries_built']}, image {r['gate']['image_bytes']} bytes")


def main():
    names = sys.argv[1:] or [
        "plain", "plain-paced", "sccache-cold", "sccache-warm", "sccache-bd-cold", "sccache-bd-warm",
        "sccache-slot-cold", "sccache-slot-warm", "clone", "clone2", "clone-53k", "clone-53k-r2",
        "plain-throttled", "plain-throttled-53k", "clone-53k-throttled", "clone-53k-throttled-r2",
    ]
    run_table(names)
    for extra in ["seed-22000", "seed-47000", "pathkey", "appprofile", "single-27k", "single-53k"]:
        f = M / f"{extra}.json"
        if f.exists():
            print(f"\n## {extra}\n")
            print(json.dumps(json.loads(f.read_text()), indent=1)[:4000])


if __name__ == "__main__":
    main()
