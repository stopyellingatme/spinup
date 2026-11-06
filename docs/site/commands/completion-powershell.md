---
title: spinup completion powershell
parent: spinup completion
grand_parent: CLI reference
---

# spinup completion powershell

Generate the autocompletion script for powershell

Generate the autocompletion script for powershell.

To load completions in your current shell session:

	spinup completion powershell | Out-String | Invoke-Expression

To load completions for every new session, add the output of the above command
to your powershell profile.


## Use

```text
spinup completion powershell [flags]
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
| `--no-descriptions` | `false` | disable completion descriptions |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
