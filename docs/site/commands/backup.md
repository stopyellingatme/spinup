# spinup backup

Back up the production database with pgBackRest

## Use

```text
spinup backup
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
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

- [`spinup backup list`](backup-list.md) — List the backups in the repository
- [`spinup backup now`](backup-now.md) — Take a backup now and print its label, size, and duration
- [`spinup backup prune`](backup-prune.md) — Apply backup retention and list the remaining backups
- [`spinup backup restore`](backup-restore.md) — Restore a backup into a new or live database
- [`spinup backup schedule`](backup-schedule.md) — Install or remove backup timers on the VM
- [`spinup backup status`](backup-status.md) — Show the repository status and the last backup
- [`spinup backup verify`](backup-verify.md) — Restore a backup in isolation and check the database
