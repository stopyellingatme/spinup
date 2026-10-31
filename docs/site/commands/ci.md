# spinup ci

Generate and verify GitHub Actions files

## Use

```text
spinup ci
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
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

- [`spinup ci generate`](ci-generate.md) — Render the locked ci pack
- [`spinup ci services`](ci-services.md) — List services for a GitHub Actions matrix
- [`spinup ci test-service`](ci-test-service.md) — Run tests for one service
- [`spinup ci verify`](ci-verify.md) — Check workflow safety and run optional actionlint
