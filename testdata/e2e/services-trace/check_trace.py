"""Check the parent links of Rust and Python spans in a Tempo response.

Usage: check_trace.py link <trace.json> <trace-id> <parent-id> <service> [<service> ...]

Exit 0 and print each parent link when all services have a matching span.
Exit 1 and print a reason when a parent link is missing.
"""

import base64
import binascii
import json
import sys


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


def hex_id(value, size):
    """Normalize a Tempo hex or base64 ID to lowercase hex."""
    if not isinstance(value, str):
        return ""
    try:
        if len(value) == size * 2:
            return bytes.fromhex(value).hex()
        decoded = base64.b64decode(value, validate=True)
        return decoded.hex() if len(decoded) == size else ""
    except (ValueError, binascii.Error):
        return ""


def link(doc, trace_id, parent_id, services):
    all_spans = list(spans(doc))
    if not all_spans:
        return False, "the trace has no spans"
    if any(hex_id(span.get("traceId"), 16) != trace_id for _, span in all_spans):
        return False, f"the response contains a span outside trace {trace_id}"
    messages = []
    for service in services:
        service_spans = [span for name, span in all_spans if name == service]
        if not service_spans:
            return False, f"no span of service {service!r}"
        for span in service_spans:
            span_id = hex_id(span.get("spanId"), 8)
            if span_id and hex_id(span.get("parentSpanId"), 8) == parent_id:
                messages.append(
                    f"trace {trace_id}: {service} span {span_id} "
                    f"({span.get('name')!r}) has parent {parent_id}"
                )
                break
        else:
            return False, f"no {service!r} span has parent {parent_id}"
    return True, "\n".join(messages)


def main(argv):
    if len(argv) < 5 or argv[0] != "link":
        print(__doc__, file=sys.stderr)
        return 2
    trace_id, parent_id = argv[2:4]
    if (len(trace_id) != 32 or hex_id(trace_id, 16) != trace_id or not int(trace_id, 16)
            or len(parent_id) != 16 or hex_id(parent_id, 8) != parent_id or not int(parent_id, 16)):
        print("the expected trace and parent IDs must be nonzero lowercase hex")
        return 1
    try:
        with open(argv[1], encoding="utf-8") as source:
            doc = json.load(source)
        ok, message = link(doc, trace_id, parent_id, argv[4:])
    except (OSError, ValueError, TypeError, AttributeError) as error:
        print(f"cannot check the Tempo response: {error}")
        return 1
    print(message)
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
