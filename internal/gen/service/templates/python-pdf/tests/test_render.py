"""Tests of the render path: the PDF, the limits, the fetcher, and the logs."""

import asyncio
import io
import json
import logging
import os
import re
import socket
import subprocess
import sys
import time
from pathlib import Path

import httpx
import pytest
from fastapi.testclient import TestClient
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
from pypdf import PdfReader
from weasyprint.urls import FatalURLFetchingError

from service.fetcher import AssetFetcher
from service.limits import APP_DIR, ASSETS_DIR, TEMPLATES_DIR, Limits
from service.main import create_app
from service.render import render_html

NOSOCKET_DIR = Path(__file__).resolve().parent / "nosocket"
SECRET = "c0ffee-secret-marker"


def fake_child(code: str) -> list[str]:
    """Return a child command that runs code instead of a render."""
    return [sys.executable, "-c", code]


def pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


@pytest.fixture
def probe_template():
    """Write a template that loads the URLs of data, and remove it after the test."""
    path = TEMPLATES_DIR / "zz_fetch_probe.html.j2"
    path.write_text(
        "<!doctype html><html><head>"
        "{% if data.css is defined %}<link rel=\"stylesheet\" href=\"{{ data.css }}\">{% endif %}"
        "</head><body><p>probe</p>"
        "{% if data.img is defined %}<img src=\"{{ data.img }}\">{% endif %}"
        "</body></html>\n"
    )
    yield "zz_fetch_probe"
    path.unlink()


@pytest.fixture
def listener():
    """Start a TCP listener on 127.0.0.1 that records no traffic by itself."""
    server = socket.create_server(("127.0.0.1", 0))
    server.settimeout(0.5)
    yield server
    server.close()


def assert_no_connection(server: socket.socket) -> None:
    with pytest.raises(TimeoutError):
        connection, _ = server.accept()
        connection.close()


# 7.1: the sample renders to a real PDF.


def test_sample_pdf_has_page_and_text():
    client = TestClient(create_app())
    response = client.post("/v1/render", json={"template": "sample", "data": {
        "title": "March report",
        "rows": [{"label": "Visits", "value": 12}],
    }})
    assert response.status_code == 200, response.text
    assert response.content.startswith(b"%PDF-")
    reader = PdfReader(io.BytesIO(response.content))
    assert len(reader.pages) >= 1
    text = reader.pages[0].extract_text()
    assert "March report" in text
    assert "Visits" in text


# 7.2: the limits.


def test_timeout_kills_child(tmp_path):
    pid_file = tmp_path / "pid"
    code = f"import os, time; open({str(pid_file)!r}, 'w').write(str(os.getpid())); time.sleep(60)"
    client = TestClient(create_app(Limits(timeout_seconds=1), command=fake_child(code)))
    started = time.monotonic()
    response = client.post("/v1/render", json={"template": "sample", "data": {"title": "t"}})
    assert response.status_code == 504
    assert time.monotonic() - started < 10
    assert not pid_alive(int(pid_file.read_text()))


def test_oversize_output_kills_child(tmp_path):
    pid_file = tmp_path / "pid"
    code = (
        f"import os, sys, time; open({str(pid_file)!r}, 'w').write(str(os.getpid())); "
        "sys.stdout.buffer.write(b'%PDF-' + b'x' * (21 * 1024 * 1024)); sys.stdout.flush(); time.sleep(60)"
    )
    client = TestClient(create_app(Limits(timeout_seconds=30), command=fake_child(code)))
    started = time.monotonic()
    response = client.post("/v1/render", json={"template": "sample", "data": {"title": "t"}})
    assert response.status_code == 500
    assert time.monotonic() - started < 20
    assert not pid_alive(int(pid_file.read_text()))


def test_child_without_pdf_fails():
    client = TestClient(create_app(command=fake_child("print('<html>')")))
    response = client.post("/v1/render", json={"template": "sample", "data": {"title": "t"}})
    assert response.status_code == 500


