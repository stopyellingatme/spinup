# spinup rollback

Switch to a stored release without changing the database

## Use

```text
spinup rollback [flags]
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
| `--to` | `` | select a stored release ID instead of the previous release |
| `--verbose` | `false` | write extra detail |
| `--with-db-plan` | `false` | print manual database recovery instructions without restoring data |
| `-y, --yes` | `false` | answer yes to prompts |
