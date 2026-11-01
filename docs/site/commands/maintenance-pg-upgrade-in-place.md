---
title: spinup maintenance pg-upgrade-in-place
parent: spinup maintenance
grand_parent: CLI reference
---

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
| `--confirm-env` | `` | type the protected environment name without a terminal |
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

## Examples

```sh
# Print the steps first
spinup maintenance pg-upgrade-in-place --to 18 --plan --env production

# Upgrade in place (this causes downtime)
spinup maintenance pg-upgrade-in-place --to 18 --i-know --env production

# Delete the old data directory after you verify the app
spinup maintenance pg-upgrade-in-place --cleanup --i-know --env production
```
