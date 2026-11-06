---
title: spinup completion fish
parent: spinup completion
grand_parent: CLI reference
---

# spinup completion fish

Generate the autocompletion script for fish

Generate the autocompletion script for the fish shell.

To load completions in your current shell session:

	spinup completion fish | source

To load completions for every new session, execute once:

	spinup completion fish > ~/.config/fish/completions/spinup.fish

You will need to start a new shell for this setup to take effect.


## Use

```text
spinup completion fish [flags]
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
