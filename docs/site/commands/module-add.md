# spinup module add

Enable modules on a branch at the locked template version

## Use

```text
spinup module add <name>... [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--abort` | `false` | remove the recorded worktree and branch |
| `--api-client` | `openapi-fetch` | Generated API client (openapi-fetch or heyapi) |
| `--auth-oidc` | `false` | Enable OpenID Connect sign-in? |
| `--ci-deploy-on-push` | `false` | Deploy the default environment on pushes to main |
| `--commit-conflicts` | `false` | commit conflict markers and continue |
| `--continue` | `false` | continue the recorded module addition |
| `--db-version` | `18` | PostgreSQL major version |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--frontend-mode` | `node` | Frontend mode (node: SvelteKit server; embed: a static app served by the Go binary) |
| `--json` | `false` | write JSON output |
| `--module` | `` | Go module path |
| `--monitoring-profile` | `lite` | Monitoring profile |
| `--no-color` | `false` | turn off color |
| `--no-verify` | `false` | skip verification in the worktree |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--package-manager` | `pnpm` | JavaScript package manager |
| `--shadcn-registry` | `1.7.0` | shadcn-svelte registry version |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
