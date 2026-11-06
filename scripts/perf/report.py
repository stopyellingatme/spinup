"""Turns a perf result JSON into Markdown (one table per suite)."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import lib  # noqa: E402

SUITE_TITLES = {"cli": "CLI", "app": "Generated app", "micro": "Go benchmarks"}


def fmt(x):
    if x is None:
        return "n/a"
    if abs(x) >= 1000:
        return f"{x:,.0f}"
    if abs(x) >= 100:
        return f"{x:.0f}"
    if abs(x) >= 10:
        return f"{x:.1f}"
    if abs(x) >= 0.01 or x == 0:
        return f"{x:.2f}"
    return f"{x:.4f}"


def md_cell(s):
    return str(s).replace("|", "\\|").replace("\n", " ")


def build(data):
    meta = data.get("meta", {})
    out = []
    p = out.append
    p("# spinup performance result")
    p("")
    m = meta.get("machine", {})
    t = meta.get("toolchain", {})
    r = meta.get("repo", {})
    p(f"- Started: {meta.get('started', '?')}; finished: {meta.get('finished', 'not finished')}")
    p(f"- Machine: {m.get('cpu', '?')}, {m.get('cores', '?')} cores, {m.get('ram', '?')}, {m.get('os', '?')}")
    p(f"- Go: {t.get('go', '?')}; GOFLAGS: `{t.get('goflags', '')}`; {t.get('nice', '')}")
    p(f"- Repository: {r.get('short', '?')} ({r.get('branch', '?')}{', dirty' if r.get('dirty') else ''})")
    sc = meta.get("scenario", {})
    p(f"- Scenario: {sc.get('resources', '?')} resources; cli suite `new --api-only --modules "
      f"{sc.get('cli_modules', '?')}`; app suite `new --modules {sc.get('app_modules', '?')}`"
      f"{'; quick mode' if meta.get('quick') else ''}")
    p(f"- Load gate: wait while load1 >= {meta.get('load_limit', '?')} (at most {meta.get('gate_max_s', '?')} s "
      f"for one series, {meta.get('gate_budget_s', '?')} s for the run)")
    p("")
    records = data.get("records", [])
    suites = []
    for rec in records:
        if rec["suite"] not in suites:
            suites.append(rec["suite"])
    for suite in suites:
        sm = data.get("suites", {}).get(suite, {})
        p(f"## {SUITE_TITLES.get(suite, suite)} suite")
        p("")
        p(f"Load before: {sm.get('load1_before', '?')}, after: {sm.get('load1_after', '?')}"
          + (f"; duration {sm.get('duration_s')} s" if sm.get("duration_s") is not None else ""))
        if sm.get("error"):
            p("")
            p(f"**Suite error:** {md_cell(sm['error'])}")
        p("")
        p("| metric | unit | better | median | p90 | n |")
        p("|---|---|---|---|---|---|")
        notes = []
        for rec in (x for x in records if x["suite"] == suite):
            label = rec["metric"] + (" (info)" if rec.get("informational") else "")
            p(f"| {md_cell(label)} | {rec.get('unit', '')} | {rec.get('better', 'lower')} | {fmt(rec.get('median'))} "
              f"| {fmt(rec.get('p90')) if rec.get('n', 0) > 1 else ''} | {rec.get('n', 0)} |")
            if rec.get("note") and suite != "micro":
                notes.append(f"- **{md_cell(rec['metric'])}**: {md_cell(rec['note'])}")
            if rec.get("informational_reason"):
                notes.append(f"- **{md_cell(rec['metric'])}** is informational: {md_cell(rec['informational_reason'])}")
        p("")
        if notes:
            p("<details><summary>Notes</summary>")
            p("")
            out.extend(notes)
            p("")
            p("</details>")
            p("")
    gaps = [x for x in records if not x.get("samples")]
    if gaps:
        p("## Not measured")
        p("")
        for x in gaps:
            p(f"- {x['suite']}/{md_cell(x['metric'])}: {md_cell(x.get('note') or 'no samples')}")
        p("")
    timeouts = [g for g in data.get("gate_events", []) if g.get("timed_out")]
    p("## Run notes")
    p("")
    p(f"- Load gate: {len(data.get('gate_events', []))} gates, {len(timeouts)} timed out "
      f"(the series then ran under load).")
    for n in data.get("notes", []):
        p(f"- {md_cell(n)}")
    p("")
    return "\n".join(out)


def main(json_path, md_path=None, echo=True):
    """Writes the Markdown of json_path to md_path (when given) and prints it (when echo)."""
    try:
        data = lib.read_results(json_path)
    except lib.Failure as e:
        print(f"perf: {e}\nTry: {e.hint}", file=sys.stderr)
        return 1
    text = build(data)
    if md_path:
        Path(md_path).write_text(text + "\n")
    if echo:
        print(text)
    return 0
