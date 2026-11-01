---
title: Global flags
parent: Reference
nav_order: 2
---

# Global flags

Every spinup command accepts the same ten flags. Put them before or after the command name.
This page also lists the exit codes and the environment variables that spinup reads.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ spinup routes -C todo --json
[{"method":"GET","path":"/healthz","source":"kit"},{"method":"GET","path":"/readyz","source":"kit"},{"method":"GET","path":"/version","source":"kit"}]
```

The second command runs in the `todo` folder and writes JSON.

## The flags

| Flag | Short | Default | What it does | Example |
| --- | --- | --- | --- | --- |
| `--dir <path>` | `-C` | current folder | Runs the command in another project folder. spinup looks up the tree for `spinup.toml`. | `spinup doctor -C ~/code/todo` |
| `--env <name>` | | `default_env` | Picks the [environment](../deploy/environments.md) for a command that touches a server. | `spinup deploy --env staging` |
| `--yes` | `-y` | off | Answers yes to prompts. It does not confirm a [protected environment](#protected-environments) outside CI. | `spinup upgrade --yes` |
| `--confirm-env <name>` | | none | Confirms a protected environment without a terminal. | `spinup deploy --env production --confirm-env production` |
| `--non-interactive` | | off | Never prompts. A missing answer fails the command. | `spinup new todo --non-interactive` |
| `--json` | | off | Writes JSON instead of text. Errors become JSON too. | `spinup routes --json` |
| `--dry-run` | | off | Shows what the command would change and writes nothing. | `spinup new todo --dry-run` |
| `--verbose` | | off | Writes extra detail. | `spinup upgrade --plan --verbose` |
| `--no-color` | | off | Turns color off. | `spinup doctor --no-color` |
| `--offline` | | off | Never downloads template versions. Use the cache only. | `spinup upgrade --plan --offline` |

Some commands have their own flag with the same name as a global one. Then the command page explains the difference.
`--diff` and `--force` are not global flags. Only the generators and a few other commands take them.

### Preview a change with --dry-run

Use `--dry-run` before a command that writes files. The output names each file and its class.

```console
$ spinup new shop --api-only --module example.com/shop --no-git --non-interactive --dry-run
...
```

### Read output in a script with --json

`--json` writes one JSON document on standard output. Use it with `jq`.

```console
$ spinup routes -C todo --json
[{"method":"GET","path":"/healthz","source":"kit"},{"method":"GET","path":"/readyz","source":"kit"},{"method":"GET","path":"/version","source":"kit"}]
```

An error in JSON mode has the same three parts as a text error:

```json
{"error":{"what":"Doctor checks failed","why":"a local health check failed","try":"correct the reported failures and run spinup doctor","code":1}}
```

### Protected environments

If an environment has `protected = true`, a risky command asks you to type its name.
Without a terminal, pass `--confirm-env` with the exact name.

<!-- doctest: skip -->
```console
$ spinup deploy --env production --confirm-env production
```

In CI, `CI=true` with `--yes` also confirms. The value of `--confirm-env` must equal the environment name, or the command stops.

## The no-terminal rule

spinup never waits for input that nobody can give.
If there is no terminal, the command does not prompt. It fails and names the flag that you must pass.

spinup decides that there is no terminal in any of these cases:

- You pass `--non-interactive`.
- The environment variable `CI` has a value, such as `CI=true`.
- Standard input or standard output is not a terminal, such as in a pipe.

```console
$ cd todo
$ spinup g service ocr
Error: generator "service" failed: cannot generate service: --lang is required and must be rust, python, or node, got ""
  Try: spinup g service ocr --lang rust
[exit 1]
```

Every prompt has a flag. Pass the flag in scripts and in CI.

## Exit codes

| Code | Meaning | Example |
| --- | --- | --- |
| `0` | The command worked. | |
| `1` | A general failure. | `spinup doctor` reports a failed check. |
| `2` | A wrong command, flag, or argument. | `spinup new --nope` |
| `3` | An upgrade stopped with merge conflicts. | `spinup upgrade` |
| `4` | The project differs from the expected state. | `spinup check` finds drift. A vulnerability scan with HIGH findings also gives `4`. |
| `5` | Another process holds the deploy lock. | `spinup deploy` while a deploy runs. |

```console
$ spinup bogus
Error: Invalid usage
  unknown command "bogus" for "spinup"
  Try: spinup --help
[exit 2]
```

Every error has the same shape. A first line says what failed. An indented line says why. A last line starts with `Try:` and gives the fix.

## Environment variables

These variables change how spinup runs.

| Variable | Default | What it does |
| --- | --- | --- |
| `CI` | not set | A value, such as `true`, turns prompts off. With `--yes`, it also confirms protected environments. It stops the update notice. |
| `NO_COLOR` | not set | Any value turns color off. The same as `--no-color`. |
| `SPINUP_OFFLINE` | not set | `1` is the same as `--offline`. |
| `SPINUP_NO_UPDATE_CHECK` | not set | `1` stops the notice about a newer spinup version. |
| `SPINUP_KEYS_DIR` | `~/.config/spinup/keys` | The folder for the private age keys of your environments. |
| `SPINUP_CREDENTIALS_DIR` | `~/.config/spinup/credentials` | The folder for the stored cloud sign-in. |
| `SPINUP_TOOLS_DIR` | `<user config dir>/spinup/bin` | The folder where `spinup tools install` puts `sops`, `age`, and `tofu`. |
| `SPINUP_BROWSER` | the system browser | The command that opens a sign-in page. spinup adds the URL as the last argument. |
| `SPINUP_SSH_CONFIG` | not set | An SSH config file. spinup passes it to `ssh -F`. |
| `DIGITALOCEAN_ACCESS_TOKEN` | not set | A DigitalOcean token for `spinup deploy digital-ocean`. `DIGITALOCEAN_TOKEN` also works. See [Deploy to DigitalOcean](../deploy-digitalocean.md). |
| `HCLOUD_TOKEN` | not set | A Hetzner Cloud token for `spinup infra`. See [Hetzner Cloud with OpenTofu](../infra.md). |

Two more variables are for people who work on spinup itself:

| Variable | What it does |
| --- | --- |
| `SPINUP_KIT_REPLACE` | A path to a local copy of the kit. `spinup new` and `spinup upgrade` write it into `api/go.mod`. |
| `SPINUP_TEMPLATES_DIR` | A folder of extra template versions. See [cache](../commands/cache.md). |

The generated app reads its own variables. They are in [App binary commands](app-commands.md) and [The kit library](kit.md).

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `could not resolve environment` | The command needs an environment, and there is no `default_env`. | Pass `--env <name>`. |
| `cannot ... in protected environment` | The environment is protected, and there is no terminal. | Pass `--confirm-env <name>`. |
| A command waits for input in CI | The command runs with a terminal attached. | Pass `--non-interactive`, or set `CI=true`. |
| `could not find spinup.toml` | You are outside an app. | Run from the app folder, or pass `-C <path>`. |
| The command shows colors in a log file | The log is a terminal. | Pass `--no-color`, or set `NO_COLOR=1`. |

## See also

- [spinup.toml](configuration.md): `default_env` and the `[env.*]` tables
- [Environments](../deploy/environments.md): protected environments in depth
- [CLI reference](../site/commands/index.md): every command and its flags
- [Doctor checks](doctor-checks.md): what `spinup doctor` checks
