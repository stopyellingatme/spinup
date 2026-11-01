---
title: spinup monitor logs
parent: spinup monitor
grand_parent: CLI reference
---

# spinup monitor logs

Query Loki logs

## Use

```text
spinup monitor logs [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--accept-new-host-key` | `false` | accept a previously unknown SSH host key |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--query` | `` | use this LogQL query instead of the service filter |
| `--service` | `` | filter by the service_name label |
| `--since` | `1h` | query this duration into the past |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Show the last hour of logs for one service
spinup monitor logs --service api --env production

# Show the last 15 minutes
spinup monitor logs --service api --since 15m --env production

# Use your own LogQL query
spinup monitor logs --query '{service_name="api"} |= "error"' --env production
```
