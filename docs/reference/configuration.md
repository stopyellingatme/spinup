---
title: spinup.toml
parent: Reference
nav_order: 1
---

# spinup.toml

`spinup.toml` is the settings file of your app. It sits in the root of the app.
You edit it by hand. spinup commands read it, and a few commands add to it.

This is a complete file with every table in use:

```toml
behavior_version = 1
modules = ["ci", "deploy", "auth", "backups", "monitoring"]
default_env = "staging"

[app]
name = "todo"
module = "example.com/todo"

[auth]
roles = ["manager", "staff"]
admin_role = "manager"
default_role = "staff"

[monitoring]
profile = "lite"
alert_email = "ops@example.com"

[backups]
backend = "pgbackrest"
retention = "4"

[maintenance]
window = "Sun 02:00-05:00"

[env.staging]
host = "203.0.113.10"
user = "deploy"
public_url = "https://staging.example.com"

[env.production]
host = "203.0.113.20"
user = "deploy"
public_url = "https://todo.example.com"
protected = true

[[services]]
name = "ocr"
port = 8080
dev_port = 8101
health = "/healthz"
```

A new app has a much smaller file. It holds only the keys that every app needs:

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cat todo/spinup.toml
# Select the kit behavior defaults.
behavior_version = 1
# Enable these optional modules.
modules = [ "ci", "deploy" ]

[app]
# Name the application.
name = "todo"
# Set the root Go module path.
module = "example.com/todo"

[monitoring]
# Pick the monitoring stack: lite, full, or none.
profile = "lite"
```

A key that you leave out takes its default. The tables below list each default.

{: .note }
`spinup.toml` is a scaffold file. `spinup upgrade` keeps your edits to it.
The file `spinup.lock` is different. spinup writes it, and you do not edit it.

## Top-level keys

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `behavior_version` | integer | `1` | Selects the defaults of the [kit](kit.md). Raise it on purpose. See [Behavior versions](../policy/behavior-version.md). |
| `modules` | list of strings | `["ci", "deploy"]` for a new app | The optional modules that are on. Change it with `spinup module add`, not by hand. |
| `default_env` | string | none | The environment that a command uses when you leave out `--env`. It must name an `[env.*]` table. |

The modules are `apikeys`, `audit`, `auth`, `backups`, `ci`, `deploy`, `flags`, `jobs`, `mail`, `monitoring`, `secrets`, and `tofu`.
Run `spinup module list` to see which are on.

```toml
behavior_version = 1
modules = ["ci", "deploy", "auth"]
default_env = "staging"
```

If `behavior_version` is missing or lower than 1, spinup uses 1.

## [app]

The name and the Go module path of the app. Both are required.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `name` | string | none | The app name. It uses lowercase letters, digits, and hyphens. It starts with a letter and has at most 63 characters. It names the folder on the VM, `/opt/<name>`. |
| `module` | string | none | The Go module path, such as `example.com/todo`. |

```toml
[app]
name = "todo"
module = "example.com/todo"
```

## [auth]

The roles of the [accounts module](../build/roles.md). You can leave the whole table out.
After you change it, run `spinup g roles` to rewrite `api/internal/roles/roles.gen.go`.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `roles` | list of strings | `["manager", "staff"]` | The role names. The list holds 1 to 16 unique names. |
| `admin_role` | string | `"manager"` | The role of an administrator. It must be in `roles`. The kit keeps at least one account with this role. |
| `default_role` | string | `"staff"` | The role of a new account. It must be in `roles`. |

A role name matches `^[a-z][a-z0-9_]{0,31}$`. It has no double underscore and no trailing underscore.

```toml
[auth]
roles = ["admin", "front_desk", "viewer"]
admin_role = "admin"
default_role = "viewer"
```

## [apikeys]

The settings of the [API keys module](../build/api-keys.md). You can leave out the table.
After you change it, run `spinup g apikeys` to rewrite `api/internal/apikeys/apikeys.gen.go` and `web/src/lib/apikeys.gen.ts`.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `prefix` | string | `key` | The start of each new token. It matches `^[a-z][a-z0-9]{1,15}$`. Existing keys keep their old prefix and keep working. |
| `default_plan` | string | `free` | The plan of a user with no plan row. It must name a declared plan. |
| `max_keys` | integer | `10` | The most keys that a user can have. The range is 1 to 100. |

## [apikeys.plans.NAME]

Use one table for each plan. A plan name matches `^[a-z][a-z0-9_]{0,31}$`. The app has 1 to 16 plans.
Without any plan table, the app has one plan, `free`, with 60 requests a minute and 1000 a day.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `per_minute` | integer | none | The requests each minute for all keys of a user, from 1 to 100000. |
| `per_day` | integer | none | The requests each UTC day for all keys of a user, from 1 to 100000000. It is at least `per_minute`. |

An unknown key in either table is an error.

```toml
[apikeys]
prefix = "todo"
default_plan = "free"

