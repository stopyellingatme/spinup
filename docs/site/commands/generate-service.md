---
title: spinup generate service
parent: spinup generate
grand_parent: CLI reference
---

# spinup generate service

Add a sidecar service

## Use

```text
spinup generate service <name> --lang rust|python|node|<plugin>/<template> [--kind service|pdf] [--port 8080] [--dev-port N] [--public /svc/<name>] [--auth none|session] [--no-openapi] [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--auth` | `none` | public route authentication: none or session |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `--dev-port` | `0` | host port (default: first unused port from 8101) |
| `--diff` | `false` | show unified diffs without writing |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--force` | `false` | overwrite files with different content |
| `--json` | `false` | write JSON output |
| `--kind` | `service` | template variant: service, or pdf with --lang python |
| `--lang` | `` | service language or plugin template (required) |
| `--no-color` | `false` | turn off color |
| `--no-openapi` | `false` | omit the OpenAPI file and Go client |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--port` | `8080` | container port |
| `--public` | `` | public URL prefix |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Examples

```sh
# Add a Rust sidecar service
spinup g service thumbs --lang rust

# Add a Python PDF report service
spinup g service reports --lang python --kind pdf

# Add a Node service with a public route behind sign-in
spinup g service hooks --lang node --public /svc/hooks --auth session
```
