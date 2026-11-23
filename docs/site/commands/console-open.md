---
title: spinup console open
parent: spinup console
grand_parent: CLI reference
---

# spinup console open

Open the console through local SSH tunnels

## Use

```text
spinup console open [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--accept-new-host-key` | `false` | accept a previously unknown SSH host key |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-browser` | `false` | print the URL without opening a browser |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Open the console in the browser through an SSH tunnel
spinup console open --env production

# Print the URL and do not open a browser
spinup console open --no-browser --env production
```
