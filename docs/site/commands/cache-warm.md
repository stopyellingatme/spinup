---
title: spinup cache warm
parent: spinup cache
grand_parent: CLI reference
---

# spinup cache warm

Download and verify missing template versions

## Use

```text
spinup cache warm [version...]
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
# Download the template version that the project locks
spinup cache warm

# Download two named versions
spinup cache warm v0.4.0 v0.5.0
```
