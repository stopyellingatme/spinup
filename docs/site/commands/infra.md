# spinup infra

Provision Hetzner Cloud infrastructure with OpenTofu

## Use

```text
spinup infra
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

- [`spinup infra apply`](infra-apply.md) — Apply infrastructure changes and update the environment host
- [`spinup infra destroy`](infra-destroy.md) — Destroy the environment infrastructure
- [`spinup infra output`](infra-output.md) — Show infrastructure outputs
- [`spinup infra plan`](infra-plan.md) — Preview infrastructure changes
