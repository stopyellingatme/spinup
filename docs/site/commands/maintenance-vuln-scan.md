---
title: spinup maintenance vuln-scan
parent: spinup maintenance
grand_parent: CLI reference
---

# spinup maintenance vuln-scan

Scan running images with Trivy

## Use

```text
spinup maintenance vuln-scan [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--fail-on` | `high` | fail on high or critical, or critical |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Scan the running images and fail on critical findings only
spinup maintenance vuln-scan --fail-on critical --env production

# Scan with the default level (high)
spinup maintenance vuln-scan --env production
```
