---
title: spinup completion
parent: CLI reference
---

# spinup completion

Generate the autocompletion script for the specified shell

Generate the autocompletion script for spinup for the specified shell.
See each sub-command's help for details on how to use the generated script.


## Use

```text
spinup completion
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Subcommands

- [`spinup completion bash`](completion-bash.md) — Generate the autocompletion script for bash
- [`spinup completion fish`](completion-fish.md) — Generate the autocompletion script for fish
- [`spinup completion powershell`](completion-powershell.md) — Generate the autocompletion script for powershell
- [`spinup completion zsh`](completion-zsh.md) — Generate the autocompletion script for zsh
