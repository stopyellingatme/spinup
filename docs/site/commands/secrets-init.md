---
title: spinup secrets init
parent: spinup secrets
grand_parent: CLI reference
---

# spinup secrets init

Create an environment key and encrypted secrets

## Use

```text
spinup secrets init [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--ci-recipient` | `` | age recipient for CI |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--no-recovery` | `false` | accept permanent loss if all environment keys are lost |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--recovery-recipient` | `` | age recipient for recovery (required unless --no-recovery) |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Create the key and the encrypted file
spinup secrets init --env production --recovery-recipient age1...

# Skip the recovery key (you accept losing the secrets if the key is lost)
spinup secrets init --env staging --no-recovery
```
