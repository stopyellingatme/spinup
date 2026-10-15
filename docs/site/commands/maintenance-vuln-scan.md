# spinup maintenance vuln-scan

Scan running images with Trivy

## Use

```text
spinup maintenance vuln-scan [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--fail-on` | `high` | fail on high or critical, or critical |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
