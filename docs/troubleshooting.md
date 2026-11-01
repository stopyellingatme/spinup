---
title: Troubleshooting
parent: Get started
nav_order: 6
---

# Troubleshooting

Every spinup error has the same shape: what failed, why, and a `Try:` line with the fix.
Read the `Try:` line first. This page lists the errors that people meet most, with the cause and the fix for each.

```console
$ spinup module list
Error: list modules: could not find spinup.toml from ".". Try: run this inside a spinup project, or pass --dir
  Try: run inside a spinup project or pass --dir
[exit 1]
```

Find your error text in the section that fits, or run `spinup doctor` to check your machine and your app in one step.
The page [Doctor checks](reference/doctor-checks.md) explains every check.

| Section | Errors |
| --- | --- |
| [Start and develop](#start-and-develop) | no project, Docker, busy ports, a database that does not answer |
| [Doctor checks](#doctor-checks) | `kit-version`, `roles-drift`, `audit-wired`, `plaintext-secrets`, `managed-missing` |
| [Drift and generators](#drift-and-generators) | `spinup check` exit 4, files that exist already |
| [Upgrade and modules](#upgrade-and-modules) | a dirty tree, conflicts, a missing template version, clashing migrations |
| [Secrets](#secrets) | `sops` or `age` missing, a lost key, no `--env` |
| [Deploy](#deploy) | an unknown environment, a held lock, an unhealthy release, a protected environment |

## Start and develop

### No project found

**Error:** `could not find spinup.toml from "."`

You ran a command outside an app. spinup looks for `spinup.toml` in the current folder and its parents.
**Fix:** `cd` into the app, or pass the folder with `-C`:

```sh
spinup -C ~/code/todo doctor
```

### Docker is not running

**Error:** `dev prerequisite docker failed: Docker daemon did not answer docker info`, with `Try: start Docker and repeat spinup doctor`

`spinup dev` starts PostgreSQL and Mailpit in Docker. It stops when the Docker daemon does not answer.
**Fix:** start Docker Desktop or the Docker service. Wait a few seconds, then run `spinup doctor --quick`.

### A port is busy

**Error:** `fail ports port 8080 is busy outside this app`, with `Try: stop the other app or run spinup dev to select free ports`

Another program uses the port of the API (8080) or of the web app (5173).
**Fix:** stop that program. Or run `spinup dev`. It picks free ports and prints them.
A port that your own app already uses is not a problem. The check warns with `port 8080 is busy. Dev will select free ports`.

### The database does not answer

**Error:** `Cannot run migration`, with `go run ./cmd/api migrate status exited with status 1` and, above it, `connection refused`

`spinup db migrate` and `spinup db status` connect to the database in `DATABASE_URL`. Nothing listens on that address.
**Fix:** start the stack with `spinup dev`, and run the command in a second terminal. Check `DATABASE_URL` in `.env`.

### A tool is missing

**Error:** `run age-keygen: the secrets tool failed`, or a doctor line such as `fail pnpm`

spinup runs the tools that your app needs, for example `sops`, `age-keygen`, `pnpm`, and `docker`.
**Fix:** run `spinup doctor` to see which tool is missing. For `sops`, `age`, and `tofu`, run `spinup tools install`:

<!-- doctest: skip -->
```console
$ spinup tools list
$ spinup tools install sops
```

For `pnpm`, run `corepack enable`. See [tools](commands/tools.md).

### A command needs a flag and has no terminal

**Error:** `cannot upgrade: confirmation requires a terminal`, with `Try: pass --yes`

Every prompt has a flag. In a script or in CI, spinup never asks. It fails and names the flag.
**Fix:** pass the flag in the `Try:` line, for example `--yes`, `--non-interactive`, or `--confirm-env production`.

## Doctor checks

### kit-version

**Message:** `CLI 0.12.0 is incompatible with kit 0.11.2: major and minor versions must match`, with `Try: spinup upgrade or go get github.com/stopyellingatme/spinup/kit@v0.12.x`

The CLI and the kit in `api/go.mod` must share the major and minor number.
**Fix:** run `spinup upgrade`. It moves the templates and the kit together. See [Upgrade](upgrading.md).
To change only the kit, run `go get` from the `Try:` line in `api/`.

### roles-drift

**Message:** `api/internal/roles/roles.gen.go does not match [auth] in spinup.toml.`, with `Try: spinup g roles`

You changed `[auth]` and did not write the roles file again.
**Fix:**

```sh
spinup g roles
```

The command needs the `auth` module. If the message says that `main.go` does not pass the role set, add the calls that [Roles](build/roles.md) shows.

### audit-wired

**Message:** `api/cmd/api/main.go lacks audit.Command( or authsink. wiring`

An app that was made before the audit module needs a few lines in `api/cmd/api/main.go`.
**Fix:** add `auth.WithEvents(authsink.New(pool))`, `auth.WithActorContext(authsink.ActorContext)`, and `app.WithCommand("audit", audit.Command(pool))`.
See [Audit trail](audit.md).

### plaintext-secrets

**Message:** `tracked private file: .env`, `tracked plaintext secrets: secrets/staging.yaml`, or `tracked files could not be inspected`

A private file or a plaintext secrets file is in git, or the folder is not a git repository.
**Fix:** run `git rm --cached .env`, and add `.env`, `.env.*`, and `*.agekey` to `.gitignore`.
For a plaintext secrets file, encrypt it with sops before you commit. In a scratch folder, run `git init`.

### managed-missing

**Message:** `managed files missing: deploy/compose.yml`, with `Try: spinup restore deploy/compose.yml`

You deleted a file that spinup owns. An upgrade cannot merge a file that is gone.
**Fix:** run `spinup restore <path>`.

## Drift and generators

### spinup check exits with code 4

**Error:** `deploy/Dockerfile.api: managed file differs from its lock hash (drift)`

You edited a managed file. The lock keeps a hash of what spinup wrote, and the hashes differ.
**Fix:** choose one. Run `spinup diff <path>` to see the change first.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ echo "# mine" >> deploy/Dockerfile.api
$ spinup check
deploy/Dockerfile.api: managed file differs from its lock hash (drift)
Try: spinup eject deploy/Dockerfile.api or spinup restore deploy/Dockerfile.api
Error: Application check failed
  the project has drift
  Try: correct the reported problems and run spinup check
[exit 4]
$ spinup eject deploy/Dockerfile.api
$ spinup check
```

`eject` keeps your edit and stops the updates for that file. `restore --force` drops your edit. See [Concepts](concepts.md#file-classes-managed-scaffold-and-seed).

### A generator refuses to overwrite a file

**Error:** `generator "page" would overwrite existing files with different content`, with `Try: pass --force, or remove the files`

You own the file, and it differs from the new draft. The generator protects your edits.
**Fix:** read the new draft with `--diff`. Then pass `--force` to replace the file, or choose another name.

### The table exists already

**Error:** `the table tasks already exists in api/db/migrations/00002_create_tasks.sql`, with `Try: pass --force to generate the files again, or choose another name`

A migration creates the table. `g resource` stops, so it does not touch your files.
**Fix:** to add a column, write a migration with `spinup g migration <name>`. See [Recipes](recipes.md#add-a-field-to-a-resource).

{: .warning }
`--force` writes the create migration again. If you applied it, `spinup db redo --i-know` deletes the rows of the table.

### A module is missing

**Error:** `cannot generate a mailer: the app has no mail module`, with `Try: spinup module add mail`

The generator belongs to a module that is off.
**Fix:** run the command that the `Try:` line shows. Commit your work first, because `module add` needs a clean tree.

## Upgrade and modules

### The tree is not clean

**Error:** `cannot upgrade: the Git tree has uncommitted changes`, with `Try: commit the changes or pass --allow-dirty`

`spinup upgrade` and `spinup module add` work from a commit, so your edits cannot get lost.
**Fix:** commit or stash your changes. `module add` says `cannot add modules: the Git tree has uncommitted changes`, and has the same fix.

```console
$ git init -q .
$ git add -A
$ git -c user.email=you@example.com -c user.name=you commit -q -m first
$ echo "notes" >> README.md
$ spinup upgrade --yes
Error: cannot upgrade: the Git tree has uncommitted changes
  Try: commit the changes or pass --allow-dirty
[exit 1]
```

### The upgrade stops with conflicts

**Exit code:** 3

You and the new template changed the same lines of a file. spinup lists the files and keeps the upgrade in a separate worktree.
**Fix:**

1. Open each listed file in `.spinup/upgrade/<version>/`. Remove the lines `<<<<<<<`, `=======`, and `>>>>>>>`, and keep the code you want.
2. From the main checkout, run `spinup upgrade --continue --yes`.
3. To give up, run `spinup upgrade --abort --yes`.

### An upgrade is in progress

**Error:** `cannot start upgrade: an upgrade is in progress, use --continue or --abort`

An earlier upgrade or `module add` did not finish.
**Fix:** run `spinup upgrade --continue --yes` or `spinup upgrade --abort --yes`. For a module, use `spinup module add --continue` or `--abort`.

### A template version is unavailable

**Error:** `cannot load templates "0.11.0": template version unavailable`, with `Try: run once without --offline`

spinup keeps downloaded template versions in a cache. The version you need is not there, and `--offline` stops the download.
**Fix:** run `spinup cache warm` while you are online. Then repeat the command with `--offline`.

<!-- doctest: skip -->
```console
$ spinup cache list
$ spinup cache warm
```

If the message says `no merge base`, run `spinup cache warm <version>` with the old version from the message. See [cache](commands/cache.md).

### Two migrations have the same number

**Warning:** `api/db/migrations/00002_create_auth.sql uses migration version 2, also used by 00002_create_tasks.sql`

A module adds numbered migrations. If your app has a migration with the same number, the order is not clear.
**Fix:** follow the `Try:` line. If your migration is not applied yet, rename it to the next free number.
If you applied it, keep its number and rename the migration of the module instead.

{: .tip }
Add modules such as `auth` before you generate resources. Then the numbers do not clash.

### The checks fail after an upgrade

**Message:** `Try: fix the failures in .spinup/upgrade/<version>, then run spinup upgrade --continue`

The upgrade builds and tests the merged code in the worktree. A step failed.
**Fix:** fix the code in the worktree, then run `spinup upgrade --continue --yes`. To skip the checks, pass `--no-verify`.

## Secrets

### sops or age is not installed

**Error:** `run sops: the secrets tool failed`, with `Try: check the installed tool, recipients, and environment key`

This message has three causes: the tool is missing, your key is not a recipient, or the key file is missing.
**Fix:** run `spinup tools install` and repeat the command. If it fails again, read the next entry.

### You do not have the key

**Error:** `run sops: the secrets tool failed`, when you edit or read a secrets file

The private key of an environment lives on your machine at `~/.config/spinup/keys/<app>-<env>.agekey`. It is not in git.
Without it, `sops` cannot decrypt `secrets/<env>.enc.yaml`.
**Fix:** ask a teammate who has the key to add your public key to `.sops.yaml`, then run `spinup secrets rekey --env <env>`. Or use the recovery key.
Set `SPINUP_KEYS_DIR` if your keys are in another folder. See [Secrets](deploy/secrets.md).

{: .warning }
If nobody holds a key, nobody can decrypt the file. Keep a recovery key outside the repository.

### The secrets file does not exist

**Error:** `read encrypted secrets: the environment file is missing or is not a regular file`, with `Try: run spinup secrets init --env staging`

You did not make the secrets of this environment yet.
**Fix:** run `spinup secrets init --env <env> --recovery-recipient age1...`.

### The environment is missing

**Error:** `select secrets environment: --env is missing or invalid`, with `Try: pass --env <environment>`

**Fix:** add `--env staging`, or set `default_env` in `spinup.toml`.

## Deploy

### The environment is unknown

**Error:** `could not resolve environment "staging". Valid environments: .`

The app has no `[env.staging]` table in `spinup.toml`. The empty list after `Valid environments:` shows that no environment exists.
**Fix:** add the environment. See [Environments](deploy/environments.md).

### The deploy needs a git commit

**Error:** `git status failed ... not a git repository`, with `Try: run spinup deploy inside a git repository with at least one commit`

A release is named after the commit it builds.
**Fix:** run `git init`, commit your files, and deploy again. For uncommitted edits, pass `--allow-dirty`.

### The deploy lock is held

**Error:** `deploy lock is held`, with `owner ..., pid ..., host ..., release ...` and `Try: wait, or run spinup deploy unlock --force`. The exit code is 5.

Another deploy to this environment runs now, or an earlier deploy died and left its lock.
**Fix:** wait for the other deploy. If you are sure that nothing runs, check the state, then remove the lock:

<!-- doctest: skip -->
```console
$ spinup deploy status --env staging
$ spinup deploy unlock --force --env staging
```

Then continue the stopped deploy with `spinup deploy --resume --env staging`. See [deploy](commands/deploy.md).

### The new release is not healthy

**Error:** `the green color did not become healthy`, with `Try: read the logs with spinup server logs, correct the problem, and run spinup deploy again`

The new color started but failed its health check. Caddy never switched, so your users still see the old release.
**Fix:** read the logs, fix the cause, and deploy again.

<!-- doctest: skip -->
```console
$ spinup server logs api --env staging
```

If the message says `the color is live but not healthy`, the switch already happened. Run `spinup rollback --env staging`.

### Roll back after a bad release

`spinup rollback` switches to the previous stored release. It does not undo database migrations.

<!-- doctest: skip -->
```console
$ spinup releases list --env staging
$ spinup rollback --env staging
```

Check that the old code works with the new schema before you roll back. See [rollback](commands/rollback.md).

### A protected environment asks for confirmation

**Error:** `cannot <action> in protected environment "production"`, with `Try: pass --confirm-env production, re-run in a terminal, or pass --yes in CI`

An environment with `protected = true` asks you to type its name before a risky command. Without a terminal, it cannot ask.
**Fix:** pass `--confirm-env production`.

## See also

- [Doctor checks](reference/doctor-checks.md): every check and its fix
- [Global flags](reference/global-flags.md): exit codes and flags that work everywhere
- [Upgrade](upgrading.md): the full upgrade steps
- [Environments](deploy/environments.md) and [Secrets](deploy/secrets.md)
