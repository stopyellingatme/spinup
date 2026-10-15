# List HTTP routes

`spinup routes` lists the HTTP methods, paths, and sources.
The table has `METHOD`, `PATH`, and `SOURCE` columns.
The command sorts entries by path, then method.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup routes --json
[{"method":"GET","path":"/healthz","source":"kit"},{"method":"GET","path":"/readyz","source":"kit"},{"method":"GET","path":"/version","source":"kit"}]
```

The kit supplies `/healthz`, `/readyz`, and `/version`.
OpenAPI operations come from `api/openapi/bundle.gen.yaml`.
Their source is `openapi:<operationId>`.

The command also reads `mux.Handle` and `mux.HandleFunc` registrations in `api/internal/routes/*.go`.
The pattern must be a string constant.
Their source is the file name and line number.
The method is `*` when the pattern has no method.
The command ignores test files and dynamic patterns.

Use `--json` for a JSON array with `method`, `path`, and `source` fields.
Use `--dir <directory>` to select a project.
