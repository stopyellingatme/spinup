# spinup monitor

Operate monitoring services through SSH

## Use

```text
spinup monitor
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
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Subcommands

- [`spinup monitor alerts`](monitor-alerts.md) — Show firing and pending alerts
- [`spinup monitor dashboards`](monitor-dashboards.md) — Manage custom Grafana dashboards
- [`spinup monitor logs`](monitor-logs.md) — Query Loki logs
- [`spinup monitor open`](monitor-open.md) — Open Grafana through a local SSH tunnel
- [`spinup monitor silence`](monitor-silence.md) — Silence alerts matching label=value
- [`spinup monitor status`](monitor-status.md) — Show monitoring health and disk use
- [`spinup monitor test-alert`](monitor-test-alert.md) — Send a test alert to configured contact points
- [`spinup monitor trace`](monitor-trace.md) — Show a Tempo span tree (full profile)
