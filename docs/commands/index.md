---
title: Command guides
nav_order: 7
has_children: true
has_toc: false
---

# Command guides

Each guide shows one command, or one group of commands, with examples you can copy.
Start with the task you want to do. The table of each group says which guide to read.

```console
$ spinup --help
...
```

Every command also has a short help text (`spinup <command> --help`).
The [CLI reference](../site/commands/index.md) lists every flag of every command. Those pages are generated from the code.

## Create and run

| I want to ... | Read |
| --- | --- |
| Make a new app | [new](new.md) |
| Start the app on my machine | [dev](dev.md) |
| Run the tests | [test](test.md) |
| Build the binary or the container images | [build](build.md) |
| See which HTTP routes the API serves | [routes](routes.md) |
| Check my tools and my setup | [doctor](doctor.md) |
| Install `sops`, `age`, and `tofu` | [tools](tools.md) |
| Print the spinup version | [version](version.md) |

## Generate

| I want to ... | Read |
| --- | --- |
| See every generator | [generate](generate.md) |
| Add a CRUD API for a table | [generate resource](generate-resource.md) |
| Add a web page | [generate page](generate-page.md) |
| Add a UI component to the web app | [generate component](generate-component.md) |
| Rebuild the web client from the API | [generate client](generate-client.md) |
| Bundle the OpenAPI files and the Go types | [generate openapi](generate-openapi.md) |
| Add a background job | [generate job](generate-job.md) |
| Add a mailer and mail templates | [generate mailer](generate-mailer.md) |
| Set the roles of the app | [generate roles](generate-roles.md) |
| Add an audit trail to a table | [generate audit](generate-audit.md) |
| Add a sidecar service | [generate service](generate-service.md) |
| Add a Grafana dashboard or an alert | [generate dashboard](generate-dashboard.md) |
| Remove what a generator made | [destroy](destroy.md) |

## Database

| I want to ... | Read |
| --- | --- |
| Apply, roll back, or create migrations, and open `psql` | [db](db.md) |
| Find unsafe changes in a migration | [db lint](db-lint.md) |

## Project and upgrade

| I want to ... | Read |
| --- | --- |
| Move my app to a newer spinup | [upgrade](upgrade.md) |
| Find files that drifted from the template | [check](check.md) |
| See how my files differ from the template | [diff](diff.md) |
| Stop spinup from managing a file | [eject](eject.md) |
| Bring back a deleted or edited template file | [restore](restore.md) |
| Turn an optional feature on | [module](module.md) |
| Manage the downloaded template versions | [cache](cache.md) |
| Update the spinup program | [self-update](self-update.md) |
| Print the compatibility data or rebuild the CLI reference | [docs](docs.md) |

## Deploy

| I want to ... | Read |
| --- | --- |
| Deploy to my own server | [deploy](deploy.md) |
| Deploy to DigitalOcean | [deploy digital-ocean](deploy-digital-ocean.md) |
| Create a Hetzner Cloud server | [infra](infra.md) |
| Go back to an earlier release | [rollback](rollback.md) |
| List or delete old releases | [releases](releases.md) |
| Manage encrypted secrets | [secrets](secrets.md) |
| Prepare or check a server | [server](server.md) |
| Generate and check the CI files | [ci](ci.md) |

## Operate

| I want to ... | Read |
| --- | --- |
| Back up the database | [backup](backup.md) |
| Restore, verify, or prune backups | [backup restore](backup-restore.md) |
| Run backups on a schedule | [backup schedule](backup-schedule.md) |
| Look at logs, metrics, and alerts | [monitor](monitor.md) |
| Keep the server healthy | [maintenance](maintenance.md) |
| Upgrade PostgreSQL | [maintenance pg](maintenance-pg.md), [in place](maintenance-pgupgrade.md) |

## Plugins

| I want to ... | Read |
| --- | --- |
| Add, trust, or check a plugin | [plugin](plugin.md) |
| Write my own plugin | [plugin new](plugin-new.md) |

## Flags that every command accepts

These flags work on every command. They come before or after the command name.

| Flag | What it does | Example |
| --- | --- | --- |
| `--dir <directory>`, `-C` | Run in this app folder. | `spinup check -C ~/code/todo` |
| `--dry-run` | Show the changes and write nothing. | `spinup new todo --dry-run` |
| `--json` | Print JSON. | `spinup routes --json` |
| `--env <name>` | Pick the target environment. | `spinup deploy --env production` |
| `--confirm-env <name>` | Type the name of a protected environment without a terminal. | `spinup deploy --env production --confirm-env production` |
| `--yes`, `-y` | Answer yes to prompts. | `spinup upgrade --yes` |
| `--non-interactive` | Never prompt. | `spinup new todo --non-interactive` |
| `--offline` | Never download template versions. | `spinup check --offline` |
| `--verbose` | Print extra detail. | `spinup dev --verbose` |
| `--no-color` | Turn off color. | `spinup doctor --no-color` |

A command ignores a flag that does not apply to it. [Global flags](../reference/global-flags.md) describes each one.
