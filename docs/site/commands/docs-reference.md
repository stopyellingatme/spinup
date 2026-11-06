---
title: spinup docs reference
parent: spinup docs
grand_parent: CLI reference
---

# spinup docs reference

Write a Markdown reference page for every command

## Use

```text
spinup docs reference [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `-h, --help` | `false` | help for reference |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--out` | `` | directory for generated Markdown pages |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Write a Markdown page for every command
spinup docs reference --out docs/cli
```