[apikeys.plans.free]
per_minute = 60
per_day = 1000

[apikeys.plans.pro]
per_minute = 600
per_day = 100000
```

The environment variable `APIKEYS_FLUSH` sets how often each API process writes its day counts to the database. The default is `5s`. See [App binary commands](app-commands.md#settings-from-the-environment).

## [flags.KEY]

The [feature flags](../build/flags.md) of the app. Use one table for each flag. You can leave them out.
After you change them, run `spinup g flags` to rewrite `api/internal/flags/flags.gen.go` and `web/src/lib/flags.gen.ts`.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `description` | string | none | What the flag does. It is required, with 1 to 200 characters and no line break. |
| `default` | bool | `false` | The answer before the first load from the database, and the first value of a new flag. |
| `public` | bool | `false` | If true, the web pages and `GET /api/flags` get the answer. |
| `owner` | string | none | Free text, up to 254 characters. The app does not use it. |
| `expires` | date | none | `YYYY-MM-DD`. The doctor warns after this day. |

A key matches `^[a-z][a-z0-9_]{0,47}$`. It has no double underscore and no trailing underscore.
The app has 0 to 64 flags. An unknown key in a flag table is an error.

```toml
[flags.new_checkout]
description = "The new checkout flow"
public = true
expires = "2027-03-31"
```

The environment variable `FLAGS_REFRESH` sets how often each API process reads the flags. The default is `10s`. See [App binary commands](app-commands.md#settings-from-the-environment).

## [monitoring]

The settings of the [monitoring pack](../monitoring.md). Only `profile` is needed.
The other keys turn on alerts and set how long data stays on the VM.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `profile` | string | `"lite"` | The monitoring stack: `lite`, `full`, or `none`. It must match the profile that spinup rendered. |
| `heartbeat_url` | string | none | A URL that Grafana calls every 5 minutes. An outside service raises an alarm when the calls stop. |
| `alert_email` | string | none | The address that receives alert emails. It needs `smtp_host`. Most relays also need `smtp_user` and `smtp_from`. |
| `smtp_host` | string | none | The address and port of the SMTP relay, such as `smtp.example.com:587`. |
| `smtp_user` | string | none | The user name for the SMTP relay. |
| `smtp_from` | string | none | The sender address of alert emails. |
| `ntfy_url` | string | none | A [ntfy](https://ntfy.sh) topic URL that receives alerts. |
| `prometheus_retention` | string | `"30d"` | How long the VM keeps metrics. |
| `prometheus_retention_size` | string | `"5GB"` | The disk space limit for metrics. |
| `tempo_retention` | string | `"336h"` | How long the VM keeps traces. |

```toml
[monitoring]
profile = "full"
alert_email = "ops@example.com"
smtp_host = "smtp.example.com:587"
smtp_user = "alerts"
smtp_from = "alerts@example.com"
prometheus_retention = "14d"
```

The SMTP password is a secret. Put it in `SMTP_PASSWORD` in `secrets/<env>.enc.yaml`, not in this file.
A Slack webhook is a secret too. Put it in `SLACK_WEBHOOK_URL`.

{: .warning }
If you change `profile`, `spinup deploy` stops with an error. The profile in `spinup.toml` must match the one in `spinup.lock`. To change the services, re-render the monitoring pack. See [Monitoring](../monitoring.md).

## [backups]

The settings of [backups](../operate/index.md). All keys are optional.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `backend` | string | `"pgbackrest"` | The backup tool: `pgbackrest`, `pgdump`, or `walg`. See [pg_dump](../backups-pgdump.md) and [WAL-G](../backups-walg.md). |
| `retention` | string | `"4"`, or `"7"` for `pgdump` | The number of full backups to keep. Write a whole number from 1 to 999 in quotes. |
| `schedule_full` | string | `"Sun *-*-* 03:00:00"` | When the full backup runs. It is a systemd `OnCalendar` value. |
| `schedule_diff` | string | `"Mon..Sat *-*-* 03:00:00"` | When the differential backup runs. |
| `schedule_verify` | string | `"Sat *-*-* 04:00:00"` | When the verify job runs. |

```toml
[backups]
backend = "pgbackrest"
retention = "8"
schedule_full = "Sun *-*-* 02:00:00"
```

The schedules take effect when you run `spinup backup schedule`. Times are on the VM clock.

## [maintenance]

Limits risky database work to a time window. The window limits an in-place restore, every PostgreSQL upgrade, `vacuum --full`, and `patch --reboot`.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `window` | string | `""` | The window in UTC. The format is `<day> HH:MM-HH:MM`. An empty value means any time. |

The day is a name such as `Sun`, a range such as `Mon-Fri`, or `*` for every day.
A window can cross midnight, such as `Mon-Fri 22:00-02:00`.

```toml
[maintenance]
window = "Sun 02:00-05:00"
```

If you run the work outside the window, the command stops. Pass `--now` to run it anyway.

## [env.NAME]

One table for each [environment](../deploy/environments.md). The table name is the environment name.
It uses lowercase letters, digits, and hyphens, and it starts with a letter.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `host` | string | none | The address of the VM. |
| `user` | string | none | The SSH user on the VM. |
| `public_url` | string | none | The address that users open. The smoke check after a deploy uses it. |
| `protected` | boolean | `false` | If `true`, risky commands ask you to type the environment name. |
| `port` | integer | `22` | The SSH port. |
| `transport` | string | `"ssh"` | How images reach the VM: `ssh` (copy over SSH) or `registry` (push, then pull by digest). |
| `host_keys` | list of strings | none | The SSH host keys to trust, such as `"ssh-ed25519 AAAA..."`. `spinup deploy digital-ocean` writes them. If the list is empty, SSH uses your `known_hosts` file. |

```toml
[env.production]
host = "203.0.113.20"
user = "deploy"
public_url = "https://todo.example.com"
protected = true
```

If you leave out `--env`, spinup uses `default_env`. If there is no `default_env` and one environment exists, spinup uses that one.

## deploy/environments/NAME.toml

The deploy settings that do not belong in `spinup.toml` live in one file for each environment.
The file name must match the environment name. A new app has `staging.toml` and `production.toml`.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `smoke_urls` | list of strings | `[]` | Paths that the deploy requests after the switch. Each must answer with a 2xx status. The deploy always checks `/healthz` and `/version` too. |
| `[replicas]` | table of integers | `1` for each service | How many containers of a service run in one version. The keys are `api`, `web`, and `worker`. |
| `[resources.SERVICE]` | table | no limit | The `cpus` and `memory` limits of one service, such as `api`, `web`, `postgres`, or a sidecar service. |

```toml
smoke_urls = ["/", "/login"]

