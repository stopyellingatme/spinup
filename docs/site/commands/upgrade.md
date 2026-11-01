---
title: spinup upgrade
parent: CLI reference
---

# spinup upgrade

Upgrade app templates in a separate Git worktree

## Use

```text
spinup upgrade [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--abort` | `false` | remove an upgrade worktree and branch |
| `--allow-dirty` | `false` | allow changes in the main checkout |
| `--commit-conflicts` | `false` | commit conflict markers and continue |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `--continue` | `false` | continue an upgrade after resolving conflicts |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--force-managed` | `false` | overwrite changed managed files |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--no-verify` | `false` | skip verification after the upgrade |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--plan` | `false` | print the plan without changing project files |
| `--to` | `` | target template version (default newest loadable) |
| `--ui` | `false` | merge shadcn-svelte component updates |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# See the plan first
spinup upgrade --plan

# Upgrade to the newest template version in a new worktree
spinup upgrade

# Upgrade to a chosen version
spinup upgrade --to 0.9.0

# Continue after you fix conflicts, or give up
spinup upgrade --continue
spinup upgrade --abort
```
