# spinup maintenance pg-minor-upgrade

Upgrade PostgreSQL to the pinned minor after a backup

## Use

```text
spinup maintenance pg-minor-upgrade [flags]
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
| `--now` | `false` | override the maintenance window |
| `--offline` | `false` | never download template versions |
| `--plan` | `false` | print the steps without changes |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
