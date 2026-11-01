---
title: spinup db lint
parent: spinup db
grand_parent: CLI reference
---

# spinup db lint

Check migrations for unsafe deploy changes

## Use

```text
spinup db lint [file...]
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

## Examples

```sh
# Check every migration for unsafe deploy changes
spinup db lint

# Check one migration file
spinup db lint api/db/migrations/00007_add_index.sql
```
