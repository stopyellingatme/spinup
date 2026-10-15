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
