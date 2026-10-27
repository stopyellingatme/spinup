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
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `--contract` | `false` | run contract tests (M4) |
| `--cover` | `false` | report test coverage with cargo llvm-cov |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--e2e` | `false` | run end-to-end tests (M4) |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--run` | `` | run tests whose names match this filter |
| `--verbose` | `false` | write extra detail |
| `--web` | `false` | run web tests (M4) |
| `-y, --yes` | `false` | answer yes to prompts |
