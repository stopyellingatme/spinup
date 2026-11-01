---
title: spinup maintenance vacuum
parent: spinup maintenance
grand_parent: CLI reference
---

# spinup maintenance vacuum

Vacuum the production database

## Use

```text
spinup maintenance vacuum [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--full` | `` | run VACUUM FULL on one table |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--now` | `false` | ignore the maintenance window |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Vacuum the production database
spinup maintenance vacuum --env production

# Run VACUUM FULL on one table, outside the window
spinup maintenance vacuum --full tickets --now --env production
```
