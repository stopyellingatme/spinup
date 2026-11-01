---
title: spinup infra destroy
parent: spinup infra
grand_parent: CLI reference
---

# spinup infra destroy

Destroy the environment infrastructure

## Use

```text
spinup infra destroy [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--i-know` | `false` | confirm destruction of the server and volume |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Delete the server and volume (this removes data)
spinup infra destroy --i-know --env staging
```
