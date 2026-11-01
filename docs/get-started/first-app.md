---
title: Your first app
parent: Get started
nav_order: 2
---

# Your first app

In this tutorial you build a small to-do app. You create it, run it, add a CRUD API and web pages, and limit a page to one role.
It takes about fifteen minutes. You need the tools from [Install spinup](install.md).

```sh
spinup new todo --module example.com/todo --modules ci,deploy,auth
cd todo
spinup g resource task title:string done:bool
spinup dev
```

These commands give you a working app with sign-in, a `tasks` table, a REST API, and list, create, and edit pages.
The steps below show each part in turn.

## 1. Create the app

`spinup new` writes the whole app into the new folder `todo`.

| Flag | What it does |
| --- | --- |
| `--module example.com/todo` | Sets the Go module path of the API |
| `--modules ci,deploy,auth` | Turns on these modules. The default is `ci,deploy`. `auth` adds sign-in. This flag is not the same as `--module`, which sets the Go path. |
| `--non-interactive` | Does not ask questions |

```console
$ spinup new todo --module example.com/todo --modules ci,deploy,auth --non-interactive
...
App      http://localhost:5173
API      http://localhost:8080
Mailpit  http://localhost:8025
$ cd todo
```

Without `--non-interactive`, spinup asks you a few questions, such as the PostgreSQL version.
spinup also makes a git repository with a first commit. Add `--no-git` to skip it.
Read [Project layout](project-layout.md) for a tour of the files.

{: .tip }
Choose the modules now. You can add `auth` later with `spinup module add auth`, but do it before you generate a resource.
The module and the resource both write numbered migrations, and a resource that exists already can clash with the number of the auth migration.

## 2. Run it

`spinup dev` runs the whole stack. Open a second terminal, run `cd todo`, and start the stack. Leave it open.
The steps after this one use your first terminal, which is also in the `todo` folder.

<!-- doctest: skip -->
```console
$ spinup dev
```

`spinup dev` does these things:

1. It checks your tools and your `.env` file.
2. It installs the web packages when `web/node_modules` is missing.
3. It starts PostgreSQL and Mailpit in Docker.
4. It applies the database migrations.
5. It starts the API with live reload, and the web app with hot reload.

It prints the addresses:

| What | Address | Use it to |
| --- | --- | --- |
| App | `http://localhost:5173` | Use the web app |
| API | `http://localhost:8080` | Call the API, for example `/healthz` |
| Mailpit | `http://localhost:8025` | Read the emails that the app sends |

If a port is busy, spinup picks a free port and prints it. Press `Ctrl-C` to stop the API and the web app.
The Docker services keep running. To stop them on exit too, start the stack with `spinup dev --down`.

## 3. Add a resource

A resource is a table with a CRUD API and its pages. Name the fields as `name:type`.
In your first terminal, run this while `spinup dev` runs. The API and the web app reload when the files appear.

```console
$ spinup g resource task title:string done:bool
Created 18, updated 2 files
Next: spinup db migrate up
Next: cd api && go test ./...
```

The generator wrote a migration, SQL queries, the OpenAPI contract, the handlers and their test, the typed web client, and the list, detail, create, and edit pages.
`spinup dev` applies migrations when it starts. To apply the new migration now, run `spinup db migrate up`.
See the new routes:

```console
$ spinup routes
METHOD  PATH             SOURCE
GET     /api/tasks       openapi:listTasks
POST    /api/tasks       openapi:createTask
DELETE  /api/tasks/{id}  openapi:deleteTask
GET     /api/tasks/{id}  openapi:getTask
PUT     /api/tasks/{id}  openapi:updateTask
GET     /healthz         kit
GET     /readyz          kit
GET     /version         kit
```

The `auth` module protects every resource route. Each signed-in user sees only their own tasks.
Step 5 shows how to sign in.

## 4. Add a page

A page is a SvelteKit route. This command writes `web/src/routes/about/+page.svelte`.

```console
$ spinup g page about
Created 1, updated 0 files
Next: cd web && pnpm check
```

Open `http://localhost:5173/about`. Edit the file and the browser updates at once.
Add `--server` to also write a `+page.server.ts` file with a `load` function.

## 5. Sign in and use a role

Open `http://localhost:5173/register` and create an account. You land on the task list at `/tasks`.
Create a task and mark it done.

Roles say who may do what. The defaults are `manager` and `staff`. A new account gets `staff`.
Limit a page to managers with `--role`:

```console
$ spinup g page reports --server --role manager
Created 2, updated 0 files
Next: cd web && pnpm check
```

Open `http://localhost:5173/reports`. You get a `403` page, because you are a `staff` user.
Make your account a manager with the `user` command of the API. It reads the database address from `.env`:

<!-- doctest: skip -->
```console
$ cd api
$ go run ./cmd/api user set-role --email you@example.com --role manager
updated you@example.com	manager
$ cd ..
```

The last line goes back to the `todo` folder. Reload `/reports`. The page opens, and you do not sign in again.
To rename the roles, edit the `[auth]` table in `spinup.toml` and run `spinup g roles`.
Read [Accounts and sign-in](../auth.md) and [Roles](../build/roles.md) for the details.

## 6. Run the tests

The generated resource has a test. `spinup test` runs the API tests with `go test`.

```console
$ spinup test --dry-run
go test ./...
```

The API tests need a PostgreSQL database. They start one in Docker, or use the `DATABASE_URL` that you set.

<!-- doctest: skip -->
```console
$ spinup test
$ cd web && pnpm check
```

## 7. Commit

`spinup new` made a git repository and a first commit. Commit the tasks that you added.
`spinup.lock` records which files spinup made, so commit it with the rest.

<!-- doctest: skip -->
```console
$ git add -A
$ git commit -m "Add tasks"
```

## Next steps

| I want to ... | Read |
| --- | --- |
| Know what each folder holds | [Project layout](project-layout.md) |
| Change the tasks table later | [Add a field to a resource](../recipes.md#add-a-field-to-a-resource) |
| Copy a solution for a common job | [Recipes](../recipes.md) |
| Put the app on the internet | [Deploy to DigitalOcean](../deploy-digitalocean.md) |
| See logs and traces | [Monitoring](../monitoring.md) |
| Send mail or run jobs | [Mail](../mail.md) and [Background jobs](../build/jobs.md) |
| Keep the app up to date | [Upgrade](../upgrading.md) |

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `spinup dev` says Docker is not available | The Docker daemon is off | Start Docker and run the command again |
| A port is busy | Another program uses it | Use `--port` or `--api-port`, or read the new address that spinup prints |
| `/tasks` sends you to `/login` | You are not signed in | Register at `/register`, or sign in |
| `/reports` shows `403` | Your role is not `manager` | Run `user set-role` as in step 5 |
| `spinup module add` says the tree is not clean | You have uncommitted changes | Commit or stash them |
| The generator fails in `pnpm install` | The web packages are missing or `pnpm` is not on `PATH` | Run `corepack enable`, then `spinup g openapi` |

## See also

- [Project layout](project-layout.md)
- [spinup new](../commands/new.md)
- [spinup dev](../commands/dev.md)
- [spinup generate resource](../commands/generate-resource.md)
- [spinup generate page](../commands/generate-page.md)
- [Flag reference: spinup new](../site/commands/new.md)
