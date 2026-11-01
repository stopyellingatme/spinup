---
title: spinup rollback
parent: CLI reference
---

# spinup rollback

Switch to a stored release without changing the database

## Use

```text
spinup rollback [flags]
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
| `--to` | `` | select a stored release ID instead of the previous release |
| `--verbose` | `false` | write extra detail |
| `--with-db-plan` | `false` | print manual database recovery instructions without restoring data |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Go back to the previous release
spinup rollback --env production

# Go back to a chosen release
spinup rollback --to 20261101T093000Z-ab12cd3 --env production

# Print the steps to recover the database by hand
spinup rollback --with-db-plan --env production
```
