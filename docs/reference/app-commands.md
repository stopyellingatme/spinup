---
title: App binary commands
parent: Reference
nav_order: 4
---

# App binary commands

The API of your app is one program, `api/cmd/api`. It serves HTTP when you start it with no arguments.
With an argument, it runs a command instead, such as `migrate up` or `user list`.
This page lists those commands. They are different from the `spinup` commands.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup db migrate up --dry-run
go run ./cmd/api migrate up
```

`spinup db migrate up` is a shortcut for the app command `migrate up`.

## Which commands your app has

The commands depend on the modules that are on.

| Command | Needs the module | What it does |
| --- | --- | --- |
| *(no argument)* | always | Serves the HTTP API. |
| [`healthcheck`](#healthcheck) | always | Asks the running server for `/healthz`. |
| [`migrate`](#migrate) | always | Runs the database migrations. |
| [`jobs-migrate`](#jobs-migrate) | `jobs` | Runs the migrations of the job queue. |
| [`user`](#user) | `auth` | Creates accounts and changes roles. |
| [`audit`](#audit) | `audit` | Reads, exports, and prunes the audit trail. |
| [`apikeys`](#apikeys) | `apikeys` | Makes, lists, and revokes API keys, and sets plans. |
| [`flags`](#flags) | `flags` | Lists and changes feature flags. |
| [`mail-worker`](#mail-worker) | `mail` and `jobs` | Runs a worker that only sends mail. |

If you give an unknown command, the program lists the commands that your app has:

<!-- doctest: skip -->
```console
$ go run ./cmd/api nope
cannot run command "nope": unknown command. Available commands: audit, jobs-migrate, mail-worker, migrate, user.
Try: use a listed command, or omit the command to serve HTTP
```

## Run a command in development

Run the commands from the `api/` folder. The program reads `DATABASE_URL` from the `.env` file of your app.

<!-- doctest: skip -->
```console
$ cd api
$ go run ./cmd/api user list
ada@example.com	manager
```

`spinup dev` starts PostgreSQL first. Start it before you run a command that uses the database.

## Run a command on the server

A deployed API runs in a container. Open a shell on the VM and run the command in that container:

<!-- doctest: skip -->
```console
$ spinup server ssh --env production
$ docker exec -i "$(docker ps --filter name=api --format '{{.Names}}' | head -n 1)" /api user list
ada@example.com	manager
```

The container already has the database settings. The program inside is `/api`.
For a command that reads a password, add `-i` and pipe the password in, as the `user create` example shows below.

{: .note }
You do not run `migrate up` by hand on the server. `spinup deploy` runs it for you before it switches traffic.

## healthcheck

Asks the server on the same machine for `/healthz`. It succeeds when the answer is 200 within two seconds.
The container health check uses it, because the image has no shell and no `curl`.

<!-- doctest: skip -->
```console
$ docker exec "$(docker ps --filter name=api --format '{{.Names}}' | head -n 1)" /api healthcheck
```

If it fails, the message says if the server is down or answered with another status.

## migrate

Runs the SQL migrations of the app. It takes one subcommand.
The app migrations are in `api/db/migrations/`. The kit migrations are in `api/db/kit/`.
The two directories have separate version numbers. See [Kit migrations](../concepts.md#kit-migrations).

| Subcommand | What it does | Example |
| --- | --- | --- |
| `up` | Applies every pending migration. It applies the kit migrations first. | `migrate up` |
| `down` | Rolls back the last app migration. | `migrate down` |
| `status` | Lists each migration as `applied` or `pending`. It lists the kit migrations first. | `migrate status` |
| `redo` | Rolls back the last app migration and applies it again. | `migrate redo` |
| `to <version>` | Moves the app migrations up or down to one version. | `migrate to 3` |
| `version` | Prints the highest applied app version. It prints `0` when none is applied. | `migrate version` |
| `kit <subcommand>` | Runs `up`, `down`, `status`, `to <version>`, or `version` on the kit migrations only. | `migrate kit to 0` |

<!-- doctest: skip -->
```console
$ go run ./cmd/api migrate status
applied  kit 00002_create_auth.sql
pending  kit 00003_password_resets.sql
applied  kit 00005_users_role.sql
applied  00001_init.sql
$ go run ./cmd/api migrate up
migrate: 1 pending
applied  kit 00003_password_resets.sql
```

The kit lines show the prefix `kit`. A kit migration and an app migration can have the same number.
The command `migrate kit` is the way to roll back a kit migration. From the project, `spinup db migrate kit ...` runs it for you. The commands `spinup db migrate <up|down|status>` and `spinup db to` work on the app migrations only.

A lock in PostgreSQL stops two runners from migrating at the same time.
With the jobs module, `migrate up` also runs the job queue migrations.

{: .warning }
`migrate down` and `migrate to` with a lower version can delete data. From your machine, `spinup db migrate down` asks you to confirm with `--i-know`.

## jobs-migrate

Runs only the migrations of the [job queue](../build/jobs.md). It takes `up`, `down`, or `status`.
You need it only when you want to move the queue on its own.

<!-- doctest: skip -->
```console
$ go run ./cmd/api jobs-migrate status
applied  river 001 create river migration
applied  river 002 initial schema
...
```

## user

Manages accounts. The only way to change a role is this command. See [Roles](../build/roles.md) and [Accounts and sign-in](../auth.md).

| Action | What it does |
| --- | --- |
| `user create --email E --role R --password-stdin` | Makes an account. The password comes from standard input. |
| `user set-role --email E --role R` | Changes the role of an account. |
| `user list` | Prints the email and role of each account. |
| `user backfill` | Gives `default_role` to each account with no role. It prints the count. |

`user create` and `user set-role` also take `--reason TEXT`. With the [audit trail](../audit.md), the text goes in the event.

<!-- doctest: skip -->
```console
$ printf '%s' 'correct horse battery' | go run ./cmd/api user create --email ada@example.com --role manager --password-stdin
created ada@example.com	manager
$ go run ./cmd/api user set-role --email ada@example.com --role staff --reason 'moved to support'
user set-role: the change would remove the last account with role "manager".
Try: give the role "manager" to another account first.
```

The password needs at least 12 characters. The command never prompts for it, so pipe it in.

## audit

Reads the [audit trail](../audit.md). It has three actions.

| Action | What it does | Example |
| --- | --- | --- |
| `audit list --resource TABLE` | Prints the events of one table. Filters: `--id`, `--actor`, `--action`, `--since`, `--until`, `--unattributed`, `--limit`. Add `--json` for one JSON object on each line. | `audit list --resource users --limit 20` |
| `audit export --since D --until D --format jsonl\|csv` | Writes the events of a date range. Add `--out FILE` to write a file. The command prints the number of events and a SHA-256 hash of the output. | `audit export --since 2026-01-01 --until 2026-02-01 --format csv --out jan.csv` |
| `audit prune --before D --reason TEXT --yes` | Deletes the events before a date. | `audit prune --before 2025-01-01 --reason 'retention policy' --yes` |

Dates are `YYYY-MM-DD`. The end date of a range is not included.

<!-- doctest: skip -->
```console
$ go run ./cmd/api audit list --resource users
2026-11-01 18:22  operator:ada  create #1  email: ada@example.com, password_hash: changed (value hidden), role: manager
```

{: .warning }
`audit prune` deletes events for good. It refuses to run without `--yes`.

## apikeys

Manages the [API keys](../build/api-keys.md) and plans. The command never prompts and needs no web server.
It writes to the database. A revoke or a plan change acts on the next key request.

| Action | What it does |
| --- | --- |
| `apikeys list [--email E] [--json]` | Prints one line for each account: the email, the plan, and today's use. `--json` prints the accounts with their keys. |
| `apikeys create EMAIL --name N [--scope read\|write] [--expires-days D]` | Makes a key and prints the token one time. The default scope is `read`. `--expires-days` is 0 to 365. |
| `apikeys revoke ID` | Revokes the key with this ID for any user. |
| `apikeys plan EMAIL PLAN` | Sets the plan of a user. The plan must be declared in `spinup.toml`. |
| `apikeys usage EMAIL [--days 30] [--json]` | Prints the stored counts as day, key ID, and count. |
| `apikeys prune --days N --yes` | Deletes usage rows and revoked keys older than N days. |

<!-- doctest: skip -->
```console
$ go run ./cmd/api apikeys create ada@example.com --name reports --scope read
todo_Q3n8...
This token cannot be shown again.
$ go run ./cmd/api apikeys plan ada@example.com pro
updated ada@example.com	pro
$ go run ./cmd/api apikeys prune --days 90
apikeys prune: --yes is required.
Try: apikeys prune --days <n> --yes
```

With the [audit trail](../audit.md), each change writes an event. The actor is the operator of the shell.
`apikeys prune` is the only action that deletes rows for good.

## flags

Lists and changes the [feature flags](../build/flags.md). The command never prompts and needs no web server.
It writes to the database, and each running process reads the change within `FLAGS_REFRESH`.

| Action | What it does |
| --- | --- |
| `flags list [--json]` | Prints one line for each flag. `--json` prints the entries with the declared roles. |
| `flags enable KEY` | Turns the global switch on. |
| `flags disable KEY` | Turns the global switch off. |
| `flags set KEY [--roles a,b] [--percent N]` | Sets the roles or the percentage. One of the two flags is required. `--roles ""` clears the roles. `--percent 0` clears the percentage. |
| `flags user KEY --email E (--on \| --off \| --clear)` | Sets or removes the rule of one account. Exactly one of the three flags is required. |
| `flags prune --yes` | Deletes the rows that have no declaration. |

The columns of `flags list` are the key, the state, the roles, the percentage, the number of user rules, and notes.
The notes are `public`, `default on`, `expires` or `expired` with a date, and `stale`.
A stale row has no declaration in `spinup.toml`. A role that `[auth]` no longer declares shows `(undeclared)`.

<!-- doctest: skip -->
```console
$ go run ./cmd/api flags list
KEY           STATE  ROLES    PERCENT  USERS  NOTES
new_checkout  on     manager  50       1      public,expires 2027-03-31
$ go run ./cmd/api flags disable new_checkout
updated new_checkout	off
$ go run ./cmd/api flags prune
flags prune: --yes is required, because the command deletes rows.
Try: flags prune --yes
```

With the [audit trail](../audit.md), each change writes an event. The actor is the operator of the shell.
`flags prune` is the only action that deletes a row. It never deletes a declared flag.

## mail-worker

Runs a worker for the `mail` queue only. Use it when you want mail capacity that is separate from the other jobs.
The standard `cmd/worker` program also sends mail, so most apps do not need this command.
See [Mail](../mail.md).

<!-- doctest: skip -->
```console
$ go run ./cmd/api mail-worker
```

## The worker program

With the jobs module, your app also has a second program, `api/cmd/worker`. It runs all background jobs.
It is not a command of `cmd/api`. `spinup dev` starts it for you. A deploy runs it as the `worker` service.
See [Background jobs](../build/jobs.md).

## Settings from the environment

The API reads its settings from environment variables. The full list is in [The kit library](kit.md#config).
The ones you meet most:

| Variable | Default | What it does |
| --- | --- | --- |
| `DATABASE_URL` | none | The PostgreSQL connection string. `DATABASE_URL_FILE` reads it from a file. |
| `PORT` | `8080` | The HTTP port. |
| `SPINUP_ENV` | `production` | If it is unset or `dev`, the program also reads the `.env` file. `dev` turns on the `/_dev` page. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `APIKEYS_FLUSH` | `5s` | With the `apikeys` module: how often a process writes its day counts to the database. A longer time lets a user go further over the day limit. |
| `FLAGS_REFRESH` | `10s` | With the `flags` module: how often a process reads the flags from the database. The lowest value is `1s`. |

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `cannot run command "x": unknown command` | The module for the command is off. | Run `spinup module add <name>`, or use a listed command. |
| `DATABASE_URL` error | The database URL is missing. | Run from `api/` with a `.env` file, or export `DATABASE_URL`. |
| `user create: --password-stdin is required` | The command does not prompt. | Pipe the password in and add `--password-stdin`. |
| `the change would remove the last account with role` | The command would leave no administrator. | Give the role to another account first. |
| `audit prune: --yes is required` | A prune is permanent. | Check the date, then add `--yes`. |
| `healthcheck: ... connection refused` | The server is not running. | Start the server, or read the container logs. |

## See also

- [Accounts and sign-in](../auth.md): the `user` command in use
- [Audit trail](../audit.md): the `audit` command in use
- [Background jobs](../build/jobs.md): the worker and its queue
- [The db command guide](../commands/db.md): `spinup db migrate` and related commands
- [The kit library](kit.md): the packages behind these commands
