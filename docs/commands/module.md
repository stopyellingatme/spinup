# Modules

A module is an optional built-in pack.
The registry reads each pack's description and requirements.
The `base`, `api`, and `web` packs are core packs.

Run `spinup module list` inside an app.
The table shows each module's name, state, requirements, and description.
Use `--json` for structured output.
The `enabled` field reads the `modules` setting in `spinup.toml`.

```console
$ spinup module add --continue --abort
Error: Invalid module arguments
  supply module names, --continue, or --abort
  Try: spinup module add --help
[exit 2]
```

Run `spinup module add monitoring --monitoring-profile lite --yes --non-interactive` to enable monitoring.
Supply multiple names to add multiple modules.
The command adds required modules and prints their names.
An API-only app cannot add a module that requires the web pack.

Each question has a flag, such as `--monitoring-profile` or `--ci-deploy-on-push`.
Explicit flags take precedence over the answers and defaults.
In a terminal, the command asks unanswered questions for the new packs.
Without a terminal, the command uses defaults or reports the missing flag.
Use `--non-interactive` to prevent prompts and `--yes` to confirm the change.

| Flag | Purpose |
| --- | --- |
| `--frontend-mode node\|embed` | Select a SvelteKit server or a static web app in the Go API. |
| `--package-manager pnpm\|bun` | Select the web package manager. |
| `--api-client openapi-fetch\|heyapi` | Select the generated web API client. |
| `--auth-oidc` | Enable OpenID Connect sign-in with the `auth` module. |

The command requires a clean Git repository and Git 2.44.0 or later.
It prints a plan before confirmation.
Use `--dry-run` to inspect the plan without creating a branch.
The command renders the locked template version, even when a newer version exists.

The branch uses `spinup/module-<names>` with sorted names separated by hyphens.
The worktree uses `.spinup/upgrade/<locked-version>`.
The upgrade flow applies the plan, updates dependencies, commits changes, and verifies the app.
The lock records the new packs and answers.
The command edits `spinup.toml` through the TOML editor and preserves comments.
The main checkout keeps its files and branch.

If files conflict, the command exits with code 3.
Resolve the markers in the worktree.
Then run `spinup module add --continue --yes` from the main checkout.
Use `spinup module add --abort --yes` to remove an interrupted run and its branch.
The corresponding `spinup upgrade --continue` and `--abort` commands also work.

Use `--no-verify` to skip verification.
Use `--commit-conflicts` to commit conflict markers for later resolution.
Use `--offline` to prevent template downloads.
The command reports an error if the locked templates are unavailable.

After success, inspect the branch and run the printed `git merge` command.
Then remove the worktree with `git worktree remove <worktree>`.
The branch remains available for inspection.
Module removal is outside this command's scope because files can contain user data.

```console
$ spinup module add --help
Enable modules on a branch at the locked template version
...
```
