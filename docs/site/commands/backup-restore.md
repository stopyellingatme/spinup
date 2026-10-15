# spinup backup restore

Restore a backup into a new or live database

## Use

```text
spinup backup restore [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--i-know` | `false` | allow replacement of the live database |
| `--into` | `` | restore destination: new or in-place |
| `--json` | `false` | write JSON output |
| `--label` | `` | backup label (default latest) |
| `--no-color` | `false` | turn off color |
| `--no-safety-backup` | `false` | skip the safety backup before replacement |
| `--non-interactive` | `false` | never prompt |
| `--now` | `false` | allow restoration outside the maintenance window |
| `--offline` | `false` | never download template versions |
| `--plan` | `false` | print the restore steps and change nothing |
| `--time` | `` | point in time in RFC 3339 format |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
