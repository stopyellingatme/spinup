# spinup maintenance pg-upgrade-in-place

Upgrade PostgreSQL with pg_upgrade --link and retain the old directory

## Use

```text
spinup maintenance pg-upgrade-in-place [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--cleanup` | `false` | remove the old data directory after verification |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--i-know` | `false` | accept downtime and permanent changes to the old cluster |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--now` | `false` | override the maintenance window |
| `--offline` | `false` | never download template versions |
| `--plan` | `false` | print the steps without changes |
| `--to` | `0` | target PostgreSQL major: 17 or 18 |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