def test_full_queue_answers_503():
    code = "import sys, time; time.sleep(1.5); sys.stdout.buffer.write(b'%PDF-1.7 fake')"
    app = create_app(Limits(concurrency=1, queue_max=1, timeout_seconds=15), command=fake_child(code))

    async def run() -> list[httpx.Response]:
        transport = httpx.ASGITransport(app=app)
        async with httpx.AsyncClient(transport=transport, base_url="http://service") as client:
            body = {"template": "sample", "data": {"title": "t"}}
            return await asyncio.gather(*(client.post("/v1/render", json=body) for _ in range(3)))

    responses = asyncio.run(run())
    statuses = sorted(response.status_code for response in responses)
    assert statuses == [200, 200, 503]
    full = next(response for response in responses if response.status_code == 503)
    assert full.headers["retry-after"] == "5"
    assert full.headers["content-type"] == "application/problem+json"


def test_waiting_request_times_out():
    code = "import sys, time; time.sleep(3); sys.stdout.buffer.write(b'%PDF-1.7 fake')"
    app = create_app(Limits(concurrency=1, queue_max=1, timeout_seconds=1.5), command=fake_child(code))

    async def run() -> list[httpx.Response]:
        transport = httpx.ASGITransport(app=app)
        async with httpx.AsyncClient(transport=transport, base_url="http://service") as client:
            body = {"template": "sample", "data": {"title": "t"}}
            return await asyncio.gather(*(client.post("/v1/render", json=body) for _ in range(2)))

    assert [response.status_code for response in asyncio.run(run())] == [504, 504]


def test_limits_from_env():
    limits = Limits.from_env({"RENDER_MAX_BODY_BYTES": "2048", "RENDER_QUEUE_MAX": "0", "RENDER_TIMEOUT_SECONDS": "2.5"})
    assert limits.max_body_bytes == 2048
    assert limits.queue_max == 0
    assert limits.timeout_seconds == 2.5
    assert limits.concurrency == 2
    for env in ({"RENDER_CONCURRENCY": "0"}, {"RENDER_MAX_BODY_BYTES": "lots"}):
        with pytest.raises(ValueError, match="Try:"):
            Limits.from_env(env)


# 7.3 and 7.4: no fetch of an external URL.


@pytest.mark.parametrize(
    "url",
    [
        "http://127.0.0.1:9/x.png",
        "https://example.com/x.css",
        "HTTP://example.com/x.png",
        "ftp://example.com/x.png",
        "file:///etc/passwd",
        "FILE:///etc/passwd",
        "file://example.com/etc/passwd",
        "file://localhost/etc/passwd",
        ASSETS_DIR.as_uri() + "/../templates/sample.html.j2",
        ASSETS_DIR.as_uri() + "/%2e%2e/templates/sample.html.j2",
        ASSETS_DIR.as_uri() + "/missing.css",
        "javascript:alert(1)",
        "relative.png",
    ],
)
def test_fetcher_blocks_without_socket(url, monkeypatch):
    def no_socket(*args, **kwargs):
        raise AssertionError("the fetcher opened a socket")

    monkeypatch.setattr(socket, "socket", no_socket)
    monkeypatch.setattr(socket, "create_connection", no_socket)
    with pytest.raises(FatalURLFetchingError):
        AssetFetcher().fetch(url)


def test_fetcher_allows_assets_and_data():
    fetcher = AssetFetcher()
    response = fetcher.fetch(ASSETS_DIR.as_uri() + "/report.css")
    try:
        assert b"@page" in response.read()
    finally:
        response.close()
    response = fetcher.fetch("data:text/plain;base64,aGVsbG8=")
    assert response.read() == b"hello"


def test_fetcher_blocks_symlink_out_of_assets(tmp_path):
    assets = tmp_path / "assets"
    assets.mkdir()
    (assets / "link.css").symlink_to("/etc/passwd")
    with pytest.raises(FatalURLFetchingError):
        AssetFetcher(assets).fetch((assets / "link.css").as_uri())


@pytest.mark.parametrize("keys", [("css",), ("img",), ("css", "img")])
def test_render_with_external_url_fails_without_connection(keys, probe_template, listener):
    port = listener.getsockname()[1]
    data = {key: f"http://127.0.0.1:{port}/x.{key}" for key in keys}
    client = TestClient(create_app())
    response = client.post("/v1/render", json={"template": probe_template, "data": data})
    assert response.status_code == 500
    assert response.headers["content-type"] == "application/problem+json"
    assert_no_connection(listener)


