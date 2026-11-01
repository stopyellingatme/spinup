---
title: spinup restore
parent: CLI reference
---

# spinup restore

Restore a tracked file from its template

## Use

```text
spinup restore <path> [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--force` | `false` | replace an existing edited file |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Put a file back to its template content
spinup restore api/internal/http/router.go

# Replace the file even if you edited it
spinup restore api/internal/http/router.go --force
```
