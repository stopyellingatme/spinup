# spinup maintenance patch

Install available package updates

## Use

```text
spinup maintenance patch [flags]
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
| `--now` | `false` | ignore the maintenance window |
| `--offline` | `false` | never download template versions |
| `--plan` | `false` | list packages without installing them |
| `--reboot` | `false` | reboot when the host requires it |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
