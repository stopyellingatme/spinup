---
title: infra — create the server with OpenTofu
parent: Command guides
---

# infra — create the server with OpenTofu

`spinup infra` runs OpenTofu for the `tofu` module. It creates, shows, and deletes the Hetzner Cloud server of an environment.
Use it when you want spinup to provision the VM before the first [deploy](deploy.md).

<!-- doctest: skip -->
```console
$ export HCLOUD_TOKEN=<your token>
$ spinup infra plan --env staging
$ spinup infra apply --env staging
```

`apply` writes the new server address to `host` in `[env.staging]` of `spinup.toml`.
Then you run `spinup server bootstrap` and `spinup deploy`.

Every subcommand needs `--env <name>` (a lowercase name), the `deploy/infra/tofu/` files, and `HCLOUD_TOKEN` in your environment.
The files come from the `tofu` module, and the token is a Hetzner Cloud token with write access.
If a piece is missing, the command stops and names the fix:

```console
$ spinup new todo --api-only --modules deploy,tofu --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ printf '\n[env.staging]\nhost = ""\nuser = "deploy"\n' >> spinup.toml
$ spinup infra plan --env staging
Error: run OpenTofu: HCLOUD_TOKEN is missing
  Try: export HCLOUD_TOKEN and run spinup tools install tofu
[exit 1]
```

{: .note }
This page is the command guide. The module, its variables, and the Hetzner setup are in [Hetzner Cloud with OpenTofu](../infra.md).

## Preview the changes

`plan` shows what OpenTofu would create or change. It changes nothing.

<!-- doctest: skip -->
```console
$ spinup infra plan --env staging
```

## Create the server

`apply` creates the server, the firewall, the SSH key, and the volume.
In a terminal, it asks you to approve the plan.
Without a terminal, you must pass `--yes`.

<!-- doctest: skip -->
```console
$ spinup infra apply --env staging
$ spinup infra apply --env staging --yes
```

To see the plan without a change to the server or to `spinup.toml`, add `--dry-run`. The command then runs a plan.

<!-- doctest: skip -->
```console
$ spinup infra apply --env staging --dry-run
```

## Show the server address

`output` prints the `ipv4`, `ipv6`, and `server_id` values.
Add the global `--json` flag for JSON.

<!-- doctest: skip -->
```console
$ spinup infra output --env staging
```

## Destroy the server

`destroy` deletes every resource of the environment: the server, the firewall, the SSH key, and the volume.

{: .warning }
The server, its volume, and the database on it are lost. Run `spinup backup` first.

The command refuses without `--i-know`:

```console
$ spinup infra destroy --env staging
Error: destroy infrastructure refused: --i-know is missing
  Try: review spinup infra plan, then pass --i-know
[exit 1]
```

<!-- doctest: skip -->
```console
$ spinup infra destroy --env staging --i-know
```

A protected environment also asks you to type its name.
Without a terminal, `destroy` needs `--yes` as well. To preview a destroy, add `--dry-run`.

<!-- doctest: skip -->
```console
$ spinup infra destroy --env staging --dry-run
```

## Run in CI

CI has no terminal, so pass `--yes` to `apply` and `destroy`.
`destroy` on a protected environment also needs `--confirm-env <name>`.

<!-- doctest: skip -->
```console
$ spinup infra apply --env production --yes
$ spinup infra destroy --env production --i-know --yes --confirm-env production
```

## Reference

| Subcommand | Purpose | Example |
| --- | --- | --- |
| `plan` | Preview the changes. | `spinup infra plan --env staging` |
| `apply` | Apply the changes and write `host`. | `spinup infra apply --env staging --yes` |
| `output` | Show `ipv4`, `ipv6`, and `server_id`. | `spinup infra output --env staging --json` |
| `destroy` | Delete the resources. | `spinup infra destroy --env staging --i-know` |

| Flag | Meaning | Example |
| --- | --- | --- |
| `--i-know` | Confirm the destruction of the resources. | `spinup infra destroy --i-know` |
| `--yes` | Approve without a prompt. | `spinup infra apply --yes` |
| `--dry-run` | Run a plan instead of a change. | `spinup infra apply --dry-run` |

## See also

- [Hetzner Cloud with OpenTofu](../infra.md): the module and its variables
- [server](server.md): prepare the new VM
- [tools](tools.md): install OpenTofu with `spinup tools install tofu`
- Flag reference: [infra](../site/commands/infra.md), [plan](../site/commands/infra-plan.md), [apply](../site/commands/infra-apply.md), [output](../site/commands/infra-output.md), [destroy](../site/commands/infra-destroy.md)
