---
title: spinup db migrate
parent: spinup db
grand_parent: CLI reference
---

# spinup db migrate

Run migrations through the application API

## Use

```text
spinup db migrate [up|down|status] | migrate kit up|down|status|to <version>|version [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--i-know` | `false` | acknowledge that down can lose data |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Apply every pending migration
spinup db migrate up

# Roll back the last migration (asks you to confirm data loss)
spinup db migrate down --i-know

# List applied and pending migrations
spinup db migrate status

# List the kit migrations of the auth and audit modules (ADR 0028)
spinup db migrate kit status
```
