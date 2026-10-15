# spinup plugin

Manage trusted template pack plugins

## Use

```text
spinup plugin
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
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

- [`spinup plugin add`](plugin-add.md) — Inspect and trust a plugin without rendering files
- [`spinup plugin info`](plugin-info.md) — Show verified plugin metadata, generators, and files
- [`spinup plugin list`](plugin-list.md) — List declared plugins and their locked hashes
- [`spinup plugin new`](plugin-new.md) — Create a declarative or executable plugin project
- [`spinup plugin remove`](plugin-remove.md) — Remove plugin records and retain rendered files
- [`spinup plugin update`](plugin-update.md) — Inspect and trust a plugin version
- [`spinup plugin verify`](plugin-verify.md) — Re-fetch plugins and verify their trust records
