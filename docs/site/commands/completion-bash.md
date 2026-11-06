---
title: spinup completion bash
parent: spinup completion
grand_parent: CLI reference
---

# spinup completion bash

Generate the autocompletion script for bash

Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source <(spinup completion bash)

To load completions for every new session, execute once:

#### Linux:

	spinup completion bash > /etc/bash_completion.d/spinup

#### macOS:

	spinup completion bash > $(brew --prefix)/etc/bash_completion.d/spinup

You will need to start a new shell for this setup to take effect.


## Use

```text
spinup completion bash
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
