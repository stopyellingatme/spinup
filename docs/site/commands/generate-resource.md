# spinup generate resource

Generate a CRUD API resource: migration, queries, OpenAPI, handlers, and tests

## Use

```text
spinup generate resource <name> <field:type>... [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--api-only` | `false` | skip the parts that other packs register, such as web pages |
| `--diff` | `false` | show unified diffs without writing |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--force` | `false` | overwrite files with different content |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--no-tests` | `false` | skip the handler test file |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
