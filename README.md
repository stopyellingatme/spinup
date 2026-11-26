# spinup

**Create a full-stack Go and SvelteKit app, deploy it to one VM, and merge template upgrades with your edits.**

[![CI](https://github.com/stopyellingatme/spinup/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/stopyellingatme/spinup/actions/workflows/ci.yml)
[![Docs](https://github.com/stopyellingatme/spinup/actions/workflows/docs.yml/badge.svg?branch=main)][site]
[![Go Reference](https://pkg.go.dev/badge/github.com/stopyellingatme/spinup/kit.svg)](https://pkg.go.dev/github.com/stopyellingatme/spinup/kit)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go 1.27+](https://img.shields.io/badge/go-1.27%2B-00ADD8.svg)](go.mod)

[Get started][install] |
[Guides](#documentation) |
[CLI reference][cli] |
[Performance](#performance) |
[Changelog](CHANGELOG.md)

spinup is a command line tool for one fixed stack: a Go API, a SvelteKit web app, and PostgreSQL.
`spinup new` writes the app as plain code that you own.
Generators add tables, REST routes, pages, jobs, and mailers.
`spinup deploy` puts the app on one VM with Docker Compose and Caddy, and switches traffic with no downtime.
Optional modules add sign-in, backups, and monitoring with Grafana.
`spinup.lock` records each file that spinup wrote, so `spinup upgrade` can merge new templates with your edits.

Visit [stopyellingatme.github.io/spinup][site] for the tutorial, the guides, and a reference page for each command.

## Highlights

- **Plain code that you own.** The API uses `net/http`, `sqlc`, and `goose`, with no ORM and no custom DSL.
  A generated app does not need spinup to run.
- **One line for a CRUD resource.** `spinup g resource` writes the migration, the SQL queries, the OpenAPI
  files, the handlers, a test, and the web pages.
- **One contract for the API and the web app.** The API serves the routes of its OpenAPI files. The web app
  calls them through a TypeScript client that spinup generates from the same files.
- **Upgrades that keep your edits.** `spinup upgrade` works on a git branch in a separate worktree. It merges
  the old template, the new template, and your file, then builds and tests the result.
- **A deploy with no downtime.** Each deploy builds images and runs the migrations. Then it starts the new
  version next to the old one. When the new version is healthy, Caddy sends traffic to it.
- **16 modules, each one optional.** If you need a feature, add its module. Modules exist for sign-in with
  roles, mail, background jobs, an audit trail, feature flags, API keys, Stripe billing, live data, and an MCP
  server.
- **Secure defaults.** The kit hashes passwords with argon2id. It limits the rate of sign-in requests. It rejects
  unknown fields in a JSON body. Each page of the web app has a Content-Security-Policy header.
- **Operations in the same tool.** Secrets stay encrypted in git with sops and age. Backups use pgBackRest by
  default. Monitoring runs Grafana, Prometheus, Loki, and Alloy next to the app.
- **Services in other languages.** A sidecar service in Rust, Python, or Node runs in its own container next
  to the API. A Python service renders PDF reports.
- **Guidance for AI agents.** Each new app has an `AGENTS.md` file with the generators, the file classes, and
  a finish checklist. The docs publish an [`llms.txt`][llms] index.
- **No telemetry.** The CLI sends no telemetry. In a terminal, it asks GitHub for a new release at most once a
  day, and `SPINUP_NO_UPDATE_CHECK=1` stops that request.

## Quick start

```sh
git clone https://github.com/stopyellingatme/spinup.git
make -C spinup install                 # builds spinup/bin/spinup from source
export PATH="$PWD/spinup/bin:$PATH"
spinup new todo --module example.com/todo
cd todo
spinup dev                             # app http://localhost:5173, API http://localhost:8080
```

The first three lines build spinup from source. The build needs Go 1.27 or later, git, and `make`, and a full
clone with no local changes. `spinup dev` also needs Node 24 or later, pnpm through corepack, and Docker.
[Install spinup][install] lists each tool and its version.
`make install` saves the templates of this build and prints the `SPINUP_TEMPLATES_DIR` value for a later upgrade.

`spinup new` asks a few questions and writes the app. Add `--non-interactive` to skip the questions.
`spinup dev` installs the web packages, starts PostgreSQL and Mailpit in Docker, applies the migrations, and
starts the API and the web app. [Your first app][first-app] shows each step with its output.

## Example

One command adds a table, a REST API, and web pages. Add `--dry-run` to see the plan first.
The word in parentheses is the class of the file.

```console
$ spinup g resource todo title:string done:bool 'due:date?' 'priority:enum:low|normal|high' --dry-run
create api/db/migrations/00002_create_todos.sql (seed)
create api/db/queries/todos.sql (scaffold)
create api/internal/handlers/server.go (scaffold)
create api/internal/handlers/todos.go (scaffold)
create api/internal/handlers/todos_test.go (scaffold)
create api/internal/handlers/validation.go (scaffold)
update api/internal/routes/routes_gen.go (managed)
create api/openapi/paths/todos.yaml (scaffold)
create api/openapi/schemas/todo.yaml (scaffold)
update web/src/lib/nav.gen.ts (managed)
create web/src/routes/todos/+page.server.ts (scaffold)
create web/src/routes/todos/+page.svelte (scaffold)
create web/src/routes/todos/[id]/+page.server.ts (scaffold)
create web/src/routes/todos/[id]/+page.svelte (scaffold)
create web/src/routes/todos/[id]/edit/+page.server.ts (scaffold)
create web/src/routes/todos/[id]/edit/+page.svelte (scaffold)
create web/src/routes/todos/fields.svelte (scaffold)
create web/src/routes/todos/new/+page.server.ts (scaffold)
create web/src/routes/todos/new/+page.svelte (scaffold)
create web/src/routes/todos/schema.ts (scaffold)
```

Without `--dry-run`, the command prints `Created 18, updated 2 files`.
spinup owns the two `managed` files and replaces them on upgrade. You own the other 18.

The migration is plain SQL for `goose`. It is a `seed` file: spinup writes it once and never changes it.

<!-- golden: testdata/golden/gen-resource/api/db/migrations/00002_create_todos.sql#L1-L13 -->
```sql
-- +goose Up
CREATE TABLE todos (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title text NOT NULL,
    done boolean NOT NULL,
    due date,
    priority text NOT NULL CHECK (priority IN ('low', 'normal', 'high')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE todos;
```

The query is plain SQL too. `sqlc` turns it into the typed Go method `Queries.CreateTodo`.

<!-- golden: testdata/golden/gen-resource/api/db/queries/todos.sql#L18-L21 -->
```sql
-- name: CreateTodo :one
INSERT INTO todos (title, done, due, priority)
VALUES ($1, $2, $3, $4)
RETURNING id, title, done, due, priority, created_at, updated_at;
```

The handler is plain Go. The request and response types come from your OpenAPI files.
Edit the handler as you need. An upgrade merges template changes into your version.

<!-- golden: testdata/golden/gen-resource/api/internal/handlers/todos.go#L94-L105 -->
```go
// CreateTodo inserts a todo.
func (s *Server) CreateTodo(ctx context.Context, req gen.CreateTodoRequestObject) (gen.CreateTodoResponseObject, error) {
	params, err := todoParams(*req.Body)
	if err != nil {
		return nil, err
	}
	row, err := s.Queries.CreateTodo(ctx, params)
	if err != nil {
		return nil, err
	}
	return gen.CreateTodo201JSONResponse(todoFromStore(row)), nil
}
```

## How it works

1. **Templates become your files.** `spinup new` renders template packs into one folder with `api/`, `web/`,
   `deploy/`, and `spinup.toml`. The `base`, `api`, and `web` packs are the core, and each module is one more
   pack ([Concepts][concepts], [Project layout][layout]).
2. **Each file has a class.** spinup owns a `managed` file. You own a `scaffold` file, and an upgrade merges
   it. spinup writes a `seed` file once. `spinup.lock` stores the class and a hash of each file
   ([Concepts][concepts]).
3. **Generators add code.** A generator writes files and records them in the lock. Then it runs the tools
   that its files need, such as `sqlc` and `oapi-codegen`, which write the typed Go code ([generate][generate]).
4. **The kit holds the runtime.** Your API imports the kit, which is a Go library. The kit has the HTTP
   server, sessions, roles, the database pool, jobs, mail, and logging. You do not write that code again for each app
   ([The kit library][kit]).
5. **A deploy is a release with a color.** `spinup deploy` starts the new release as `blue` or `green` while
   the other color serves. Caddy switches traffic when the new release is healthy, and `spinup rollback`
   returns to a stored release ([Deploy][deploy]).
6. **An upgrade is a three-way merge.** `spinup upgrade` merges the old template, the new template, and your
   file. Codemods edit your code for a breaking change, and each one runs once ([Upgrade][upgrade]).

`spinup doctor` checks your tools and your app, and prints a fix for each failure ([Doctor checks][doctor]).

## Performance

These numbers come from the committed baseline [`perf/baseline/apple-m1-max.md`](perf/baseline/apple-m1-max.md).
The run measured commit `3d475d30c8cd` on 2026-11-13.
The machine was one Apple M1 Max with 10 cores and 64 GiB, with macOS 26.4.1 and Go 1.27.3.

| What | Median |
|---|---:|
| The stripped `spinup` binary | 21.1 MB |
| `spinup --help` | 11.5 ms |
| `spinup new --api-only --modules ci,jobs,mail --no-git` | 42.6 ms |
| `spinup g resource` for one resource, with no database steps | 111 ms |
| `spinup upgrade` with no template change | 37.9 ms |
| Generated API: cold build | 11.3 s |
| Generated API: build after a one-line edit | 1.23 s |
| Generated API: start to the first 200 from `/healthz` | 11.9 ms |
| Generated API: `GET /healthz` | 77,188 requests per second |
| Generated API: signed-in list of 50 rows as JSON | 5,395 requests per second, p50 5.73 ms |
| Generated API: memory when idle | 22.3 MiB |
| Web app: client bundle, brotli | 158,359 bytes |
| Web app: JavaScript of the sign-in page, brotli (CSS not counted) | 101,034 bytes |
| Web app: server render of the sign-in page | 4,987 requests per second |
| Web app: server render of a signed-in list page | 824 requests per second |

Read the limits before you use a number.
The load generator is `scripts/perf/loadgen` with 32 connections for 5 seconds.
It ran on the same machine as the server, on all cores.
The web numbers come from one Node process.
Each timed number is the median of 2 to 30 samples.

The table shows one commit on one laptop. It does not show the current code.
It does not compare spinup with another tool.
CI does not measure time. It checks the allocation budgets of the kit with `make perf-gate`.
[Performance loop][perf] describes the method.

## When to use it

spinup fits a product that needs a Go API, a SvelteKit web app, and PostgreSQL on one server.
It fits a team that wants to own plain code and still receive template fixes.
It fits a team that wants deploys, backups, and monitoring from the same tool that made the app.

Do not use spinup for these needs:

- **Several servers or Kubernetes.** spinup deploys to one VM that runs Docker Compose and Caddy.
- **Another database, another web framework, or an ORM.** The stack is fixed: PostgreSQL through `sqlc`, and SvelteKit.
- **Native Windows development.** `spinup dev`, `spinup deploy`, and `spinup server` need WSL2.
- **One binary with sign-in pages.** Embed mode serves the web app from the Go binary. It does not accept the `auth` module yet.

## Documentation

| Guide | What it covers |
|---|---|
| [Install spinup][install] | the tools that spinup needs, and their versions |
| [Your first app][first-app] | a tutorial: create an app, run it, add a resource, and limit a page to a role |
| [Concepts][concepts] | packs, modules, file classes, the lock, the three versions, and a glossary |
| [Build your app][build] | the web app, sign-in, roles, mail, jobs, audit trail, feature flags, API keys, MCP, billing, live data, sidecar services, PDF reports, embed mode, and plugins |
| [Deploy][deploy] | DigitalOcean, your own server, Hetzner Cloud with OpenTofu, environments, secrets, and generated CI |
| [Operate][operate] | monitoring, the operator console, and backups |
| [Upgrade][upgrade] | the plan, the merge, conflicts, and codemods |
| [Command guides][commands] | one guide with examples for each command |
| [CLI reference][cli] | each command and each flag, generated from the code |
| [Reference][reference] | `spinup.toml`, doctor checks, app commands, the kit, and the support policies |
| [Compatibility matrix][compat] and [Pinned stack][stack] | the versions that fit together |
| [Contributing][contributing] | how to work on spinup |

The API of the kit is on [pkg.go.dev](https://pkg.go.dev/github.com/stopyellingatme/spinup/kit).
The design records are in the repository: [`docs/adr/`](docs/adr/) and [`docs/design/architecture.md`](docs/design/architecture.md).

## Examples

1. [`testdata/golden/gen-resource/`](testdata/golden/gen-resource/): the files of one generated resource. Start here.
2. [`testdata/golden/api/`](testdata/golden/api/): the files that the `base` and `api` packs write, with `api/cmd/api/main.go`.
3. [`testdata/golden/auth/`](testdata/golden/auth/): the files that the `auth` module adds or changes.
4. [`examples/plugin/`](examples/plugin/): a plugin with one executable generator, one declarative generator, one check, and a conformance test.

The golden trees are real generator output. `make golden` compares each tree with a new render.

## Go modules

| Module | What it is |
|---|---|
| [`github.com/stopyellingatme/spinup`](cmd/spinup) | the `spinup` command, with the template packs embedded in the binary |
| [`github.com/stopyellingatme/spinup/kit`](kit) | the runtime library that each generated API imports: `app`, `auth`, `httpx`, `db`, `jobs`, `mail`, `telemetry`, and more |
| [`github.com/stopyellingatme/spinup/plugin-sdk`](plugin-sdk) | API 1 for executable plugins: JSON-RPC 2.0 over stdin and stdout, with a conformance kit |

`go.work` joins the three modules.

## Status

spinup has no release yet. Build it from source, as the quick start shows.
The install script, the Homebrew cask, and `spinup self-update` need a release, so they do not work yet.
A source build has the version `0.1.0-alpha.<n>`, and a new app pins a prerelease tag of the kit.

The kit stays on v0.x until version 1.0. A deprecated kit API stays for one minor version.
The API of a package under `kit/x/` can change before 1.0.
The release configuration signs each binary with cosign and adds an SBOM. No release has used it yet.

spinup runs on Linux and macOS, on amd64 and arm64. On Windows, `spinup dev`, `spinup deploy`, and `spinup server` need WSL2.
The stack is pinned: Go 1.27, Node 24, SvelteKit 3, Svelte 5, TypeScript 6, Tailwind 4, and PostgreSQL 18.
[Pinned stack][stack] lists each exact version.
[CHANGELOG.md](CHANGELOG.md) lists what changed, and the [v1.0 checklist][v1] lists what is still open.

## Values

1. **Own the glue, not the tools.** spinup owns the render engine, the lock, the merge, the codemods, the
   deployer, and the plugin host. `sqlc`, `goose`, Docker Compose, Caddy, and sops do the other work. A tool
   that owns less costs less to maintain.
2. **Keep the scaffold thin and the kit fat.** If two apps differ only by configuration values, the code
   belongs in the kit. A fix then reaches your app as a new kit version, with no merge.
3. **Decide the class of a file before you write its template.** The class says who owns the file. The
   upgrade uses the class.
4. **A render is a pure function.** The template version, the answers, and the plugin set decide each byte.
   So an upgrade can render the old template again, and your app does not store it.
5. **No CGO, no custom DSL, no custom ORM, and no bundler configuration in the CLI.** You already know the
   tools that the app uses.
6. **Each error says what failed, why, and a `Try:` line.** The next command is in the message.
7. **Each prompt has a flag.** With no terminal, the CLI never prompts. It fails and names the missing flag,
   so each command works in CI.
8. **The core works with zero plugins.** spinup uses a plugin only after you approve its exact contents.
9. **The CLI sends no telemetry.**

## Contributing

spinup welcomes issues and pull requests. [CONTRIBUTING.md](CONTRIBUTING.md) has the repository layout, the
checks, and the review rules. Follow the [code of conduct](CODE_OF_CONDUCT.md).
Report a vulnerability as [SECURITY.md](SECURITY.md) describes.

## License

[MIT](LICENSE). [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) lists the files from other projects that
this repository ships, with the license of each.

### Generated code

The files that spinup writes into your app are yours.
You can use, change, and distribute them under any terms.
You do not need to keep the spinup copyright notice.
This permission covers the output of the template packs and the generators.

The permission does not cover third-party files that spinup copies into an app. [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) lists them.
It does not cover the kit. Your app imports the kit as a Go module under the MIT license.

[site]: https://stopyellingatme.github.io/spinup/
[install]: https://stopyellingatme.github.io/spinup/get-started/install.html
[first-app]: https://stopyellingatme.github.io/spinup/get-started/first-app.html
[layout]: https://stopyellingatme.github.io/spinup/get-started/project-layout.html
[concepts]: https://stopyellingatme.github.io/spinup/concepts.html
[build]: https://stopyellingatme.github.io/spinup/build/
[deploy]: https://stopyellingatme.github.io/spinup/deploy/
[operate]: https://stopyellingatme.github.io/spinup/operate/
[upgrade]: https://stopyellingatme.github.io/spinup/upgrading.html
[commands]: https://stopyellingatme.github.io/spinup/commands/
[generate]: https://stopyellingatme.github.io/spinup/commands/generate.html
[cli]: https://stopyellingatme.github.io/spinup/site/commands/
[reference]: https://stopyellingatme.github.io/spinup/reference/
[kit]: https://stopyellingatme.github.io/spinup/reference/kit.html
[doctor]: https://stopyellingatme.github.io/spinup/reference/doctor-checks.html
[compat]: https://stopyellingatme.github.io/spinup/site/compat.html
[stack]: https://stopyellingatme.github.io/spinup/site/stack.html
[contributing]: https://stopyellingatme.github.io/spinup/contributing/
[perf]: https://stopyellingatme.github.io/spinup/contributing/performance.html
[v1]: https://stopyellingatme.github.io/spinup/contributing/v1-checklist.html
[llms]: https://stopyellingatme.github.io/spinup/llms.txt
