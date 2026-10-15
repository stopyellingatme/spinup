"""Check Tempo responses for the trace e2e test (run.sh).

Usage:
  check_trace.py ids <search.json>
      Print the trace IDs of a Tempo search response, one in each line.
  check_trace.py link <trace.json> <trace-id> <web-service> <api-service>
      Exit 0 when the trace holds a span of <api-service> whose parent is a span of
      <web-service>, and print a summary. Exit 1 when it does not, and print why.

Tempo returns OTLP JSON. The /api/v2/traces endpoint wraps it in {"trace": ...}. The span IDs
can be base64 or hex, but one response uses one encoding, so the script compares them as strings.
"""

import json
import sys


def load(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def search_ids(doc):
    return [t["traceID"] for t in doc.get("traces") or [] if t.get("traceID")]


def resource_spans(doc):
    if "trace" in doc:
        doc = doc["trace"]
    return doc.get("resourceSpans") or doc.get("batches") or []


def service_name(resource):
    for attr in (resource or {}).get("attributes") or []:
        if attr.get("key") == "service.name":
            return (attr.get("value") or {}).get("stringValue", "")
    return ""


def spans(doc):
    """Yield (service, span) for each span in the trace."""
    for rs in resource_spans(doc):
        service = service_name(rs.get("resource"))
        scopes = rs.get("scopeSpans") or rs.get("instrumentationLibrarySpans") or []
        for scope in scopes:
            for span in scope.get("spans") or []:
                yield service, span


def link(doc, trace_id, web, api):
    all_spans = list(spans(doc))
    by_id = {s.get("spanId"): (svc, s) for svc, s in all_spans}
    web_spans = [s for svc, s in all_spans if svc == web]
    api_spans = [s for svc, s in all_spans if svc == api]
    trace_ids = {s.get("traceId") for _, s in all_spans}
    if len(trace_ids) != 1:
        return False, f"the spans carry {len(trace_ids)} trace IDs, want 1"
    if not web_spans:
        return False, f"no span of service {web!r}"
    if not api_spans:
        return False, f"no span of service {api!r}"
    for span in api_spans:
        parent = by_id.get(span.get("parentSpanId"))
        if parent and parent[0] == web:
            return True, (
                f"trace {trace_id}: {len(all_spans)} spans "
                f"({len(web_spans)} {web}, {len(api_spans)} {api}); "
                f"{api} span {span.get('name')!r} is a child of "
                f"{web} span {parent[1].get('name')!r}"
            )
    return False, f"no {api!r} span has a parent span in {web!r}"


def main(argv):
    if len(argv) >= 2 and argv[0] == "ids":
        for trace_id in search_ids(load(argv[1])):
            print(trace_id)
        return 0
    if len(argv) == 5 and argv[0] == "link":
        ok, message = link(load(argv[1]), argv[2], argv[3], argv[4])
        print(message)
        return 0 if ok else 1
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
