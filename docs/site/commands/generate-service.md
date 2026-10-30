# spinup generate service

Add a sidecar service

## Use

```text
spinup generate service <name> --lang rust|python|<plugin>/<template> [--port 8080] [--dev-port N] [--no-openapi] [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `--dev-port` | `0` | host port (default: first unused port from 8101) |
| `--diff` | `false` | show unified diffs without writing |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--force` | `false` | overwrite files with different content |
| `--json` | `false` | write JSON output |
| `--lang` | `` | service language or plugin template (required) |
| `--no-color` | `false` | turn off color |
| `--no-openapi` | `false` | omit the OpenAPI file and Go client |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--port` | `8080` | container port |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
