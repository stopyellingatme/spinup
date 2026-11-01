---
title: spinup infra output
parent: spinup infra
grand_parent: CLI reference
---

# spinup infra output

Show infrastructure outputs

## Use

```text
spinup infra output
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
# Show the host address and other outputs
spinup infra output --env production

# Print them as JSON
spinup infra output --json --env production
```
