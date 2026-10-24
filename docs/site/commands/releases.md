# spinup releases

Inspect and prune stored releases

## Use

```text
spinup releases
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

- [`spinup releases list`](releases-list.md) — List the release history and current marker
- [`spinup releases prune`](releases-prune.md) — Prune old releases while retaining current and previous
- [`spinup releases show`](releases-show.md) — Show a release snapshot with secret hashes only
