# spinup generate

Generate application files

## Use

```text
spinup generate <kind> [args...]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `--diff` | `false` | show unified diffs without writing |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--force` | `false` | overwrite files with different content |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Subcommands

- [`spinup generate alert`](generate-alert.md) — Add a custom Prometheus alert rule
- [`spinup generate client`](generate-client.md) — Generate TypeScript types from OpenAPI
- [`spinup generate component`](generate-component.md) — Add shadcn-svelte components to the web app (needs the network)
- [`spinup generate dashboard`](generate-dashboard.md) — Add a custom Grafana dashboard
- [`spinup generate handler`](generate-handler.md) — Generate a plain HTTP handler and its test
- [`spinup generate job`](generate-job.md) — Add a background job (River) to api/internal/jobs
- [`spinup generate mailer`](generate-mailer.md) — Add a typed mailer and email templates
- [`spinup generate migration`](generate-migration.md) — Generate an empty SQL migration
- [`spinup generate model`](generate-model.md) — Generate a table: migration, sqlc queries, and OpenAPI schemas
- [`spinup generate openapi`](generate-openapi.md) — Bundle OpenAPI files and generate Go types
- [`spinup generate page`](generate-page.md) — Generate a SvelteKit page in web/src/routes
- [`spinup generate resource`](generate-resource.md) — Generate a CRUD API resource: migration, queries, OpenAPI, handlers, and tests
- [`spinup generate service`](generate-service.md) — Add a sidecar service
