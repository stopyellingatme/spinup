---
title: spinup secrets
parent: CLI reference
---

# spinup secrets

Manage encrypted secrets for each environment

## Use

```text
spinup secrets
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

- [`spinup secrets diff`](secrets-diff.md) — Show names changed since Git HEAD
- [`spinup secrets edit`](secrets-edit.md) — Edit secrets in a terminal
- [`spinup secrets get`](secrets-get.md) — Write one secret value to stdout
- [`spinup secrets init`](secrets-init.md) — Create an environment key and encrypted secrets
- [`spinup secrets push`](secrets-push.md) — Write the environment's decrypted secrets to the host, under the deploy lock
- [`spinup secrets rekey`](secrets-rekey.md) — Update encryption recipients from .sops.yaml
- [`spinup secrets rotate`](secrets-rotate.md) — Replace the data encryption key
- [`spinup secrets set`](secrets-set.md) — Read a secret value from stdin

## Examples

```sh
# Create keys, set a value, then push to the host
spinup secrets init --env production --recovery-recipient age1...
spinup secrets set API_KEY --env production < api-key.txt
spinup secrets push --env production
```
