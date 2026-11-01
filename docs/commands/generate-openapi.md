---
title: generate openapi — rebuild the Go server code
parent: Command guides
---

# generate openapi — rebuild the Go server code

The OpenAPI files in `api/openapi/` describe your API.
`spinup g openapi` bundles them into one file and regenerates the Go server code from it.
Run it after you edit an OpenAPI file by hand.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g resource todo title:string
Created 8, updated 1 files
Next: spinup db migrate up
Next: cd api && go test ./...
$ spinup g openapi
Created 0, updated 0 files
Next: cd api && go build ./...
```

The command takes no arguments.
`spinup g resource` already runs it, so the second run here changes nothing.

## What it writes

| File | What it is | Commit it? |
| --- | --- | --- |
| `api/openapi/bundle.gen.yaml` | All fragments merged into one document, with sorted keys. | Yes |
| `api/internal/gen/api.gen.go` | The Go models, the router, and the strict server interface. | Yes |

The bundle is also embedded in the app and shown on the [API reference page](../api-docs.md).
Both files are build output: the lock does not track them, and the command rebuilds them from their inputs.
The app needs the `oapi-codegen` runtime package, which the generated `go.mod` already lists.

## How the files fit together

`api/openapi/openapi.yaml` is the root document.
It holds the title, the version, the servers, and the shared `Problem` schema.
The `Problem` schema describes every error body of the API, including `request_id` and the field errors.
A new app has no paths in it.

Each resource adds two fragment files:

| Fragment | Contents |
| --- | --- |
| `api/openapi/paths/<plural>.yaml` | A map from each URL path to its operations. |
| `api/openapi/schemas/<singular>.yaml` | A map from each schema name to its schema. |

The bundler reads every `.yaml` file in these two folders, in file name order, and merges them into the root document.

## Add your own endpoint

Drop a new file in `paths/`.
Add a file in `schemas/` too if the endpoint needs a new type.
This file adds `GET /api/ping`:

```yaml
# api/openapi/paths/ping.yaml
/api/ping:
  get:
    operationId: ping
    summary: Check that the API answers
    responses:
      "200":
        description: The API is up.
```

Save the file, then run the command:

```console
$ printf '/api/ping:\n  get:\n    operationId: ping\n    summary: Check that the API answers\n    responses:\n      "200":\n        description: The API is up.\n' > api/openapi/paths/ping.yaml
$ spinup g openapi
Created 0, updated 0 files
Next: cd api && go build ./...
$ grep -c 'Ping(ctx context.Context' api/internal/gen/api.gen.go
1
```

The Go build now fails with `missing method Ping` until you add the handler to the `Server` type.
Write it in `api/internal/handlers/`:

```go
func (s *Server) Ping(ctx context.Context, req gen.PingRequestObject) (gen.PingResponseObject, error) {
	return gen.Ping200Response{}, nil
}
```

For a response body, add a schema in `schemas/` and point to it with a `$ref`:

```yaml
# api/openapi/schemas/pong.yaml
Pong:
  type: object
  required: [message]
  properties:
    message:
      type: string
```

In the path file, set `schema: {$ref: "#/components/schemas/Pong"}` under `content: application/json:`.

## Rules for fragments

- Start every `$ref` with `#/components/`, such as `#/components/schemas/Pong`. It must resolve inside the bundle.
- Use a path or a schema name only once. A duplicate stops the command and names both files.
- Put paths in `paths/` and schemas in `schemas/`.

```console
$ cp api/openapi/paths/ping.yaml api/openapi/paths/ping2.yaml
$ spinup g openapi
Error: generator post-step "openapi" failed: cannot bundle OpenAPI: duplicate paths key "/api/ping" in openapi/paths/ping.yaml and openapi/paths/ping2.yaml
...
[exit 1]
$ rm api/openapi/paths/ping2.yaml
```

The `Try:` line of this error suggests running `oapi-codegen` by hand.
Ignore it for a duplicate key. Remove the duplicate and run the command again.
For other failures, the by-hand command runs in `api/`: `go tool -modfile=tools.mod oapi-codegen -config oapi-codegen.yaml openapi/bundle.gen.yaml`.

## Preview

`--dry-run` and `--diff` print nothing and run no tool, because the command writes no file of its own.

```console
$ spinup g openapi --dry-run
```

## If there are no paths

An API with no routes has nothing to generate.
The command still writes the bundle and says so:

```console
$ spinup new empty --api-only --module example.com/empty --no-git --non-interactive
...
$ cd empty
$ spinup g openapi
no OpenAPI paths; oapi-codegen skipped
Try: spinup g resource <name> <field:type>...
Created 0, updated 0 files
Next: cd api && go build ./...
$ test -s api/openapi/bundle.gen.yaml
$ test ! -e api/internal/gen/api.gen.go
```

## What to commit

Commit `bundle.gen.yaml` and `api.gen.go` with the fragments that produced them.
The compile step in CI then does not need to regenerate them.
The root document `openapi.yaml` is yours to edit.
The files that configure the generator (`oapi-codegen.yaml` and `api/internal/gen/generate.go`) are managed, so do not edit them.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `duplicate paths key` | Two fragments define the same path. Remove one. |
| `go build` says `missing method` | A new operation needs a handler. Add the method to `Server`. |
| A `$ref` does not resolve | Use `#/components/schemas/<Name>` and check the spelling. |

## See also

- [generate client](generate-client.md): the TypeScript side of the same bundle
- [generate resource](generate-resource.md): writes the fragments for you
- [API reference page](../api-docs.md)
- Flag reference: [`generate openapi`](../site/commands/generate-openapi.md)
