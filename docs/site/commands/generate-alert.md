---
title: spinup generate alert
parent: spinup generate
grand_parent: CLI reference
---

# spinup generate alert

Add a custom Prometheus alert rule

## Use

```text
spinup generate alert <name> --expr <PromQL> [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `--diff` | `false` | show unified diffs without writing |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--expr` | `` | PromQL expression for the alert |
| `--for` | `5m` | time that the expression must stay true |
| `--force` | `false` | overwrite files with different content |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--severity` | `warning` | alert severity: warning or critical |
| `--summary` | `` | short alert summary |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Alert when a target is down for 5 minutes
spinup g alert TargetDown --expr 'up == 0' --severity critical

# Alert on server errors, with a summary
spinup g alert HighErrors --expr 'sum(rate(http_server_request_duration_seconds_count{http_response_status_code=~"5.."}[5m])) > 0' --for 10m --summary "API returns 5xx"
```
