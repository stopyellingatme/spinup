# Generated CI

The `ci` pack creates managed GitHub workflows and a local action that installs spinup.
The web pack adds `e2e.yml` when the `ci` module is selected.
An upgrade merges these files through the normal three-way merge.
Add custom jobs in separate workflow files.

Preview an API-only app with CI:

```console
$ spinup new preview --api-only --modules ci --module example.com/preview --no-git --non-interactive --dry-run
...
.github/actions/setup-spinup/action.yml (managed)
...
.github/workflows/ci.yml (managed)
...
.github/workflows/upgrade-check.yml (managed)
...
```

| File | Trigger | Purpose |
| --- | --- | --- |
| `ci.yml` | Push to `main`, pull requests | Build, vet, and test the API with PostgreSQL. Check the app with spinup. Build and test each sidecar service. |
| `e2e.yml` | Pull requests, with `web` | Migrate PostgreSQL, start both apps, and run Playwright. |
| `deploy.yml` | Manual, with `deploy` | Deploy to the selected GitHub environment. |
| `backup-verify.yml` | Weekly or manual, with `backups` | Verify the latest backup of the default environment. |
| `upgrade-check.yml` | Weekly or manual | Install the newest release and propose an upgrade PR. |

## Service jobs

The `spinup` job of `ci.yml` runs `spinup ci services --json` and saves the matrix as a job output.
The `services` job uses that matrix.
It does not run when the app declares no service.
For each service, the job runs these commands:

<!-- doctest: skip -->
```console
$ spinup build --image --target ocr
$ spinup ci test-service ocr
```

`spinup ci test-service` builds a temporary test Dockerfile from the pinned images.
It runs the tests of the service in Docker, so the runner needs only Docker.

| Language | Test commands |
| --- | --- |
| `rust` | `cargo test --locked` |
| `python` | `uv sync --locked`, then `pytest` |
| `node` | `pnpm install --frozen-lockfile`, then `pnpm test`, then `pnpm run typecheck` if the script exists |

The language comes from the files in `services/<name>/`.
A service with no known language is only built.
`spinup g service` needs no edit of the workflow.
An existing app gets the job through the three-way merge of the managed file `ci.yml`.
See [Generate and verify CI files](commands/ci.md) for the commands.

The web job runs `pnpm check` and `pnpm test`.
An app without unit tests can pass the web job.
The Vite configuration excludes `e2e/` from Vitest and keeps the default exclusions.
Local `pnpm test` uses the same configuration.
A failed Playwright run uploads `web/playwright-report/` for seven days.
The Go version comes from `api/go.mod`.
Every workflow pins the Node major from the spinup stack manifest.
This major matches the Node image.
The `packageManager` field selects pnpm.

Set the repository variable `SPINUP_ENVIRONMENT` to the default environment from `spinup.toml`.
Backup verification and deployment on pushes use `${{ vars.SPINUP_ENVIRONMENT || 'staging' }}`.
Without this variable, both workflows use `staging`.
The manual deploy input takes precedence over this variable.
The workflows share a concurrency group for each environment.

Enable deployment on pushes to `main` during generation:

```console
$ spinup new preview --api-only --modules ci,deploy --module example.com/preview --ci-deploy-on-push --no-git --non-interactive --dry-run
...
.github/workflows/deploy.yml (managed)
...
```

The `--ci-deploy-on-push` flag defaults to false.
A push deploys to the environment selected by `SPINUP_ENVIRONMENT`, with `staging` as the fallback.
Configure the target host and public URL in `spinup.toml` before deployment.
Configure the matching GitHub environment with these secrets:

| Secret | Value |
| --- | --- |
| `SPINUP_AGE_KEY` | Private age key for the CI recipient in `.sops.yaml`. |
| `SPINUP_SSH_KEY` | Private SSH key for the deploy user. |
| `SPINUP_SSH_KNOWN_HOSTS` | Trusted SSH host entries for the target server. |

Encrypt the environment secrets to the CI recipient before deployment.
The workflows install the age key with mode `0600` at `~/.config/spinup/keys/<app>-<env>.agekey`.
They require strict SSH host verification and remove the keys after the operation.
Only the deploy and backup workflows read these three secrets.
Both workflows read them within their GitHub environment.

The local `setup-spinup` action reads `cli_version` from `spinup.lock` by default.
Its optional `version` input accepts a published version or `latest`.
The action downloads the Linux amd64 archive and checks its SHA-256 before installation.
It installs spinup at `$RUNNER_TEMP/spinup/bin` and adds that directory to `GITHUB_PATH`.
The `latest` input resolves the newest release through the GitHub API with `GITHUB_TOKEN`.
The default path needs no token for downloads.
Development versions fail because they have no published archive.
Generate or upgrade the app with a published release before GitHub runs its workflows.

The upgrade workflow removes `SPINUP_KIT_REPLACE` before it runs spinup.
It opens the PR from the separate worktree created by the upgrade engine.
If the upgrade returns exit code 3, the PR contains the conflicts and lists their paths.
Resolve the markers and complete the upgrade before merging that PR.

Enable the repository setting "Allow GitHub Actions to create and approve pull requests".
Create the repository secret `SPINUP_UPGRADE_TOKEN` for upgrades that change workflows and for PRs that must trigger CI.
Use a fine-grained token or a GitHub App token with contents, pull-requests, and workflows write permissions.
Both checkout and PR creation use `${{ secrets.SPINUP_UPGRADE_TOKEN || github.token }}`.

Without the secret, the workflow uses `GITHUB_TOKEN`.
This fallback cannot push changes under `.github/workflows/`, and its PRs do not trigger CI.
The PR body explains these limits when the workflow uses the fallback.
Only the upgrade job grants `GITHUB_TOKEN` write permissions for contents and pull requests.
All workflows declare read access to contents at the top level.
