---
title: spinup infra apply
parent: spinup infra
grand_parent: CLI reference
---

# spinup infra apply

Apply infrastructure changes and update the environment host

## Use

```text
spinup infra apply
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
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Create or update the server and volume
spinup infra apply --env production

# Show what would happen without changes
spinup infra apply --dry-run --env production
```
