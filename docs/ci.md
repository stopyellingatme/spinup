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
| `ci.yml` | Push to `main`, pull requests | Build, vet, and test the API with PostgreSQL. Check the app with spinup. |
| `e2e.yml` | Pull requests, with `web` | Migrate PostgreSQL, start both apps, and run Playwright. |
| `deploy.yml` | Manual, with `deploy` | Deploy to the selected GitHub environment. |
| `backup-verify.yml` | Weekly or manual, with `backups` | Verify the latest backup of the default environment. |
| `upgrade-check.yml` | Weekly or manual | Install the newest release and propose an upgrade PR. |

The web job runs `pnpm check` and `pnpm test`.
An app without unit tests can pass the web job.
The web job excludes `e2e/` because Playwright runs those tests separately.
A failed Playwright run uploads `web/playwright-report/` for seven days.
Go and Node versions come from `api/go.mod` and `web/package.json`.
The `packageManager` field selects pnpm.

The deploy pack lists `staging` first.
The deploy and backup workflows use `staging` as their default environment.
Select another environment through the manual deploy input.
The deploy workflow serializes remote operations for each environment.
The backup workflow shares this concurrency group for `staging`.

Enable deployment on pushes to `main` during generation:

```console
$ spinup new preview --api-only --modules ci,deploy --module example.com/preview --ci-deploy-on-push --no-git --non-interactive --dry-run
...
.github/workflows/deploy.yml (managed)
...
```

The `--ci-deploy-on-push` flag defaults to false.
A push deploys to `staging`.
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
Only the deploy and backup workflows read these secrets.
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
The repository must allow GitHub Actions to create pull requests.
Only the upgrade job receives write permissions for contents and pull requests.
All workflows declare read access to contents at the top level.
