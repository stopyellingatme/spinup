---
title: routes — list the HTTP routes
parent: Command guides
---

# routes — list the HTTP routes

`spinup routes` prints every HTTP route that your API serves: the method, the path, and where the route comes from.
Use it to check that a new resource or handler is wired in.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup routes
METHOD  PATH      SOURCE
GET     /healthz  kit
GET     /readyz   kit
GET     /version  kit
```

The command sorts routes by path, then by method.
It reads your source files and does not start the API. Use `--dir <app>` to run it from another folder.

## Add a resource and look again

Generate a resource and the table grows. The `SOURCE` of each new route names its OpenAPI operation.
`--api-only` skips the web pages:

```console
$ spinup generate resource todo title:string --api-only
...
$ spinup routes
METHOD  PATH             SOURCE
GET     /api/todos       openapi:listTodos
POST    /api/todos       openapi:createTodo
DELETE  /api/todos/{id}  openapi:deleteTodo
GET     /api/todos/{id}  openapi:getTodo
PUT     /api/todos/{id}  openapi:updateTodo
GET     /healthz         kit
GET     /readyz          kit
GET     /version         kit
```

## Get JSON

Use `--json` for a script. The output is one array on one line. Each item has `method`, `path`, and `source`.

```console
$ spinup routes --json | cut -c1-66
[{"method":"GET","path":"/api/todos","source":"openapi:listTodos"}
```

## Where routes come from

| Source | Meaning |
| --- | --- |
| `kit` | The runtime library serves the route: `/healthz`, `/readyz`, and `/version`. |
| `openapi:<operationId>` | An operation in `api/openapi/bundle.gen.yaml`. |
| `<file>:<line>` | A `mux.Handle` or `mux.HandleFunc` call in `api/internal/routes/*.go`. |

For a route in a Go file, the method is `*` when the pattern names no method.

The command cannot see every route. It skips test files, and it skips a pattern that is not a string constant.
If a route is missing, check how you registered it.

## See also

- [`generate resource`](generate-resource.md) adds the routes of a CRUD API.
- [API reference page](../api-docs.md) shows the same API as a web page.
- Flag reference: [`routes`](../site/commands/routes.md)
