# spinup new

Create an application from template packs

## Use

```text
spinup new <name> [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--api-client` | `openapi-fetch` | Generated API client (openapi-fetch or heyapi) |
| `--api-only` | `false` | create only the Go API |
| `--auth-oidc` | `false` | Enable OpenID Connect sign-in? |
| `--ci-deploy-on-push` | `false` | Deploy the default environment on pushes to main |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `--db-version` | `18` | PostgreSQL major version |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--frontend-mode` | `node` | Frontend mode (node: SvelteKit server; embed: a static app served by the Go binary) |
| `--json` | `false` | write JSON output |
| `--module` | `` | Repository path (github.com/<owner>/<repo>) |
| `--modules` | `ci,deploy` | Modules, separated by commas |
| `--monitoring-profile` | `lite` | Monitoring profile |
| `--no-color` | `false` | turn off color |
| `--no-git` | `false` | do not initialize a git repository |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--package-manager` | `pnpm` | JavaScript package manager |
| `--plugin` | `` | add a trusted pack source with optional @version (repeatable) |
| `--shadcn-registry` | `1.7.0` | shadcn-svelte registry version |
| `--trust` | `false` | trust plugin sources and hashes |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
