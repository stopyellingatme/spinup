---
title: Recipes
parent: Get started
nav_order: 5
---

# Recipes

Short answers for the tasks you do most. Each recipe has the commands to type and a link to the full guide.
Find your task in the list, copy the commands, and change the names.

Run these recipes in your own app. The outputs below come from a sample app with every module on, so the migration numbers and the file lists in your app can differ.
To follow along exactly, create the sample app with this command in an empty folder:

```console
$ spinup new todo --module example.com/todo --modules ci,deploy,auth,jobs,mail,secrets,audit,monitoring,backups --no-git --non-interactive
...
$ cd todo
```

| I want to ... | Recipe |
| --- | --- |
| Add a table with a CRUD API and pages | [Add a resource](#add-a-resource) |
| Add a column to a table | [Add a field to a resource](#add-a-field-to-a-resource) |
| Add a page | [Add a page](#add-a-page) |
| Change the database | [Write and apply a migration](#write-and-apply-a-migration) |
| Limit a route to a role | [Limit a resource to a role](#limit-a-resource-to-a-role) |
| Send an email | [Send an email](#send-an-email) |
| Run work in the background | [Run a background job](#run-a-background-job) |
| Call a program in another language | [Add a sidecar service](#add-a-sidecar-service) |
| Make a PDF | [Make a PDF report](#make-a-pdf-report) |
| Deploy | [Deploy to a new server](#deploy-to-a-new-server) |
| Go back to the last release | [Roll back a release](#roll-back-a-release) |
| Back up the database | [Back up and restore](#back-up-and-restore) |
| Move to a new spinup version | [Upgrade your app](#upgrade-your-app) |

## Build

### Add a resource

A resource is a table with a CRUD API and web pages. Name each field as `name:type`.

```console
$ spinup g resource task title:string done:bool --dry-run
...
create api/db/migrations/00007_create_tasks.sql (seed)
...
create web/src/routes/tasks/+page.svelte (scaffold)
...
```

Remove `--dry-run` to write the files. Then run `spinup db migrate up`.
See [generate resource](commands/generate-resource.md).

### Add a field to a resource

Do not run `g resource` again. A new field touches the database, the queries, the API contract, the Go handlers, and the web form.
The example adds `priority` to `task`. Start with a migration. See [Write and apply a migration](#write-and-apply-a-migration).

```console
$ spinup db new add_priority --dry-run
Would create api/db/migrations/00007_add_priority.sql
```

Fill in the new file:

```sql
-- +goose Up
ALTER TABLE tasks ADD COLUMN priority integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE tasks DROP COLUMN priority;
```

Then change the code in this order:

1. Run `spinup db migrate up` to apply the migration.
2. In `api/db/queries/tasks.sql`, add `priority` to every `SELECT` list and `RETURNING` list, to the `INSERT`, and to the `UPDATE`. Run `spinup db sqlc`.
3. In `api/openapi/schemas/task.yaml`, add `priority` to the `Task` and `TaskInput` schemas. Run `spinup g openapi` and `spinup g client`.
4. In `api/internal/handlers/tasks.go`, add the field to `taskFromStore`, `taskParams`, and the update call. Run `cd api && go build ./...`.
5. In `web/src/routes/tasks/`, add the field to `schema.ts` (the zod schema, `fromItem`, and `toBody`). Add an input to `fields.svelte`. The new page and the edit page share both files.

While `spinup dev` runs, saving a query file or an OpenAPI file runs sqlc and the generators for you.
If you skip a step, the build or the type check fails and names the missing field.
See [db](commands/db.md) and [generate resource](commands/generate-resource.md).

### Add a page

```console
$ spinup g page reports --server --dry-run
create web/src/routes/reports/+page.server.ts (scaffold)
create web/src/routes/reports/+page.svelte (scaffold)
```

`--server` also writes a `load` function that runs on the server. See [generate page](commands/generate-page.md) and [The web app](web.md).

### Add a shadcn component

<!-- doctest: skip -->
```console
$ spinup g component dialog
```

The command needs the network. See [generate component](commands/generate-component.md).

### Run the app and read the routes

<!-- doctest: skip -->
```console
$ spinup dev
```

`spinup dev` starts PostgreSQL, the API, and the web app. In another terminal, list the routes of the API:

```console
$ spinup routes
METHOD  PATH      SOURCE
GET     /healthz  kit
GET     /readyz   kit
GET     /version  kit
```

See [dev](commands/dev.md) and [routes](commands/routes.md).

### Run the tests

```console
$ spinup test --dry-run
go test ./...
```

Remove `--dry-run` to run the Go tests. Run the web tests with `pnpm test` in `web/`. See [Test your app](build/testing.md).

## Database

### Write and apply a migration

```console
$ spinup db new add_notes --dry-run
Would create api/db/migrations/00007_add_notes.sql
$ spinup db migrate up --dry-run
go run ./cmd/api migrate up
```

Edit the new file, then run `spinup db migrate up` without `--dry-run`.
The command `spinup g migration <name>` does the same as `spinup db new <name>`.

Check a migration for changes that are unsafe in a zero-downtime deploy:

<!-- doctest: skip -->
```console
$ spinup db lint
```

See [db](commands/db.md) and [db lint](commands/db-lint.md).

### Start again with an empty development database

{: .warning }
`spinup db reset` deletes every row of the development database.

<!-- doctest: skip -->
```console
$ spinup db reset --yes
```

To keep a copy first, run `spinup db dump backup.dump`. Load it later with `spinup db restore backup.dump`.

## Accounts and roles

### Limit a resource to a role

The roles come from `[auth]` in `spinup.toml`. The defaults are `manager` and `staff`.

```console
$ spinup g resource report title:string --role manager --dry-run
...
update api/internal/routes/routes_gen.go (managed)
...
```

A user with another role gets the answer 403. See [Roles](build/roles.md).

### Add a role

Edit `spinup.toml`, then write the roles file:

```toml
[auth]
roles = ["manager", "staff", "viewer"]
admin_role = "manager"
default_role = "staff"
```

```console
$ spinup g roles --dry-run
unchanged api/internal/roles/roles.gen.go (managed)
```

The output says `unchanged` here because the example did not save the new roles. After you edit the file, it says `update`.
`spinup doctor` warns with `roles-drift` when the file and `[auth]` differ.

### Create an account or change a role

Run the app command from `api/`. It reads `DATABASE_URL` from `.env`.

<!-- doctest: skip -->
```console
$ cd api
$ printf '%s' 'correct horse battery' | go run ./cmd/api user create --email ada@example.com --role manager --password-stdin
```

To change a role, use the action `user set-role`. The page [App binary commands](reference/app-commands.md#user) lists every action and shows how to run them on a server. See also [Accounts and sign-in](auth.md).

## Mail, jobs, services, and reports

### Send an email

Make a mailer, then call its `Send` method from a handler.

```console
$ spinup g mailer receipt amount:string --dry-run
create api/internal/mail/receipt_gen.go (managed)
create api/internal/mail/templates/receipt.html.tmpl (seed)
create api/internal/mail/templates/receipt.subject.tmpl (seed)
create api/internal/mail/templates/receipt.txt.tmpl (seed)
```

```go
err := mailers.SendReceipt(r.Context(), "ada@example.com", mail.ReceiptData{Amount: "$12"})
```

In development, read the message in Mailpit at `http://localhost:8025`. See [Mail](mail.md).

### Run a background job

```console
$ spinup g job send_digest --dry-run
create api/internal/jobs/register_gen.go (managed)
create api/internal/jobs/send_digest.go (scaffold)
create api/internal/jobs/send_digest_test.go (scaffold)
```

Replace the body of the generated function with your work.
`spinup dev` starts the worker for you and restarts it when Go files change. In production, the worker runs as its own service in `deploy/compose.yml`.
See [Background jobs](build/jobs.md).

### Add a sidecar service

```console
$ spinup g service ocr --lang python --dry-run
...
update spinup.toml (scaffold)
```

The languages are `rust`, `python`, and `node`. The command also writes the Dockerfile and an OpenAPI file.
See [Sidecar services](services.md).

### Make a PDF report

A PDF report is a Python sidecar service with the kind `pdf`:

```console
$ spinup g service pdfs --lang python --kind pdf --dry-run
...
update spinup.toml (scaffold)
```

See [PDF reports](pdf-reports.md).

## Audit

### Record who changed a record

With the `audit` module on, a new resource has a trail and a History page. To leave one out, pass `--no-audit`:

```console
$ spinup g resource note body:string --no-audit --dry-run
...
```

To add the trail to a table that exists already, run `spinup g audit <table>`. Read the events with the app command:

<!-- doctest: skip -->
```console
$ cd api
$ go run ./cmd/api audit list --resource tasks
```

See [Audit trail](audit.md).

## Deploy

### Add an environment

Add the server to `spinup.toml`, then make its secrets and prepare the VM:

```toml
[env.staging]
host = "203.0.113.10"
user = "root"
public_url = "https://staging.example.com"
```

<!-- doctest: skip -->
```console
$ spinup server bootstrap --env staging
$ spinup secrets init --env staging --recovery-recipient age1...
```

A fresh VM has no `deploy` user, so connect as `root` (or another user with passwordless sudo) for the bootstrap.
The bootstrap creates the `deploy` user. After it, change `user` to `"deploy"` in `spinup.toml`.
See [Deploy for the first time](deploy.md#deploy-for-the-first-time), [Environments](deploy/environments.md) and [server](commands/server.md).

### Deploy to a new server

If you have a DigitalOcean account, one command creates the server and deploys:

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --region nyc3 --no-recovery --yes
```

For your own server, see [Deploy to your own server](deploy.md). For this command, see [Deploy to DigitalOcean](deploy-digitalocean.md).

### Deploy a release

Commit your work, then deploy. Look at the plan first if you are not sure.

<!-- doctest: skip -->
```console
$ spinup deploy --plan --env staging
$ spinup deploy --env staging
```

If a deploy stops, run `spinup deploy status --env staging`, then `spinup deploy --resume --env staging`. See [deploy](commands/deploy.md).

### Roll back a release

<!-- doctest: skip -->
```console
$ spinup releases list --env staging
$ spinup rollback --env staging
```

A rollback never undoes a database migration. See [rollback](commands/rollback.md).

## Secrets

### Set a secret

<!-- doctest: skip -->
```console
$ printf '%s' 'postgres://app:pw@postgres:5432/app?sslmode=disable' | spinup secrets set --env staging DATABASE_URL
$ spinup secrets push --env staging
```

`secrets set` reads the value from the standard input, so it stays out of your shell history. See [Secrets](deploy/secrets.md).

### Give a new teammate access

Add their age public key to `.sops.yaml`, then re-encrypt:

<!-- doctest: skip -->
```console
$ spinup secrets rekey --env staging
```

See [secrets](commands/secrets.md).

## Operate

### Open Grafana for production

<!-- doctest: skip -->
```console
$ spinup monitor open --env production
$ spinup monitor logs --env production
```

See [Monitoring](monitoring.md) and [monitor](commands/monitor.md).

### Back up and restore

Pick the backend in `spinup.toml`:

```toml
[backups]
backend = "pgbackrest"
retention = "4"
```

<!-- doctest: skip -->
```console
$ spinup backup now --env production
$ spinup backup verify --env production
```

To check the steps of a restore without a change, use `--plan`:

```console
$ printf '\n[env.staging]\nhost = "203.0.113.10"\nuser = "deploy"\npublic_url = "https://staging.example.com"\n' >> spinup.toml
$ spinup backup restore --env staging --into in-place --plan
Plan: Check --i-know, protected confirmation, and maintenance window
...
```

See [Backups with pg_dump](backups-pgdump.md), [Backups with WAL-G](backups-walg.md), and [backup restore](commands/backup-restore.md).

## Upgrade and repair

### Upgrade your app

<!-- doctest: skip -->
```console
$ spinup self-update
$ spinup upgrade --plan
$ spinup upgrade --yes
$ git merge spinup/upgrade-0.12.0
```

The branch name uses the new version. See [Upgrade](upgrading.md).

### Check your app and fix a file

```console
$ spinup check
...
```

If a managed file has drift, `spinup diff <path>` shows it. Then run `spinup eject <path>` to keep your edit, or `spinup restore <path> --force` to drop it.
See [Concepts](concepts.md#file-classes-managed-scaffold-and-seed).

### Check the health of your machine

<!-- doctest: skip -->
```console
$ spinup doctor
```

Each failed check prints a `Try:` line. See [Doctor checks](reference/doctor-checks.md) and [Troubleshooting](troubleshooting.md).

## See also

- [Your first app](get-started/first-app.md): a hands-on tutorial
- [Concepts](concepts.md): the ideas behind the commands
- [Command guides](commands/index.md): one page for each command
- [Troubleshooting](troubleshooting.md): what to do when a command fails
