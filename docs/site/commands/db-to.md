---
title: spinup db to
parent: spinup db
grand_parent: CLI reference
---

# spinup db to

Migrate up or down to a version

## Use

```text
spinup db to <version> [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--i-know` | `false` | acknowledge that a move down can lose data |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Migrate up to version 7
spinup db to 7

# Move down to version 3 (asks you to confirm data loss)
spinup db to 3 --i-know
```
