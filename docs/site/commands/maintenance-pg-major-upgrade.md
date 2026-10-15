# spinup maintenance pg-major-upgrade

Upgrade the PostgreSQL major with a verified dump and retained old volume

## Use

```text
spinup maintenance pg-major-upgrade [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--cleanup` | `false` | remove the retained old volume after a completed upgrade |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--i-know` | `false` | accept app downtime or permanent removal of the old volume |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--now` | `false` | override the maintenance window |
| `--offline` | `false` | never download template versions |
| `--plan` | `false` | print the steps without changes |
| `--to` | `0` | target PostgreSQL major: 16, 17, or 18 |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
