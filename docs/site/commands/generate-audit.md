---
title: spinup generate audit
parent: spinup generate
grand_parent: CLI reference
---

# spinup generate audit

Attach audit triggers and add resource history

## Use

```text
spinup generate audit <resource-or-table> [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--api-only` | `false` | skip history web pages |
| `--audit-redact` | `` | additional columns to redact from the trail |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `--diff` | `false` | show unified diffs without writing |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--force` | `false` | overwrite files with different content |
| `--history-role` | `` | comma-separated roles that can read history |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Add audit triggers and a history page to a resource
spinup g audit ticket

# Add the trail only, and hide a column
spinup g audit users --api-only --audit-redact phone
```
