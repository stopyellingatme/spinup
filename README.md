# spinup

spinup creates and operates a full-stack app: a Go API, a SvelteKit web app, PostgreSQL, a Docker Compose deploy to a VM, and OpenTelemetry monitoring.
Generated apps are plain code that you own. spinup tracks which files it wrote, so you can upgrade an app for years and keep your edits.

**Documentation: https://stopyellingatme.github.io/spinup/**

## Quick start

Install spinup:

```sh
curl -fsSL https://github.com/stopyellingatme/spinup/releases/latest/download/install.sh | sh
# or: brew install --cask stopyellingatme/tap/spinup
```

Make an app and run it. `spinup new` asks a few questions. Add `--non-interactive` to skip them.

```sh
spinup new todo --module example.com/todo
cd todo
spinup dev
```

`spinup dev` installs the web packages, starts PostgreSQL and Mailpit in Docker, applies the migrations, and starts the API and the web app.

Open http://localhost:5173 for the app, http://localhost:8080 for the API, and http://localhost:8025 for Mailpit.
You need Go 1.26 or later, Node 24 or later, pnpm through corepack, and Docker. Read the [install guide](https://stopyellingatme.github.io/spinup/get-started/install.html) for details.

Add a CRUD API with its pages, then deploy:

```sh
spinup g resource task title:string done:bool
spinup deploy digital-ocean --recovery-recipient age1...
```

## What you get

- A Go API with `net/http`, `sqlc`, `goose` migrations, and an OpenAPI contract
- A SvelteKit web app with a typed API client
- Modules: sign-in with roles, mail, jobs, audit trail, secrets, backups, monitoring, and OpenTofu
- Sidecar services in Rust, Python, or Node, and PDF reports
- A zero-downtime deploy to your own VM or to a new DigitalOcean droplet
- Monitoring with Grafana, and backups with pgBackRest, WAL-G, or pg_dump
- Safe upgrades: `spinup upgrade` merges new templates with your edits in a separate git worktree

## Documentation

| I want to ... | Read |
| --- | --- |
| Install spinup | [Install](https://stopyellingatme.github.io/spinup/get-started/install.html) |
| Build a first app | [Your first app](https://stopyellingatme.github.io/spinup/get-started/first-app.html) |
| Look up a command | [CLI reference](https://stopyellingatme.github.io/spinup/site/commands/) |
| Add sign-in, mail, jobs, or services | [Build your app](https://stopyellingatme.github.io/spinup/build/) |
| Deploy | [Deploy](https://stopyellingatme.github.io/spinup/deploy/) |
| Watch and back up my app | [Operate](https://stopyellingatme.github.io/spinup/operate/) |
| Upgrade an app | [Upgrade](https://stopyellingatme.github.io/spinup/upgrading.html) |

The source of the docs is in [`docs/`](docs/).

## Repository layout

| Path | What it holds |
| --- | --- |
| `cmd/spinup/`, `internal/` | The `spinup` command line tool (module `github.com/stopyellingatme/spinup`) |
| `internal/templates/packs/` | The template packs that `spinup new` renders |
| `kit/` | The runtime library of generated apps (its own Go module) |
| `plugin-sdk/` | The SDK for plugin authors (its own Go module) |
| `docs/` | The documentation site. `docs/site/` is generated, so do not edit it. |
| `docs/adr/`, `docs/design/` | Design decisions and the design of spinup |
| `testdata/script/` | End-to-end command tests |

`go.work` joins the three Go modules.

## Build and test

You need Go 1.27, Node 24, and Docker for the full checks.

```sh
make build      # go build in every module
make test       # go test -race in every module
make lint       # golangci-lint in every module
make golden     # golden-tree tests of the template packs
make script     # testscript tests of the commands
make doctest    # run the code blocks in docs/
```

To build the CLI from this checkout, run `go build -o bin/spinup ./cmd/spinup`. `make help` lists every target. Read [Contributing](https://stopyellingatme.github.io/spinup/contributing/) before you send a change.

## Security

Report a vulnerability as [SECURITY.md](SECURITY.md) describes.
