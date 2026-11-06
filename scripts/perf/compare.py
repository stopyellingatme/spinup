"""Compares two perf results and fails on a regression past a threshold.

Each record says whether lower or higher is better. The change is (new - base) / base of the
medians. A metric is worse when the change goes the wrong way by more than the threshold.
compare exits 1 when a primary metric is worse. An informational metric is shown, and it
never fails the compare.
"""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import lib  # noqa: E402
import report  # noqa: E402


def _key(rec):
    return rec["suite"], rec["metric"]


def verdict(base, new, threshold):
    """Returns (ratio, verdict, is_regression) for two records (either can be None)."""
    if base is None:
        return None, "new", False
    info = bool(base.get("informational") or (new or {}).get("informational"))
    b = base.get("median")
    n = new.get("median") if new else None
    if new is None or n is None:
        return None, "missing", False
    if b is None:
        return None, "n/a (no base)", False
    better = base.get("better", "lower")
    if b == 0:
        if n == 0:
            return 1.0, "same", False
        change = float("inf") if better == "lower" else float("-inf")
        ratio = None
    else:
        ratio = n / b
        change = (n - b) / abs(b)
    worse = change > threshold if better == "lower" else change < -threshold
    improved = change < -threshold if better == "lower" else change > threshold
    if worse:
        return ratio, "worse (info)" if info else "WORSE", not info
    if improved:
        return ratio, "better", False
    return ratio, "same", False


def compare(base_data, new_data, threshold):
    """Returns (rows, regressions). A row is (metric, base, new, ratio, verdict)."""
    base_by = {_key(r): r for r in base_data.get("records", [])}
    new_by = {_key(r): r for r in new_data.get("records", [])}
    keys = list(base_by)
    keys += [k for k in new_by if k not in base_by]
    rows, regressions = [], []
    for k in keys:
        b, n = base_by.get(k), new_by.get(k)
        ratio, v, bad = verdict(b, n, threshold)
        rec = b or n
        unit = rec.get("unit", "")
        label = f"{k[0]}/{k[1]}" + (" (info)" if rec.get("informational") else "")
        rows.append((label, unit, rec.get("better", "lower"), b.get("median") if b else None,
                     n.get("median") if n else None, ratio, v))
        if bad:
            regressions.append(label)
    return rows, regressions


def _context_lines(base_data, new_data):
    lines = []
    for name, data in (("base", base_data), ("new", new_data)):
        m = data.get("meta", {})
        r = m.get("repo", {})
        mc = m.get("machine", {})
        load = ", ".join(f"{s} {v.get('load1_before', '?')}->{v.get('load1_after', '?')}"
                         for s, v in data.get("suites", {}).items())
        lines.append(f"{name}: {r.get('short', '?')} ({r.get('branch', '?')}{', dirty' if r.get('dirty') else ''}), "
                     f"{m.get('started', '?')}, {mc.get('cpu', '?')}, {m.get('toolchain', {}).get('go', '?')}"
                     f"{', quick' if m.get('quick') else ''}; load {load or '?'}")
    bm, nm = base_data.get("meta", {}), new_data.get("meta", {})
    if bm.get("quick") != nm.get("quick"):
        lines.append("warning: one result is a quick run and the other is not, so the sample counts differ")
    if bm.get("machine", {}).get("cpu") != nm.get("machine", {}).get("cpu"):
        lines.append("warning: the results come from different CPUs, so the timings do not compare")
    return lines


def _table(rows):
    head = ("metric", "unit", "better", "base", "new", "ratio", "verdict")
    cells = [head] + [(m, u, b, report.fmt(x), report.fmt(y), f"{r:.2f}" if r is not None else "n/a", v)
                      for m, u, b, x, y, r, v in rows]
    widths = [max(len(str(c[i])) for c in cells) for i in range(len(head))]
    out = []
    for i, c in enumerate(cells):
        out.append("  ".join(str(x).ljust(widths[j]) if j in (0, 1, 2, 6) else str(x).rjust(widths[j])
                             for j, x in enumerate(c)).rstrip())
        if i == 0:
            out.append("  ".join("-" * w for w in widths))
    return out


def markdown(rows, threshold, context):
    out = ["# spinup performance compare", ""]
    out += [f"- {line}" for line in context]
    out += [f"- threshold: {threshold:.0%}", "", "| metric | unit | better | base | new | ratio | verdict |",
            "|---|---|---|---|---|---|---|"]
    for m, u, b, x, y, r, v in rows:
        out.append(f"| {report.md_cell(m)} | {u} | {b} | {report.fmt(x)} | {report.fmt(y)} | "
                   f"{f'{r:.2f}' if r is not None else 'n/a'} | {v} |")
    return "\n".join(out) + "\n"


def main(base_path, new_path, threshold=0.10, md_path=None):
    if threshold < 0:
        print(f"perf: --threshold is {threshold}, and it cannot be negative\nTry: --threshold 0.10", file=sys.stderr)
        return 2
    try:
        base_data = lib.read_results(base_path)
        new_data = lib.read_results(new_path)
    except lib.Failure as e:
        print(f"perf: {e}\nTry: {e.hint}", file=sys.stderr)
        return 2
    rows, regressions = compare(base_data, new_data, threshold)
    context = _context_lines(base_data, new_data)
    for line in context:
        print(line)
    print(f"threshold: {threshold:.0%} (a primary metric that is worse by more than this fails the compare)")
    print()
    for line in _table(rows):
        print(line)
    print()
    counts = {}
    for row in rows:
        counts[row[6]] = counts.get(row[6], 0) + 1
    print("summary: " + ", ".join(f"{v} {k}" for k, v in sorted(counts.items())))
    if md_path:
        Path(md_path).write_text(markdown(rows, threshold, context))
    sys.stdout.flush()
    if regressions:
        print(f"perf: {len(regressions)} primary metrics got worse by more than {threshold:.0%}: "
              f"{', '.join(regressions)}\nTry: run both results again on an idle machine, or find the change that "
              f"made them slower", file=sys.stderr)
        return 1
    return 0
