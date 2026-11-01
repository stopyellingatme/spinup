"""Tests of the HTTP routes of the PDF service."""

import pytest
from fastapi.testclient import TestClient

from service.limits import Limits
from service.main import app, create_app

client = TestClient(app)


def test_healthz():
    response = client.get("/healthz")
    assert response.status_code == 200
    assert response.text == "ok"


def test_render_sample():
    response = client.post("/v1/render", json={"template": "sample", "data": {"title": "March report"}})
    assert response.status_code == 200, response.text
    assert response.headers["content-type"] == "application/pdf"
    assert "content-disposition" not in response.headers
    assert response.content.startswith(b"%PDF-")


def test_unknown_template():
    response = client.post("/v1/render", json={"template": "missing", "data": {}})
    assert response.status_code == 404
    assert response.headers["content-type"] == "application/problem+json"


def test_missing_data_key():
    response = client.post("/v1/render", json={"template": "sample", "data": {}})
    assert response.status_code == 422
    assert response.headers["content-type"] == "application/problem+json"
    assert "title" in response.json()["detail"]


@pytest.mark.parametrize(
    "body",
    [
        b"not json",
        b"[]",
        b'{"template": "sample"}',
        b'{"data": {}}',
        b'{"template": "sample", "data": [], "extra": 1}',
        b'{"template": "sample", "data": "text"}',
        b'{"template": 7, "data": {}}',
        b'{"template": "../sample", "data": {}}',
        b'{"template": "Sample", "data": {}}',
        b'{"template": "sample.html", "data": {}}',
        b'{"template": "' + b"a" * 65 + b'", "data": {}}',
    ],
)
def test_bad_body(body):
    response = client.post("/v1/render", content=body, headers={"content-type": "application/json"})
    assert response.status_code == 422, response.text
    assert response.headers["content-type"] == "application/problem+json"


def test_body_over_limit():
    small = TestClient(create_app(Limits(max_body_bytes=1024)))
    body = {"template": "sample", "data": {"title": "x" * 2048}}
    response = small.post("/v1/render", json=body)
    assert response.status_code == 413
    assert response.headers["content-type"] == "application/problem+json"


def test_streamed_body_over_limit():
    small = TestClient(create_app(Limits(max_body_bytes=1024)))

    def chunks():
        yield b'{"template": "sample", "data": {"title": "'
        for _ in range(8):
            yield b"x" * 256
        yield b'"}}'

    response = small.post("/v1/render", content=chunks(), headers={"content-type": "application/json"})
    assert response.status_code == 413
