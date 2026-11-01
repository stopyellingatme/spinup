---
title: Generated CI
parent: Deploy
nav_order: 6
---

# Generated CI

The `ci` module writes GitHub Actions workflows that build and test your app, deploy it, and propose spinup upgrades.
Most of them work with no setup. The deploy and backup workflows need three secrets and one variable.

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

The files are managed, so `spinup upgrade` updates them with a three-way merge.
Add your own jobs in separate workflow files.

| File | Trigger | What it does |
| --- | --- | --- |
| `ci.yml` | Push to `main`, pull requests | Builds, vets, and tests the API with PostgreSQL. Checks the app with spinup. Builds and tests each sidecar service. |
| `e2e.yml` | Pull requests, with `web` | Migrates PostgreSQL, starts both apps, and runs Playwright. |
| `deploy.yml` | Manual, with `deploy`. Also on push to `main` with `--ci-deploy-on-push`. | Deploys to the selected GitHub environment. |
| `backup-verify.yml` | Weekly or manual, with `backups` | Verifies the latest backup of the default environment. |
| `upgrade-check.yml` | Weekly or manual | Installs the newest release and opens an upgrade PR. |

## Set up deploy from CI

Skip this section if you only want tests.

1. **Make a CI key for the secrets.** Add its public key as a recipient and encrypt the environment secrets to it:

   ```sh
   age-keygen -o ci.agekey
   spinup secrets init --env staging --recovery-recipient age1... --ci-recipient age1...
   ```

   For an existing file, add the public key to `.sops.yaml` and run `spinup secrets rekey --env staging`.
   See [Secrets](deploy/secrets.md).
2. **Create the secrets** in the GitHub environment that has the name of your spinup environment. Make the GitHub environment first, and make an SSH key for CI:

   ```sh
   gh api --method PUT repos/{owner}/{repo}/environments/staging
   ssh-keygen -t ed25519 -N "" -f ~/.ssh/spinup_ci
   ssh-copy-id -i ~/.ssh/spinup_ci.pub deploy@staging.example.com
   ```

   The three secrets are:

   | Secret | Value | How to get it |
   | --- | --- | --- |
   | `SPINUP_AGE_KEY` | The private age key of the CI recipient. | The file `ci.agekey`. |
   | `SPINUP_SSH_KEY` | The private SSH key of the deploy user. | The key pair `spinup_ci` that you made above. The server has its public key. |
   | `SPINUP_SSH_KNOWN_HOSTS` | The trusted host keys of the server. | `ssh-keyscan -t ed25519 staging.example.com` |

   ```sh
   gh secret set SPINUP_AGE_KEY --env staging < ci.agekey
   gh secret set SPINUP_SSH_KEY --env staging < ~/.ssh/spinup_ci
   ssh-keyscan -t ed25519 staging.example.com | gh secret set SPINUP_SSH_KNOWN_HOSTS --env staging
   ```

3. **Set the default environment** as a repository variable:

   ```sh
   gh variable set SPINUP_ENVIRONMENT --body staging
   ```

   Without it, the workflows use `staging`.
4. **Run the workflow:**

   ```sh
   gh workflow run deploy.yml -f environment=staging
   ```

The workflows install the age key at `~/.config/spinup/keys/<app>-<env>.agekey` with mode 0600.
They require strict SSH host checking and remove the keys when the job ends.
Only the deploy and backup workflows read these secrets.
The workflows share a concurrency group for each environment, so two deploys never overlap.
The manual `environment` input beats the variable.

## Deploy on every push

Pass `--ci-deploy-on-push` when you generate the app:

```console
$ spinup new preview --api-only --modules ci,deploy --module example.com/preview --ci-deploy-on-push --no-git --non-interactive --dry-run
...
.github/workflows/deploy.yml (managed)
...
```

The flag is off by default.
To turn it on for an existing app, set `"ci_deploy_on_push": true` in the `answers` of `spinup.lock`, then run `spinup ci generate`.
A push to `main` then deploys to the environment in `SPINUP_ENVIRONMENT`.
Set the host and the public URL of that environment in `spinup.toml` first.

## Check the workflows

`spinup ci verify` checks the workflows for unsafe settings and runs `actionlint` when it is installed.
`spinup ci generate` renders the locked `ci` pack again.
See the [ci command guide](commands/ci.md).

<!-- doctest: skip -->
```console
$ spinup ci verify
```

## Service jobs

The `spinup` job of `ci.yml` runs `spinup ci services --json` and uses the result as a matrix.
The `services` job does not run when the app has no [sidecar service](services.md).
For each service, it runs:

<!-- doctest: skip -->
```console
$ spinup build --image --target ocr
$ spinup ci test-service ocr
```

`test-service` runs the tests in Docker with the pinned images, so the runner needs only Docker.
The language comes from the files in `services/<name>/`:

| Language | Test commands |
| --- | --- |
| `rust` | `cargo test --locked` |
| `python` | `uv sync --locked`, then `pytest` |
| `node` | `pnpm install --frozen-lockfile`, then `pnpm test`, then `pnpm run typecheck` if the script exists |

A service with no known language is only built.
A new service needs no workflow edit.
An existing app gets the job through the three-way merge of `ci.yml`.

## Web jobs

The web job runs `pnpm check` and `pnpm test`.
An app with no unit tests passes.
A failed Playwright run uploads `web/playwright-report/` for seven days.
The workflows take the Go version from `api/go.mod` and the Node major from the spinup stack.

## The upgrade workflow

`upgrade-check.yml` runs weekly. It installs the newest spinup release and opens a pull request with the upgrade.
If the upgrade has conflicts (exit code 3), the PR holds the conflict markers and lists the files. Resolve them before you merge.

To let the workflow work fully:

1. Turn on the repository setting "Allow GitHub Actions to create and approve pull requests".
2. Create the repository secret `SPINUP_UPGRADE_TOKEN`. Use a fine-grained token or a GitHub App token with write access to contents, pull requests, and workflows.

   ```sh
   gh secret set SPINUP_UPGRADE_TOKEN
   ```

Without the secret, the workflow uses `GITHUB_TOKEN`.
That token cannot change files under `.github/workflows/`, and its PRs do not start CI.
The PR text says so when this happens.

## Versions and permissions

The local action `setup-spinup` installs spinup at `$RUNNER_TEMP/spinup/bin`.
It reads `cli_version` from `spinup.lock`. Its optional `version` input takes a published version or `latest`.
It checks the SHA-256 of the Linux amd64 archive before it installs.
A development version fails, because it has no published archive.
Every workflow reads contents at the top level. Only the upgrade job writes contents and pull requests.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `Cannot deploy: a required environment secret is missing` | One of the three secrets is not set in that GitHub environment. | Set `SPINUP_AGE_KEY`, `SPINUP_SSH_KEY`, and `SPINUP_SSH_KNOWN_HOSTS`. |
| The deploy uses `staging` | `SPINUP_ENVIRONMENT` is not set. | Set the variable, or pass the `environment` input. |
| Host key verification fails | `SPINUP_SSH_KNOWN_HOSTS` is stale. | Run `ssh-keyscan` again and update the secret. |
| The upgrade PR does not run CI | The workflow used `GITHUB_TOKEN`. | Create `SPINUP_UPGRADE_TOKEN`. |
| `setup-spinup` fails | `spinup.lock` names a development version. | Upgrade to a published release. |

## See also

- [Deploy overview](deploy/index.md)
- [Secrets](deploy/secrets.md) and [Environments](deploy/environments.md)
- [The `ci` command guide](commands/ci.md)
- [Flag reference of `spinup ci`](site/commands/ci.md)
