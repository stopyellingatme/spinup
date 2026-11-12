---
title: spinup ci services
parent: spinup ci
grand_parent: CLI reference
---

# spinup ci services

List services for a GitHub Actions matrix

## Use

```text
spinup ci services [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--changed-since` | `` | keep only services changed since this git ref (all services if a shared file changed or git fails) |
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

## Examples

```sh
# Print the services as a GitHub Actions matrix
spinup ci services --json

# Keep only the services that a pull request changed
spinup ci services --json --changed-since origin/main
```
