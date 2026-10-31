"""Tests of OpenTelemetry configuration."""

from fastapi import FastAPI
from fastapi.testclient import TestClient
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
from opentelemetry.trace import SpanKind

from service import telemetry


def test_fastapi_span_uses_traceparent():
    app = FastAPI()
    exporter = InMemorySpanExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    FastAPIInstrumentor.instrument_app(app, tracer_provider=provider)

    @app.get("/items")
    def items() -> dict[str, bool]:
        return {"ok": True}

    with TestClient(app) as client:
        response = client.get(
            "/items",
            headers={
                "traceparent": (
                    "00-4bf92f3577b34da6a3ce929d0e0e4736-"
                    "00f067aa0ba902b7-01"
                ),
            },
        )

    assert response.status_code == 200
    server_span = next(
        span for span in exporter.get_finished_spans() if span.kind is SpanKind.SERVER
    )
    assert server_span.context.trace_id == 0x4BF92F3577B34DA6A3CE929D0E0E4736
    assert server_span.parent is not None
    assert server_span.parent.span_id == 0x00F067AA0BA902B7


def test_setup_without_endpoint(monkeypatch):
    monkeypatch.delenv("OTEL_EXPORTER_OTLP_ENDPOINT", raising=False)

    assert telemetry.setup(FastAPI()) is None
