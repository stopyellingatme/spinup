# spinup server

Prepare, audit, and access a VM

## Use

```text
spinup server
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
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Subcommands

- [`spinup server audit`](server-audit.md) — Report unsafe settings on the host
- [`spinup server bootstrap`](server-bootstrap.md) — Prepare a Debian or Ubuntu host
- [`spinup server logs`](server-logs.md) — Read service logs from the host
- [`spinup server ssh`](server-ssh.md) — Open an interactive shell on the host
