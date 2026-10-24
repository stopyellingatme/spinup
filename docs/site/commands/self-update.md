# spinup self-update

Verify and install a spinup release

## Use

```text
spinup self-update [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--channel` | `stable` | release channel: stable or next |
| `--check` | `false` | check for a release without installing it |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--require-signature` | `false` | require cosign signature verification |
| `--verbose` | `false` | write extra detail |
| `--version` | `` | install this release version |
| `-y, --yes` | `false` | answer yes to prompts |
