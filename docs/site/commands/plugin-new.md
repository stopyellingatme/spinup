# spinup plugin new

Create a declarative or executable plugin project

## Use

```text
spinup plugin new <name> [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--kind` | `pack` | plugin kind: pack or exec |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--out` | `` | destination directory (default ./<name>) |
| `--platform` | `darwin-arm64` | executable target as GOOS-GOARCH |
| `--sdk-path` | `` | local plugin-sdk directory for an executable plugin |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
