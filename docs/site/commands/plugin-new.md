---
title: spinup plugin new
parent: spinup plugin
grand_parent: CLI reference
---

# spinup plugin new

Create a declarative or executable plugin project

## Use

```text
spinup plugin new <name> [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--kind` | `pack` | plugin kind: pack or exec |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--out` | `` | destination directory (default ./<name>) |
| `--platform` | `` | executable target as GOOS-GOARCH (default: the current platform) |
| `--sdk-path` | `` | local plugin-sdk directory for an executable plugin |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Create a declarative pack plugin in ./my-pack
spinup plugin new my-pack

# Create an executable plugin in a chosen directory
spinup plugin new my-exec --kind exec --out ../plugins/my-exec
```