def test_render_with_file_url_fails(probe_template):
    client = TestClient(create_app())
    response = client.post("/v1/render", json={"template": probe_template, "data": {"img": "file:///etc/passwd"}})
    assert response.status_code == 500


def test_child_renders_without_socket():
    env = {**os.environ, "PYTHONPATH": str(NOSOCKET_DIR)}
    guard = subprocess.run(
        [sys.executable, "-c", "import socket; socket.socket()"],
        env=env, cwd=APP_DIR, capture_output=True, timeout=60,
    )
    assert guard.returncode != 0
    assert b"socket blocked" in guard.stderr

    request = json.dumps({"template": "sample", "data": {"title": "No socket"}}).encode()
    child = subprocess.run(
        [sys.executable, "-m", "service.render_job"],
        input=request, env=env, cwd=APP_DIR, capture_output=True, timeout=60,
    )
    assert child.returncode == 0, child.stderr
    assert child.stdout.startswith(b"%PDF-")


# 7.5: escaping.


def test_data_is_escaped():
    html = render_html("sample", {"title": "<script>alert(1)</script>", "notes": "\"><img src=x>"})
    assert "<script>" not in html
    assert "&lt;script&gt;" in html
    assert "<img src=x>" not in html


def test_templates_do_not_use_safe():
    pattern = re.compile(r"\|\s*safe\b|\bmarkupsafe\b|autoescape\s+false", re.IGNORECASE)
    templates = sorted(TEMPLATES_DIR.rglob("*.j2"))
    assert templates
    for path in templates:
        assert not pattern.search(path.read_text()), f"{path.name} turns off escaping"


# 7.6: the logs hold no request data.


def test_logs_hold_no_data(caplog, capfd, probe_template):
    caplog.set_level(logging.DEBUG)
    client = TestClient(create_app())
    requests = [
        ({"template": "sample", "data": {"title": SECRET}}, 200),
        ({"template": "sample", "data": {"other": SECRET}}, 422),
        ({"template": probe_template, "data": {"img": f"http://127.0.0.1:9/{SECRET}.png"}}, 500),
        ({"template": "sample", "data": {"title": SECRET, "rows": SECRET}}, 422),
    ]
    for body, status in requests:
        response = client.post("/v1/render", json=body)
        assert response.status_code == status, response.text
        assert SECRET not in json.dumps(response.headers.multi_items())
    assert "render template=sample status=200" in caplog.text
    captured = capfd.readouterr()
    assert SECRET not in caplog.text
    assert SECRET not in captured.out
    assert SECRET not in captured.err


# Decision 9: the pdf.render span.


def test_render_span():
    exporter = InMemorySpanExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    client = TestClient(create_app(tracer_provider=provider))
    response = client.post("/v1/render", json={"template": "sample", "data": {"title": "Span"}})
    assert response.status_code == 200
    span = next(span for span in exporter.get_finished_spans() if span.name == "pdf.render")
    assert span.attributes["pdf.template"] == "sample"
    assert span.attributes["pdf.bytes"] == len(response.content)
    assert span.attributes["pdf.render_ms"] >= 0


def test_stalled_body_times_out_and_frees_the_queue():
    """A client that never finishes its body gets 408 and gives its queue place back."""
    app = create_app(Limits(concurrency=1, queue_max=0, timeout_seconds=0.5), command=fake_child("pass"))
    sent: list[dict] = []

    async def scenario() -> None:
        chunks = [{"type": "http.request", "body": b'{"templ', "more_body": True}]

        async def receive() -> dict:
            if chunks:
                return chunks.pop()
            await asyncio.sleep(30)
            return {"type": "http.disconnect"}

        async def send(message: dict) -> None:
            sent.append(message)

        scope = {
            "type": "http", "asgi": {"version": "3.0"}, "http_version": "1.1", "method": "POST",
            "scheme": "http", "path": "/v1/render", "raw_path": b"/v1/render", "query_string": b"",
            "headers": [(b"content-type", b"application/json"), (b"content-length", b"100")],
            "client": ("127.0.0.1", 1), "server": ("127.0.0.1", 80),
        }
        await asyncio.wait_for(app(scope, receive, send), timeout=10)

    asyncio.run(scenario())
    start = next(m for m in sent if m["type"] == "http.response.start")
    assert start["status"] == 408
    assert app.state.renderer.pending == 0
