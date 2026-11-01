---
title: spinup monitor silence
parent: spinup monitor
grand_parent: CLI reference
---

# spinup monitor silence

Silence alerts matching label=value

## Use

```text
spinup monitor silence <matcher> [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--accept-new-host-key` | `false` | accept a previously unknown SSH host key |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--for` | `2h` | silence duration |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Silence one alert for 2 hours (the default)
spinup monitor silence prometheus_alertname=HighErrorRate --env production

# Silence for 30 minutes
spinup monitor silence prometheus_alertname=HighErrorRate --for 30m --env production
```
