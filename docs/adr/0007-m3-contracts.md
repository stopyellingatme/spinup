# ADR 0007: M3 contracts (data and generators)

Status: accepted (2026-10-14)

The M3 units run in parallel. These decisions fix the contracts between them. The main loop owns `internal/gen/contract.go`. Units implement it and never edit it.

## 1. Generator contract (`internal/gen/contract.go`)

- A generator is a `gen.Generator`: a name, a short help line, optional flags, and `Run(ctx, gen.Input) (*gen.Output, error)`. It registers itself with `registry.Register` in `init()` (unit M3.0 owns `internal/gen/registry`).
- `Run` never writes to disk. It returns a `vfs.Plan` (each file has a `Class`), the post-steps to run, and up to two `Next:` lines.
- The runner (`internal/gen/run`, unit M3.0) owns everything else: `--dry-run`, `--diff`, `--force`, writing with `vfs.Apply`, lock records (class, `template: "gen/<generator>"`, hash, mode), the post-steps, and the `Next:` lines.
- Without `--force`, a generator never changes an existing file whose content differs from the plan, except files that the plan marks `managed` and that the lock records with `template: "gen/..."` (generator-owned files such as `routes_gen.go`).

## 2. Post-steps

| Post-step | Command (in `api/`) |
|---|---|
| `sqlc` | `go tool -modfile=tools.mod sqlc generate` |
| `openapi` | bundle `api/openapi/` into `api/openapi/bundle.gen.yaml` (in Go, unit M3.3), then `go tool -modfile=tools.mod oapi-codegen -config oapi-codegen.yaml openapi/bundle.gen.yaml` |
| `gofmt` | `gofmt -w` on the planned `.go` files |

Tool outputs (`api/internal/store/*.go`, `api/internal/gen/api.gen.go`, `api/openapi/bundle.gen.yaml`) are not in the lock. They are rebuilt from their inputs. Commit them to git.

## 3. Generated API layout (additions to ADR 0005 §2)

- `api/sqlc.yaml` (managed, unit M3.2): engine postgresql, `schema: db/migrations`, `queries: db/queries`, Go package `store` in `internal/store`, `sql_package: pgx/v5`, JSON tags, an interface, and these overrides: `timestamptz` and `date` to `time.Time` (a pointer when nullable), `uuid` to `github.com/google/uuid.UUID` (the type oapi-codegen uses for `format: uuid`, so the store and API types match), `numeric` to `string`.
- `api/db/queries/` (scaffold `.sql` files, one per resource). sqlc fails with no query, so an app has no store package until its first resource. `spinup db sqlc` on such an app says so, with `Try: spinup g resource <name> <field:type>...`.
- OpenAPI (critic resolution 5): `api/openapi/openapi.yaml` (scaffold, the user's root: info, servers, security, shared components). Generators write one file per resource: `api/openapi/paths/<plural>.yaml` (a map from path to path item) and `api/openapi/schemas/<singular>.yaml` (a map from schema name to schema). The bundler merges the root with every `paths/*.yaml` and `schemas/*.yaml` (sorted, a duplicate path or schema name is an error) and rewrites `$ref`s to `#/components/schemas/<Name>`. Generators never touch `openapi.yaml`.
- `api/oapi-codegen.yaml` (managed, unit M3.3): package `gen`, output `internal/gen/api.gen.go`, `std-http-server`, `strict-server`, and `models`. No embedded spec (it would add kin-openapi). The app depends on `github.com/oapi-codegen/runtime` only.
- Handlers: one `Server` struct in `api/internal/handlers/server.go` (scaffold, written by the first resource) with `Queries *store.Queries` and `DB *pgxpool.Pool`. Each resource adds `api/internal/handlers/<plural>.go` (scaffold) with the strict-server methods. Errors map to problem details with kit `httpx`.
- `api/internal/routes/routes_gen.go` (managed, generator-owned): once the app has a resource, it registers `gen.HandlerFromMux(gen.NewStrictHandlerWithOptions(server, nil, options), mux)`. Before that, it stays empty (M1 shape).
- Routes live under `/api/<plural>` (`/api/todos`, `/api/todos/{id}`).

## 4. Migrations and the database commands

- File names are goose sequential: `NNNNN_<snake_name>.sql`, the next number is the highest existing number plus one (`internal/db/ops.NextMigrationName`, unit M3.2). `spinup db new`, `g migration`, and `g resource` all use it.
- `spinup db migrate|status|redo|to` run the app binary (ADR 0005 §1). Kit `db.MigrateCommand` gains `redo`, `to <version>`, and `version` (unit M3.2).
- `db psql`, `db dump`, `db restore`, `db seed`, `db reset`, and `db doctor` reach the development database through `docker compose -f deploy/compose.dev.yml exec postgres ...`. `db reset` refuses any environment other than local development and calls `project.ConfirmProtected` when `--env` names a protected one.

## 5. Jobs

Kit `jobs` wraps River (`riverqueue/river` with `riverpgxv5`). The `jobs` pack adds `api/cmd/worker/main.go` and the River migrations run in the same `migrate` step, after the app migrations.

## 6. Lockfiles

`go.mod.tmpl` and `go.sum` cover every module of every pack (a superset): the main loop runs `bump-stack` in app mode with all modules turned on. Unused requirements do not break `go build -mod=readonly`.
