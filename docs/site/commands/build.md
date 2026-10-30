# spinup build

Build the application API

## Use

```text
spinup build [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--image` | `false` | build application container images |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--platform` | `` | target platform (default linux/amd64) |
| `--push` | `false` | push images with provenance and an SBOM |
| `--registry` | `` | container registry (default ghcr.io) |
| `--tag` | `` | version tag (default dev) |
| `--target` | `[]` | image target to build; repeat or separate names with commas |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
