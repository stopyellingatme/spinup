# spinup plugin add

Inspect and trust a plugin without rendering files

## Use

```text
spinup plugin add <source> [flags]
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
| `--trust` | `false` | trust the displayed plugin source and hash |
| `--trust-exec` | `` | trust execution of the named plugin with your user privileges |
| `--verbose` | `false` | write extra detail |
| `--version` | `` | fixed git tag or path plugin version |
| `-y, --yes` | `false` | answer yes to prompts |
