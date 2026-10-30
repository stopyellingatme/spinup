"""Tests of the HTTP routes."""

from fastapi.testclient import TestClient

from service.main import app

client = TestClient(app)


def test_healthz():
    response = client.get("/healthz")
    assert response.status_code == 200
    assert response.text == "ok"


def test_sum():
    response = client.post("/v1/sum", json={"values": [1, 2.5, -0.5]})
    assert response.status_code == 200
    assert response.json() == {"sum": 3.0}


def test_sum_requires_values():
    assert client.post("/v1/sum", json={}).status_code == 422
