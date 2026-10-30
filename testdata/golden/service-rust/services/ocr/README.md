# ocr

This Rust service belongs to todo.
It uses axum and tokio.

Run the service from the application root with `spinup dev`.
Generate the Go client with `spinup g client` when an OpenAPI file is declared.

Run these commands in this directory for local development:

```sh
cargo test --locked
cargo run --locked
```

The service listens on port 8080.
Set `PORT` to change the port.
`GET /healthz` returns `ok`.
`POST /v1/sum` accepts `{"values": [1, 2.5]}` and returns `{"sum": 3.5}`.
Run `service healthcheck` to test readiness inside the container.

Keep the package name `service` to preserve the lockfile and the Docker build.
