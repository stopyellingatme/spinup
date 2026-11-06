---
title: generate client — rebuild the API clients
parent: Command guides
---

# generate client — rebuild the API clients

`spinup g client` rebuilds the typed clients that call your Go API and your sidecar services.
Run it after you change an OpenAPI file, so the web app and the Go code agree with the API.

It does three jobs in order:

1. It bundles the OpenAPI files and regenerates the Go server code (the same as [`spinup g openapi`](generate-openapi.md)).
2. It generates the TypeScript types for the web app.
3. It generates a Go client for each sidecar service that has an OpenAPI file.

The generator takes no arguments.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g service ocr --lang python
Created 12, updated 1 files
Next: spinup g client
Next: spinup dev
$ spinup g client
no OpenAPI paths; oapi-codegen skipped
Try: spinup g resource <name> <field:type>...
client generation skipped: no web pack
Created 0, updated 0 files
Next: cd web && pnpm check
$ find api/internal/services -type f | sort
api/internal/services/ocr/client.gen.go
api/internal/services/ocr/oapi-codegen.yaml
api/internal/services/ocr/ocr.go
```

This app has no resources and no web app, so the first two jobs skip with a note.
The third job still wrote the client of the `ocr` service.

## When to run it

Run `spinup g client` in these cases:

- You edited a file in `api/openapi/`, or you ran a command that changed it.
- You added or changed the `openapi.yaml` of a sidecar service.
- `spinup doctor` warns that the client is stale.

`spinup g resource` and `spinup g service` already run the parts they need.

## The web client

In an app with a web app, the command writes `web/src/lib/api/schema.d.ts`.
The pages import their types from this file.

<!-- doctest: skip -->
```console
$ spinup g client
Created 0, updated 0 files
Next: cd web && pnpm check
$ cd web && pnpm check
```

If `web/node_modules` is missing, the command runs `pnpm install --frozen-lockfile` first.
If you chose the heyapi client for the web app, the output goes to `web/src/lib/api/client` instead.
See [Package manager and API client](../web-variants.md).

Commit the file together with the OpenAPI bundle.
The lock does not track it, because the command rebuilds it from its inputs.

## Service clients

A service that declares `openapi = "openapi.yaml"` in `spinup.toml` gets a Go client in `api/internal/services/<name>/`:

| File | Class | What it is |
| --- | --- | --- |
| `oapi-codegen.yaml` | managed | The generator configuration. |
| `client.gen.go` | managed | The typed client and the models. |
| `<name>.go` | scaffold | `Config` and `New`. Edit it. The command never replaces it. |

`New` reads the service address from `SERVICE_<NAME>_URL` and adds tracing to every call.
If the OpenAPI file of the service is missing, the command fails with a `Try:` line.
See [Sidecar services](../services.md) for how the API calls a service.

## Preview

`--dry-run` and `--diff` print nothing and run no tool, because the command writes no file of its own.

```console
$ spinup g client --dry-run
$ spinup g client --json 2>/dev/null | grep -o '"post_steps":\[[^]]*\]'
"post_steps":["openapi","client","service-clients"]
```

## If a step is skipped

The command prints a reason on stderr and goes on:

| Message | Meaning |
| --- | --- |
| `no OpenAPI paths; oapi-codegen skipped` | The API has no routes yet. Run `spinup g resource`. |
| `client generation skipped: no web pack` | The app was made with `--api-only`. |
| `client generation skipped: api/openapi/bundle.gen.yaml is missing` | The bundle step did not run. Run `spinup g openapi`. |

If pnpm is not installed, the command fails with a `Try:` line.

## The drift check

`spinup doctor` includes a `client-drift` check.
It builds the types into a temporary file and compares them with `schema.d.ts`.
It never changes your files.

<!-- doctest: skip -->
```console
$ spinup doctor
...
warn	client-drift	web/src/lib/api/schema.d.ts is stale
Try: spinup g client
```

The check warns when the types differ or the file is missing.
It skips outside a project, without the web app, without a bundle, without `web/node_modules`, and in `spinup doctor --quick`.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `pnpm check` reports type errors after an API change | Run `spinup g client`, then fix the code that uses the changed type. |
| `client-drift` warns | Run `spinup g client` and commit the result. |
| The command fails on a service | Check the `openapi` path of the service in `spinup.toml`. |

## See also

- [generate openapi](generate-openapi.md): the Go side of the same bundle
- [generate service](generate-service.md) and [Sidecar services](../services.md)
- [The web app](../web.md)
- [doctor](doctor.md): the `client-drift` check
- Flag reference: [`generate client`](../site/commands/generate-client.md)
