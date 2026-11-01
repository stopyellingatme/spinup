---
title: Environments
parent: Deploy
nav_order: 3
---

# Environments

An environment is one place you deploy to, such as `staging` or `production`.
Each environment has its own server, secrets, release history, and deploy lock.
You choose one with `--env`.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ cat deploy/environments/staging.toml
...
smoke_urls = [] # an API-only app has no page at /
...
[replicas]
api = 1
...
```

A new app has the settings files for `staging` and `production`.
It has no `[env.*]` table yet. Add one when you have a server.
An environment has two parts: the server address in `spinup.toml`, and the deploy settings in `deploy/environments/<env>.toml`.

## Add an environment

Put the server in `spinup.toml`:

```toml
[env.staging]
host = "203.0.113.10"
user = "deploy"
public_url = "https://staging.example.com"
protected = false

[env.production]
host = "203.0.113.20"
user = "deploy"
public_url = "https://todo.example.com"
protected = true
```

| Key | Meaning | Default |
| --- | --- | --- |
| `host` | The address of the VM. | none |
| `user` | The SSH user. | none |
| `public_url` | The address that users open. The smoke check uses it. | none |
| `protected` | Ask for confirmation before a risky command. | `false` |
| `port` | The SSH port. | `22` |
| `transport` | How images reach the VM: `ssh` or `registry`. | `ssh` |
| `host_keys` | The SSH host keys to trust. The DigitalOcean command writes them. | none |

An environment name uses lowercase letters, digits, and hyphens.
It starts with a letter.

`spinup deploy digital-ocean` and `spinup infra apply` write `host` for you.

## Choose an environment

Pass `--env` to any command that touches a server:

```sh
spinup deploy --env staging
spinup secrets set --env production DATABASE_URL
```

If you leave out `--env`, spinup uses `default_env` from `spinup.toml`.
If there is no `default_env` and only one environment exists, spinup uses that one.
Otherwise the command fails and lists the valid names.

```toml
default_env = "staging"
```

This is the error for an unknown name:

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ spinup deploy --env nope --dir todo
Error: Deploy preflight failed
  could not resolve environment "nope". Valid environments: . Try: pass --env <name>
  Try: pass --env <name> with an environment of spinup.toml
[exit 1]
```

## Set smoke URLs, replicas, and limits

The file `deploy/environments/<env>.toml` holds the deploy settings that are not in `spinup.toml`:

```toml
smoke_urls = ["/", "/login"]

[replicas]
api = 2
web = 2
worker = 1

[resources.api]
cpus = "1.0"
memory = "512m"

[resources.postgres]
cpus = "2.0"
memory = "1g"
```

| Key | Meaning | Example |
| --- | --- | --- |
| `smoke_urls` | Paths that the deploy requests after the switch. Each path starts with `/` and must answer 2xx. `/healthz` and `/version` are always checked. | `["/", "/login"]` |
| `[replicas]` | How many containers of a service run in one version. Every service defaults to `1`, including `worker`. | `api = 2` |
| `[resources.<service>]` | The `cpus` and `memory` limits of `api`, `web`, `postgres`, and each [sidecar service](../services.md). A service with no entry has no limit. | `memory = "512m"` |

During a switch both versions run, so the VM needs memory for two sets of `api` and `web`.
Docker units apply to `memory` (`512m`, `1g`). `cpus` is a fraction of the CPUs of the VM.

A new environment with no settings file fails the deploy and names the file to create.
Copy `staging.toml` to `<env>.toml` to start. `spinup deploy digital-ocean` does this for you.

## Protect an environment

Set `protected = true` on any environment where a mistake is costly.
spinup then asks you to type the environment name before a risky command.
These commands are risky: deploy, rollback, `deploy unlock --force`, `secrets push`, `secrets rotate`, `secrets rekey`, `releases prune`, database changes, restore, PostgreSQL upgrade, and destroy.

<!-- doctest: skip -->
```console
$ spinup deploy --env production
spinup deploy targets the protected environment "production" (203.0.113.20). Type the environment name to continue:
```

Without a terminal, the command fails and names the fix.
Pass `--confirm-env` with the exact name:

```sh
spinup deploy --env production --confirm-env production
```

If the value does not match the environment, the command stops.
In CI, `CI=true` with `--yes` also confirms. The generated deploy workflow does this.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `could not resolve environment` | The name is not in `spinup.toml`, or no default exists. | Add `[env.<name>]`, or pass `--env`. |
| `cannot ... in protected environment` | No terminal and no confirmation. | Pass `--confirm-env <name>`. |
| `--confirm-env has the value ...` | The value differs from the environment. | Pass the exact environment name. |
| `read deploy/environments/<env>.toml` fails | The environment has no settings file. | Copy `deploy/environments/staging.toml` to `<env>.toml`. |
| `smoke URL ... is not a path` | A `smoke_urls` entry does not start with `/`. | Write the path, such as `"/login"`. |
| The deploy never uses my new limits | The file name does not match the environment. | Name the file `deploy/environments/<env>.toml`. |

## See also

- [Deploy to your own server](../deploy.md)
- [Secrets](secrets.md)
- [Global flags](../reference/global-flags.md)
- [Configuration reference](../reference/configuration.md)
