# spinup test

Run application tests

## Use

```text
spinup test [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--api` | `true` | run API tests (default) |
| `--contract` | `false` | run contract tests (M4) |
| `--cover` | `false` | report Go test coverage |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--e2e` | `false` | run end-to-end tests (M4) |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--race` | `false` | enable the Go race detector |
| `--run` | `` | run tests matching this regular expression |
| `--verbose` | `false` | write extra detail |
| `--web` | `false` | run web tests (M4) |
| `-y, --yes` | `false` | answer yes to prompts |
