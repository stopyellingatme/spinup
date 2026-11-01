---
title: Home
nav_order: 1
permalink: /
---

# spinup

spinup creates a full-stack app and runs it for you. You get a Go API, a SvelteKit web app, PostgreSQL, a deploy to a VM, and monitoring.
Generated files stay easy to read and easy to upgrade, because spinup knows which files you own.

## Quick start

```sh
curl -fsSL https://github.com/stopyellingatme/spinup/releases/latest/download/install.sh | sh
spinup new todo --module example.com/todo
cd todo
spinup dev
```

`spinup new` asks a few questions. Add `--non-interactive` to skip them.
`spinup dev` installs the web packages, starts PostgreSQL and Mailpit in Docker, runs the migrations, and starts the API and the web app.
It prints the addresses:

```text
App      http://localhost:5173
API      http://localhost:8080
Mailpit  http://localhost:8025
```

The first command installs spinup. You can also use Homebrew, and you need Go, Node, and Docker. Read [Install spinup](get-started/install.md).
You want every step with its output? Read [Your first app](get-started/first-app.md).

## What you get

| Part | What it is |
| --- | --- |
| API | Go with `net/http`, PostgreSQL through `sqlc`, migrations with `goose`, and an OpenAPI contract |
| Web app | SvelteKit with Tailwind and shadcn-svelte, and a typed client generated from the OpenAPI contract |
| Database | PostgreSQL, in Docker for development |
| Deploy | Docker Compose on a VM, Caddy for HTTPS, and a zero-downtime switch between two copies of the app |
| Monitoring | OpenTelemetry in the app, with Grafana for logs, metrics, and traces |
| Modules | Sign-in with roles, mail, jobs, audit trail, secrets, backups, monitoring, and OpenTofu, each added when you need it |
| Sidecar services | Rust, Python, or Node next to the API, and PDF reports |
| Upgrades | `spinup upgrade` brings new template versions into your app and keeps your edits |

## I want to ...

| I want to ... | Read |
| --- | --- |
| Install spinup | [Install spinup](get-started/install.md) |
| Build and run a first app | [Your first app](get-started/first-app.md) |
| Find a file in my app | [Project layout](get-started/project-layout.md) |
| Learn the words that the docs use | [Concepts](concepts.md) |
| Copy a solution for a common job | [Recipes](recipes.md) |
| Add a CRUD API and its pages | [generate resource](commands/generate-resource.md) |
| Add a page | [generate page](commands/generate-page.md) |
| Add sign-in | [Accounts and sign-in](auth.md) |
| Limit a page to some users | [Roles](build/roles.md) |
| Send an email | [Mail](mail.md) |
| Run a background job | [Background jobs](build/jobs.md) |
| Make a PDF report | [PDF reports](pdf-reports.md) |
| Add a service in Rust, Python, or Node | [Sidecar services](services.md) |
| Deploy to a new DigitalOcean droplet | [Deploy to DigitalOcean](deploy-digitalocean.md) |
| Deploy to my own server | [Deploy to your own server](deploy.md) |
| See logs, metrics, and traces | [Monitoring](monitoring.md) |
| Back up the database | [Backups with pg_dump](backups-pgdump.md) or [Backups with WAL-G](backups-walg.md) |
| Upgrade my app to a new template | [Upgrade](upgrading.md) |
| Look up a flag of a command | [CLI reference](site/commands/index.md) |
| Fix an error | [Troubleshooting](troubleshooting.md) |
| See which versions of the tools and the kit fit together | [Compatibility matrix](site/compat.md) and [Pinned stack](site/stack.md) |

## The sections

| Section | What it covers |
| --- | --- |
| [Get started](get-started/index.md) | Install spinup, make an app, and learn the layout |
| [Build your app](build/index.md) | The web app, sign-in, mail, jobs, services, reports, and plugins |
| [Deploy](deploy/index.md) | DigitalOcean, your own server, environments, secrets, and CI |
| [Operate](operate/index.md) | Monitoring, backups, and data handling |
| [Upgrade](upgrading.md) | Bring new template versions into your app |
| [Command guides](commands/index.md) | One page with examples for each command |
| [CLI reference](site/commands/index.md) | Every command and every flag of it, generated from the code |
| [Reference](reference/index.md) | Settings and policies: the `spinup.toml` file, global flags, doctor checks, app commands, and the support policies |
| [Contributing](contributing/index.md) | How to work on spinup itself |
