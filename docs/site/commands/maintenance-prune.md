---
title: spinup maintenance prune
parent: spinup maintenance
grand_parent: CLI reference
---

# spinup maintenance prune

Remove unused app images and build cache

## Use

```text
spinup maintenance prune [flags]
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
| `--plan` | `false` | list images without removing them |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# List the images that would be removed
spinup maintenance prune --plan --env production

# Remove unused images and build cache
spinup maintenance prune --env production
```
