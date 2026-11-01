---
title: spinup secrets push
parent: spinup secrets
grand_parent: CLI reference
---

# spinup secrets push

Write the environment's decrypted secrets to the host, under the deploy lock

## Use

```text
spinup secrets push [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--accept-new-host-key` | `false` | trust the host key on the first connection |
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
# Write the decrypted secrets to the host
spinup secrets push --env production

# Trust the host key on the first connection
spinup secrets push --accept-new-host-key --env production
```
