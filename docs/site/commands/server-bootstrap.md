# spinup server bootstrap

Prepare a Debian or Ubuntu host

## Use

```text
spinup server bootstrap [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--accept-new-host-key` | `false` | accept a previously unknown SSH host key |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--plan` | `false` | probe the host and print changes without applying them |
| `--print-cloud-init` | `false` | print the rendered cloud-init with the operator public key |
| `--ssh-key` | `~/.ssh/id_ed25519.pub` | operator public key for the deploy user |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
