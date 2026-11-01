---
title: spinup backup verify
parent: spinup backup
grand_parent: CLI reference
---

# spinup backup verify

Restore a backup in isolation and check the database

## Use

```text
spinup backup verify [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--label` | `` | backup label (default latest) |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--time` | `` | point in time in RFC 3339 format |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Restore the latest backup in isolation and check it
spinup backup verify

# Check the state at a point in time
spinup backup verify --time 2026-10-31T14:00:00Z
```
