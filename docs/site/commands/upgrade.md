# spinup upgrade

Upgrade app templates in a separate Git worktree

## Use

```text
spinup upgrade [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--abort` | `false` | remove an upgrade worktree and branch |
| `--allow-dirty` | `false` | allow changes in the main checkout |
| `--commit-conflicts` | `false` | commit conflict markers and continue |
| `--continue` | `false` | continue an upgrade after resolving conflicts |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--force-managed` | `false` | overwrite changed managed files |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--no-verify` | `false` | skip verification after the upgrade |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--plan` | `false` | print the plan without changing project files |
| `--to` | `` | target template version (default newest loadable) |
| `--ui` | `false` | merge shadcn-svelte component updates |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
