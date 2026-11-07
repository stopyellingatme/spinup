---
title: spinup generate mcp
parent: spinup generate
grand_parent: CLI reference
---

# spinup generate mcp

Generate a typed MCP tool

## Use

```text
spinup generate mcp tool <name> [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `--destructive` | `false` | mark the tool as destructive (implies --write) |
| `--diff` | `false` | show unified diffs without writing |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--force` | `false` | overwrite files with different content |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--role` | `[]` | declared roles that can call the tool (repeatable) |
| `--verbose` | `false` | write extra detail |
| `--write` | `false` | allow the tool to write |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Add a write tool
spinup g mcp tool lookup_order --write
```
