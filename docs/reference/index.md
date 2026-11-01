---
title: Reference
nav_order: 9
has_children: true
has_toc: false
---

# Reference

Use these pages to look up a key, a flag, a check, or a policy. They list facts and give a short example for each.
To learn how to do a task, read the guides in [Build your app](../build/index.md) and [Deploy](../deploy/index.md) first.

```console
$ spinup --help
...
```

## Look something up

| I want to ... | Read |
| --- | --- |
| Find a key of `spinup.toml` and its default | [spinup.toml](configuration.md) |
| See what `--dry-run`, `--json`, `--env`, or `--yes` do | [Global flags](global-flags.md) |
| Know the exit codes or the environment variables | [Global flags](global-flags.md) |
| Fix a failed `spinup doctor` check | [Doctor checks](doctor-checks.md) |
| Run `migrate`, `user`, or `audit` in my app | [App binary commands](app-commands.md) |
| Find the kit package for auth, jobs, mail, or tests | [The kit library](kit.md) |
| Check the Go, Node, and PostgreSQL versions that spinup supports | [Compatibility matrix](../site/compat.md) |
| Find the pinned version of an image | [Pinned stack](../site/stack.md) |
| Look up a flag of one command | [CLI reference](../site/commands/index.md) |

## Policies

| I want to know ... | Read |
| --- | --- |
| Which versions and platforms get fixes, and how far back I can upgrade | [Support policy](../policy/support.md) |
| What changes in the kit can reach my app | [Kit compatibility](../policy/kit-compat.md) |
| When to raise `behavior_version` | [Behavior versions](../policy/behavior-version.md) |
| What I trust when I add a plugin | [Plugin trust](../policy/plugins.md) |
| Which commands work on Windows | [Windows](../policy/windows.md) |

## See also

- [Command guides](../commands/index.md): one guide for each command group, with examples
- [Concepts](../concepts.md): the words that the docs use
- [Troubleshooting](../troubleshooting.md): common errors and their fixes
