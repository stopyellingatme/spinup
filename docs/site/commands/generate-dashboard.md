---
title: spinup generate dashboard
parent: spinup generate
grand_parent: CLI reference
---

# spinup generate dashboard

Add a custom Grafana dashboard

## Use

```text
spinup generate dashboard <name> [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `--diff` | `false` | show unified diffs without writing |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--force` | `false` | overwrite files with different content |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--panel` | `[]` | time-series panel as title=PromQL expression |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Add a dashboard with one panel (title=PromQL)
spinup g dashboard orders --panel 'Orders per second=sum(rate(orders_created_total[5m]))'
```
