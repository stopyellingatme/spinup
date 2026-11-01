---
title: spinup test
parent: CLI reference
---

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
| `--contract` | `false` | run contract tests (not available yet) |
| `--cover` | `false` | report Go test coverage |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--e2e` | `false` | run end-to-end tests (not available yet) |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--race` | `false` | enable the Go race detector |
| `--run` | `` | run tests matching this regular expression |
| `--verbose` | `false` | write extra detail |
| `--web` | `false` | run web tests (not available yet) |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Run the API tests
spinup test

# Run matching tests with the race detector
spinup test --run TestTicket --race

# Report coverage
spinup test --cover
```
