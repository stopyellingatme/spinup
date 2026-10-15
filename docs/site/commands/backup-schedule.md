# spinup backup schedule

Install or remove backup timers on the VM

## Use

```text
spinup backup schedule [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--plan` | `false` | print the schedule without changing the VM |
| `--remove` | `false` | disable and delete backup timers and services |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
