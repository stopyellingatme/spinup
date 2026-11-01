---
title: Test your app
parent: Build your app
nav_order: 13
---

# Test your app

A generated app comes with tests for the API, the web app, and each sidecar service.
`spinup test` runs the API tests. The web and service tests have their own commands.
The generated CI runs all of them for you.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup test --dry-run
go test ./...
```

Run `spinup test` without `--dry-run` to run the tests.

## Run the API tests

`spinup test` runs `go test ./...` in `api/`.
It streams the output and returns the exit code of `go test`.

```console
$ spinup test --run Todo --race --cover --dry-run
go test -run Todo -race -cover ./...
```

| Flag | What it does | Example |
| --- | --- | --- |
| `--run <regexp>` | Runs only the matching tests | `spinup test --run TestTodosCRUD` |
| `--race` | Turns on the race detector | `spinup test --race` |
| `--cover` | Reports test coverage | `spinup test --cover` |

The flags `--web`, `--e2e`, and `--contract` are not available yet.
They stop with an error. Use `pnpm test` and `pnpm test:e2e` in `web/` instead.

## The test database

Tests that use the database need PostgreSQL.
`kit/testkit` finds a server in this order:

1. If `DATABASE_URL` is set, it uses that server. The role must be allowed to create databases.
2. Otherwise it starts one PostgreSQL container with Docker.
3. If neither works, the test is skipped. When `CI` is set, the test fails instead.

Each test gets its own new database, and testkit drops it when the test ends.
So tests do not affect each other, and `t.Parallel()` is safe.

The `.env.example` file sets `DATABASE_URL` for the development database.
Tests create separate databases on the same server, so they do not touch your data.

<!-- doctest: skip -->
```console
$ docker compose -f deploy/compose.dev.yml up -d postgres
$ DATABASE_URL=postgres://app:app@localhost:5432/app?sslmode=disable spinup test
```

## Write a handler test

`spinup g resource` writes `api/internal/handlers/todos_test.go`.
It tests create, read, update, list, and delete against a real database, and it checks bad input.
Copy its pattern for your own handlers:

```go
func TestTodoList(t *testing.T) {
	t.Parallel()
	pool := testkit.DBWithMigrations(t, migrations.FS)
	handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
		routes.Register(mux, routes.Deps{DB: pool})
	})).Handler()

	rec := testkit.Do(t, handler, http.MethodGet, "/api/todos", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	list := testkit.DecodeJSON[gen.TodoList](t, rec)
	if list.Total != 0 {
		t.Fatalf("got %d items, want 0", list.Total)
	}
}
```

A test that fails before any query runs needs no database.
The generated bad-input test passes `routes.Deps{}` with no `DB` for that reason.

The helpers are in `kit/testkit` and `kit/auth/authtest`:

| Helper | What it does |
| --- | --- |
| `testkit.DB(t)` | A pool on a new, empty database |
| `testkit.DBWithMigrations(t, migrations.FS)` | A pool on a database with your migrations applied. It is fast, because it clones a template. |
| `testkit.Do(t, handler, method, path, body)` | Sends a request to a handler and returns the recorder |
| `testkit.DecodeJSON[T](t, rec)` | Reads the JSON body of a response |
| `authtest.As(handler, userID)` | Runs the handler as a signed-in user |
| `authtest.AsRole(handler, userID, roles.Manager)` | Runs the handler as a user with a role. `roles` is the package `api/internal/roles` of your app. |

To test a route that needs a signed-in user, wrap the handler with `authtest`.
The wrapper skips the sign-in step, so you need no cookie:

```go
handler = authtest.AsRole(handler, 1, roles.Manager) // act as user 1 with the role manager
```

If the table of your resource refers to `users`, insert a user row first.
The generated tests for an owned resource do this with `INSERT INTO users`.

## Run the web tests

The web app uses [Vitest](https://vitest.dev) for unit tests and `svelte-check` for types.
Run these in `web/`:

<!-- doctest: skip -->
```console
$ pnpm check
$ pnpm test
$ pnpm test -- --watch
```

Put a test next to the code, in a file that ends with `.test.ts`.
A new app without the `auth` module has no web tests. `pnpm test` passes in that case.

## Run the browser tests

Playwright tests are in `web/e2e/`.
The smoke test opens the home page.
With the `auth` module, a second test registers a user, signs out, signs in, and reloads.

Start the stack in one terminal, then run the tests in another:

<!-- doctest: skip -->
```console
$ spinup dev
$ cd web && pnpm exec playwright install chromium
$ pnpm test:e2e
```

Playwright opens `http://localhost:5173`.
If the web app runs at another URL, set `PUBLIC_ORIGIN`. An embed app listens on port 8080:

<!-- doctest: skip -->
```console
$ PUBLIC_ORIGIN=http://localhost:8080 pnpm test:e2e
```

With bun, replace `pnpm test:e2e` with `bun run test:e2e`, and `pnpm exec` with `bunx`.
Add your tests as `web/e2e/*.spec.ts` files. The smoke test is a seed file, so you can change it.

```ts
import { expect, test } from '@playwright/test';

test('the about page loads', async ({ page }) => {
	await page.goto('/about');
	await expect(page.getByRole('heading', { name: 'About' })).toBeVisible();
});
```

## Test a service

Each sidecar service has its own tests in `services/<name>/`.
`spinup ci test-service` runs them in Docker with the toolchain of the service language.
Use `--dry-run` to see the Dockerfile without Docker.

```console
$ spinup g service ocr --lang rust
...
$ spinup ci test-service ocr --dry-run
...
WORKDIR /app
COPY . .
RUN cargo test --locked
```

See [Sidecar services](../services.md) for the languages.

## Tests in CI

The `ci` module writes the workflows. Run `spinup ci generate` if you added it later.

| Workflow | What it runs |
| --- | --- |
| `ci.yml`, job `api` | `go mod verify`, `go build`, `go vet`, and `go test -race` with a PostgreSQL service |
| `ci.yml`, job `web` | `pnpm check` and `pnpm test` (with bun, the `bun` commands) |
| `ci.yml`, job `spinup` | `spinup check` and `spinup doctor` |
| `ci.yml`, job `services` | `spinup ci test-service` for each service |
| `e2e.yml` | On each pull request, builds the stack, then runs the Playwright tests |

The `api` job sets `DATABASE_URL` and `CI=true`, so a database test that cannot find a server fails and does not skip.
See [Generated CI](../ci.md) for the details.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| A database test is skipped | No `DATABASE_URL` and no Docker | Set `DATABASE_URL`, or start Docker |
| `permission denied to create database` | The role cannot create databases | Use a role with `CREATEDB` |
| `cannot run web tests: this target is unavailable` | You passed `--web` | Run `pnpm test` in `web/` |
| `pnpm test` finds no tests | The app has none yet | Add a `.test.ts` file |

## See also

- [test](../commands/test.md) and the [flag reference](../site/commands/test.md)
- [The web app](../web.md)
- [Sidecar services](../services.md)
- [Generated CI](../ci.md)
- [ci](../commands/ci.md)
