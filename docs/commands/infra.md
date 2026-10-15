# Provision infrastructure

`spinup infra` runs OpenTofu for the `tofu` module.
The module is in `docs/infra.md`.
Each command needs `--env <name>` and the `deploy/infra/tofu/` files.

| Command | Purpose |
| --- | --- |
| `spinup infra plan` | Preview infrastructure changes. |
| `spinup infra apply` | Apply the changes and write the server address to `[env.<name>] host`. |
| `spinup infra output` | Show the `ipv4`, `ipv6`, and `server_id` outputs. |
| `spinup infra destroy` | Remove the resources. Needs `--i-know`. |

```console
$ spinup infra --help
...
  apply       Apply infrastructure changes and update the environment host
  destroy     Destroy the environment infrastructure
  output      Show infrastructure outputs
  plan        Preview infrastructure changes
...
$ spinup infra plan --help
Preview infrastructure changes
...
$ spinup infra output --help
Show infrastructure outputs
...
```

Without a terminal, `apply` and `destroy` need `--yes`.
The `--dry-run` flag runs a plan and does not change the host entry.
