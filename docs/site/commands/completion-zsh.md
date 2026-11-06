---
title: spinup completion zsh
parent: spinup completion
grand_parent: CLI reference
---

# spinup completion zsh

Generate the autocompletion script for zsh

Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(spinup completion zsh)

To load completions for every new session, execute once:

#### Linux:

	spinup completion zsh > "${fpath[1]}/_spinup"

#### macOS:

	spinup completion zsh > $(brew --prefix)/share/zsh/site-functions/_spinup

You will need to start a new shell for this setup to take effect.


## Use

```text
spinup completion zsh [flags]
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
