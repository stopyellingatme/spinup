#!/usr/bin/env python3
"""Checks for testdata/e2e/monitoring/run.sh.

Each subcommand reads JSON (or docker output) from a file and exits 0 when the check passes.
It prints one line that says what it found. It exits 1 when the check fails.

  targets FILE              every active target in the Prometheus /api/v1/targets response is up
  up FILE JOB...            the Prometheus query response for `up` has the value 1 for each JOB
  loki FILE MARKER          the Loki query_range response has a log line that contains MARKER
  tempo FILE TRACE SPAN     the Tempo trace response holds the span SPAN of the trace TRACE
  memory FILE BUDGET_MIB    the `docker stats` samples in FILE stay under BUDGET_MIB
"""

import base64
import json
import sys

UNITS = {
    "B": 1,
    "KiB": 1024,
    "MiB": 1024**2,
    "GiB": 1024**3,
    "TiB": 1024**4,
    "kB": 1000,
    "KB": 1000,
    "MB": 1000**2,
    "GB": 1000**3,
    "TB": 1000**4,
}


def load(path):
    with open(path, encoding="utf-8") as handle:
        return json.load(handle)


def fail(message):
    print(message)
    sys.exit(1)


def targets(path):
    data = load(path).get("data", {})
    active = data.get("activeTargets", [])
    if not active:
        fail("Prometheus has no active target yet")
    down = [
        f"{t.get('labels', {}).get('job', '?')} {t.get('scrapeUrl', '?')} health={t.get('health')} {t.get('lastError', '')}".strip()
        for t in active
        if t.get("health") != "up"
    ]
    if down:
        fail("targets not up: " + "; ".join(down))
    jobs = sorted({t.get("labels", {}).get("job", "?") for t in active})
    print(f"{len(active)} Prometheus target(s) up: {', '.join(jobs)}")


def up(path, jobs):
    result = load(path).get("data", {}).get("result", [])
    values = {}
    for series in result:
        job = series.get("metric", {}).get("job", "")
        value = series.get("value", [0, "0"])[1]
        # One job can have more than one instance. Every instance must be up.
        values[job] = values.get(job, True) and value == "1"
    missing = [job for job in jobs if job not in values]
    down = [job for job in jobs if job in values and not values[job]]
    if missing or down:
        problems = []
        if missing:
            problems.append("no up series for " + ", ".join(missing))
        if down:
            problems.append("up is 0 for " + ", ".join(down))
        seen = ", ".join(f"{job}={'1' if ok else '0'}" for job, ok in sorted(values.items()))
        fail("; ".join(problems) + f" (seen: {seen or 'nothing'})")
    print(f"up == 1 for {', '.join(jobs)}")


def loki(path, marker):
    for stream in load(path).get("data", {}).get("result", []):
        for entry in stream.get("values", []):
            if len(entry) > 1 and marker in entry[1]:
                labels = ",".join(f"{k}={v}" for k, v in sorted(stream.get("stream", {}).items()))
                print(f"Loki has the log line {marker} ({labels})")
                return
    fail(f"Loki has no log line with {marker} yet")


def normalize_id(value):
    """Tempo returns IDs as hex or as base64. Return lowercase hex."""
    if not value:
        return ""
    text = str(value)
    try:
        int(text, 16)
        return text.lower()
    except ValueError:
        pass
    try:
        return base64.b64decode(text).hex()
    except (ValueError, TypeError):
        return text.lower()


def spans(node):
    """Yield every span object in a Tempo v1 or v2 trace response."""
    if isinstance(node, dict):
        if "spanId" in node and "name" in node:
            yield node
        for value in node.values():
            yield from spans(value)
    elif isinstance(node, list):
        for value in node:
            yield from spans(value)


def tempo(path, trace_id, span_id):
    data = load(path)
    for span in spans(data):
        if normalize_id(span.get("spanId")) == span_id.lower():
            got = normalize_id(span.get("traceId"))
            if got and got.lstrip("0") != trace_id.lower().lstrip("0"):
                fail(f"span {span_id} has the trace ID {got}, not {trace_id}")
            print(f"Tempo has the span {span_id} ({span.get('name')}) of the trace {trace_id}")
            return
    fail(f"Tempo has no span {span_id} in the trace {trace_id} yet")


def parse_size(text):
    text = text.strip()
    for unit in sorted(UNITS, key=len, reverse=True):
        if text.endswith(unit):
            return float(text[: -len(unit)]) * UNITS[unit]
    return float(text)


def memory(path, budget_mib):
    """FILE holds `docker stats --no-stream` samples. A line `--` separates two samples."""
    samples = [[]]
    with open(path, encoding="utf-8") as handle:
        for line in handle:
            line = line.rstrip("\n")
            if line == "--":
                samples.append([])
            elif line.strip():
                name, usage = line.split("\t", 1)
                used = parse_size(usage.split("/")[0])
                samples[-1].append((name, used))
    samples = [s for s in samples if s]
    if not samples:
        fail("docker stats returned no container")
    totals = [sum(used for _, used in sample) for sample in samples]
    worst = max(range(len(samples)), key=lambda i: totals[i])
    for name, used in sorted(samples[worst]):
        print(f"  {name:<45} {used / 1024**2:8.1f} MiB")
    total_mib = totals[worst] / 1024**2
    budget = float(budget_mib)
    line = f"total {total_mib:.1f} MiB (the highest of {len(samples)} samples), budget {budget:.0f} MiB"
    if total_mib >= budget:
        fail("over budget: " + line)
    print(line)


def main(argv):
    if len(argv) < 3:
        print(__doc__, file=sys.stderr)
        return 2
    command, path, args = argv[1], argv[2], argv[3:]
    if command == "targets":
        targets(path)
    elif command == "up":
        up(path, args)
    elif command == "loki":
        loki(path, args[0])
    elif command == "tempo":
        tempo(path, args[0], args[1])
    elif command == "memory":
        memory(path, args[0])
    else:
        print(f"check.py: unknown command {command}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
