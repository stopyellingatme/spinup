# spinup generate resource

Generate a CRUD API resource: migration, model, handlers, and tests

## Use

```text
spinup generate resource [args...] [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--api-only` | `false` | skip resource parts, such as web pages |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--no-tests` | `false` | skip the handler test file |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--shared` | `false` | share rows between signed-in users (requires auth) |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