[replicas]
api = 2
web = 2

[resources.api]
cpus = "1.0"
memory = "512m"
```

`cpus` is a fraction of the CPUs of the VM, written as a string. `memory` uses Docker units, such as `512m` or `1g`.
During a switch both versions run, so the VM needs memory for two sets. See [Environments](../deploy/environments.md).

## [[plugins]]

One table for each [plugin](../plugins.md). `spinup plugin add` writes these tables. Do not edit them by hand.

| Key | Type | What it does |
| --- | --- | --- |
| `name` | string | The plugin name. |
| `source` | string | Where spinup fetches the plugin: `path:<directory>` or `git:<url>`. |
| `version` | string | The plugin version that you approved. |

```toml
[[plugins]]
name = "notes"
source = "path:../plugins/notes"
version = "0.1.0"
```

## [[services]]

One table for each [sidecar service](../services.md). `spinup g service` writes these tables.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `name` | string | none | The service name. It uses 1 to 20 lowercase letters and digits and starts with a letter. spinup reserves some names, such as `api`, `web`, and `postgres`. A Go keyword is not valid. |
| `port` | integer | none | The port that the container listens on. |
| `dev_port` | integer | none | The port on your machine in `spinup dev`. Each service needs its own. Do not use 8080, 5173, 5432, 1025, 8025, 3000, 4317, or 4318. |
| `health` | string | none | An HTTP path that answers 200 when the service is ready. It starts with `/`. |
| `openapi` | string | `""` | An OpenAPI file, relative to `services/<name>/`. With a file, spinup generates a Go client. |
| `healthcheck` | list of strings | none | The Compose health check command in exec form. |
| `secrets` | list of strings | none | The secret names that the service can read in `/run/secrets`. |
| `env` | table of strings | none | Variables that are not secret. A key cannot start with `SPINUP_` or `OTEL_`. |
| `db` | boolean | `false` | If `true`, the service joins the database network. |
| `gpus` | string | `""` | `"all"` or a count such as `"1"`. An empty value means no GPU. |
| `public` | string | `""` | A URL path, such as `/svc/ocr`, that the edge routes to the service. An empty value keeps the service internal. |
| `auth` | string | `"none"` | The check on a public route: `none` or `session`. It needs `public`. `session` needs the auth module. |
| `memory` | string | none | The default memory limit in a deploy, such as `3g`. |
| `tmp_size` | string | Docker default | The size of `/tmp` in the container, such as `256m`. |

```toml
[[services]]
name = "ocr"
port = 8080
dev_port = 8101
health = "/healthz"
public = "/svc/ocr"
auth = "session"
env = { LANGUAGE = "eng" }
```

## Check the file

spinup checks `spinup.toml` before a deploy, a monitoring command, a maintenance command, and a restore.
An error names the key and ends with a `Try:` line.

```console
$ cd todo
$ printf '\n[backups]\nbackend = "tape"\n' >> spinup.toml
$ spinup deploy --env staging --dry-run
Error: Deploy preflight failed
  spinup.toml is not valid: backups backend "tape" must be pgbackrest, pgdump, or walg
  Try: correct spinup.toml
[exit 1]
```

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `could not resolve environment` | The `--env` name has no `[env.*]` table, and `default_env` is not set. | Add the table, or pass `--env`. |
| `default environment "x" does not exist` | `default_env` names a missing table. | Correct `default_env`. |
| `app name "x" must match ...` | The name has capitals, spaces, or an underscore. | Use lowercase letters, digits, and hyphens. |
| `monitoring profile "x" must be lite, full, or none` | The profile is misspelled. | Use one of the three values. |
| `auth admin_role "x" must be in roles` | The admin role is not in `roles`. | Add it to `roles`, or choose another role. |
| `service "x": auth = "session" needs the auth module` | A service checks sessions, but the app has no auth module. | Run `spinup module add auth`, or set `auth = "none"`. |

## See also

- [Environments](../deploy/environments.md): the `[env.*]` tables and the settings files in depth
- [Roles](../build/roles.md): the `[auth]` table in use
- [Sidecar services](../services.md): the `[[services]]` tables in use
- [Behavior versions](../policy/behavior-version.md): when to raise `behavior_version`
- [Global flags](global-flags.md): `--env` and `--dir`
