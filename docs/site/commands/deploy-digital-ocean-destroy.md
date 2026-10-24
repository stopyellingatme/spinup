# spinup deploy digital-ocean destroy

Delete the droplet, the firewall, and the DNS record of the environment

Delete only the resources that carry the environment tag and the IDs in spinup.toml.
The database on the droplet is lost. Run spinup backup first. The account SSH key and the local secrets stay.

## Use

```text
spinup deploy digital-ocean destroy [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--accept-new-host-key` | `false` | accept the SSH host key of a new VM |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--i-know` | `false` | accept that the command deletes the droplet and its database |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
