# spinup dev

Run the local development stack

## Use

```text
spinup dev [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--api-port` | `8080` | preferred API port |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--down` | `false` | stop Docker infrastructure on exit |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--lgtm` | `false` | start Grafana LGTM monitoring |
| `--no-color` | `false` | turn off color |
| `--no-infra` | `false` | skip Docker infrastructure |
| `--no-web` | `false` | skip the Vite dev server |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--port` | `5173` | preferred web port |
| `--verbose` | `false` | write extra detail |
| `--watch` | `air` | Go watcher: air or wgo |
| `-y, --yes` | `false` | answer yes to prompts |
