# ocr

This Python service belongs to todo.
It uses FastAPI and uvicorn.

Run the service from the application root with `spinup dev`.
Generate the Go client with `spinup g client` when an OpenAPI file is declared.

Run these commands in this directory for local development:

```sh
uv sync --locked
uv run --locked python -m pytest
uv run --locked uvicorn service.main:app --host 0.0.0.0 --port 8080
```

The container listens on port 8080.
If you change its port, change the Dockerfile command and `PORT` together.
`GET /healthz` returns `ok`.
`POST /v1/sum` accepts `{"values": [1, 2.5]}` and returns `{"sum": 3.5}`.
Run `python -m service.healthcheck` to test readiness inside the container.

## Tracing

Set `OTEL_EXPORTER_OTLP_ENDPOINT` to export traces with OTLP over HTTP.
The service reads `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` through the OpenTelemetry SDK.
The FastAPI instrumentation accepts the incoming W3C `traceparent` header.
It does not trace `GET /healthz`.

Keep the project name `service` to preserve the lockfile.
