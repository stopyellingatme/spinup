# spinup generate

Generate application files

## Use

```text
spinup generate <kind> [args...]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `--diff` | `false` | show unified diffs without writing |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--force` | `false` | overwrite files with different content |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Subcommands

- [`spinup generate handler`](generate-handler.md) — Generate a plain HTTP handler and its test
- [`spinup generate openapi`](generate-openapi.md) — Bundle OpenAPI files and generate Go types
- [`spinup generate resource`](generate-resource.md) — Generate a CRUD API resource: migration, queries, OpenAPI, handlers, and tests
