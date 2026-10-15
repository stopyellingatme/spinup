# todo

Stack: Go API, PostgreSQL, Docker Compose, and Mailpit.

## Quick start

Start the development services:

```sh
docker compose -f deploy/compose.dev.yml up -d
spinup db migrate up
cd api && go run ./cmd/api
```

In another terminal, check the API:

```sh
curl localhost:8080/healthz
```
