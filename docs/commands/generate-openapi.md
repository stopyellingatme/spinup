# Generate OpenAPI code

Run `spinup g openapi` from the application root to bundle the OpenAPI files and generate Go code.
The command also accepts the name `spinup generate openapi`.
It takes no positional arguments.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g openapi --dry-run
$ test ! -e api/openapi/bundle.gen.yaml
$ spinup g openapi
no OpenAPI paths; oapi-codegen skipped
Try: spinup g resource <name> <field:type>...
Created 0, updated 0 files
Next: cd api && go build ./...
$ test -s api/openapi/bundle.gen.yaml
$ test ! -e api/internal/gen/api.gen.go
```

The root document is `api/openapi/openapi.yaml`.
It contains the application title, version, servers, and shared components.
Its `Problem` schema describes the response from kit `httpx.WriteProblem`, including `request_id` and field errors.
The root document starts with no paths.

Each resource adds two files:

| File | Contents |
| --- | --- |
| `api/openapi/paths/<plural>.yaml` | A map from each URL path to its path item. |
| `api/openapi/schemas/<singular>.yaml` | A map from each schema name to its schema. |

The bundler reads fragment files with the `.yaml` extension in filename order.
It merges paths and schemas into the root document.
Duplicate paths or schema names cause an error that names both files.
Every `$ref` must start with `#/components/` and resolve within the bundle.
Use `#/components/schemas/<Name>` for schema references, including references between fragments.

The command writes `api/openapi/bundle.gen.yaml` with sorted keys and a generated-file header.
If the bundle has no paths, it skips oapi-codegen.
The command writes the skip notice to stderr.
Otherwise, it runs this command inside `api/`:

```sh
go tool -modfile=tools.mod oapi-codegen -config oapi-codegen.yaml openapi/bundle.gen.yaml
```

The configuration generates models, a standard HTTP server, and a strict server interface in `api/internal/gen/api.gen.go`.
It retains unused schemas and does not embed the specification.
The generated application requires the oapi-codegen runtime package.

Commit the bundle and generated Go code.
The lock excludes these tool outputs because the command rebuilds them from their inputs.
Run `spinup g openapi` after editing the root document or a fragment.
This command bundles the files before code generation.
The directives in `api/internal/gen/generate.go` require an existing bundle and SQL queries.

The root document has the `scaffold` class.
The configuration and generation directives have the `managed` class.
The fragment directories contain seed `.gitkeep` files.

Use `--dry-run` or `--diff` to preview without writing or running tools.
Use `--json` for structured results.
After generation, run `cd api && go build ./...`.
