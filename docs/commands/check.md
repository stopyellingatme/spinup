---
title: check — find drift for CI
parent: Command guides
---

# check — find drift for CI

`spinup check` tells you whether your app still matches what spinup made.
It exits with 0 when the app is clean and with 4 when it finds a problem.
Run it in CI, and before you start an upgrade.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup check
$ echo "exit code: $?"
exit code: 0
```

A clean app prints nothing. The command compares each file with the hash in `spinup.lock`.

## Read a problem report

Edit a [managed file](../concepts.md) and the command reports it. Each problem comes with a fix:

```console
$ echo "# my change" >> deploy/Dockerfile.api
$ spinup check
deploy/Dockerfile.api: managed file differs from its lock hash (drift)
Try: spinup eject deploy/Dockerfile.api or spinup restore deploy/Dockerfile.api
Error: Application check failed
  the project has drift
  Try: correct the reported problems and run spinup check
[exit 4]
```

You have two ways out. Run [`spinup restore`](restore.md) to undo your edit, or [`spinup eject`](eject.md) to keep it and own the file.

```console
$ spinup restore deploy/Dockerfile.api --force
$ spinup check
```

## What counts as a problem

The command exits with 4 for any of these:

- A managed file differs from the lock (state `drift`).
- A managed file is missing (`missing`).
- A tracked path is not a regular file, for example a symbolic link (`not-regular`).
- A file has conflict markers (`<<<<<<<`) from an unfinished merge (`conflict`).
- `spinup.lock` is invalid (`invalid-lock`).
- The template version of the lock cannot load, for example when it is not in the cache (`template-unavailable`).
- A newer template version is available (`behind`).

`--allow-behind` skips the last two checks.

A scaffold file that you edited is fine, as long as it has no conflict markers.
Scaffold files are yours. A seed file is yours too. See [`new`](new.md#file-classes) for the three file classes.

## Get JSON

`--json` prints one object with a `problems` array. The array is empty when the app is clean.

```console
$ spinup check --json
{
  "problems": []
}
```

With a problem, each item of the array looks like this:

```json
{
  "path": "deploy/Dockerfile.api",
  "state": "drift",
  "message": "managed file differs from its lock hash",
  "try": "spinup eject deploy/Dockerfile.api or spinup restore deploy/Dockerfile.api"
}
```

## Allow a newer template

When spinup finds a newer template version, `check` fails so that you notice the upgrade.
The message names the version: `newer template available: <version>`.
If you want CI to stay green until you upgrade, pass `--allow-behind`:

```console
$ spinup check --allow-behind
```

## Run without a network

`--offline` never downloads a template. If the template of your lock is not in the cache, run `spinup cache warm` once.
`--dir` checks an app in another folder.

```console
$ spinup check --dir . --offline
```

## Flags

| Flag | What it does | Example |
| --- | --- | --- |
| `--allow-behind` | Do not fail when a newer template exists. | `spinup check --allow-behind` |
| `--json` | Print a `problems` object. | `spinup check --json` |
| `--dir <directory>`, `-C` | Check the app in this folder. | `spinup check -C ~/code/todo` |
| `--offline` | Never download template versions. | `spinup check --offline` |

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `managed file differs from its lock hash` | You edited a managed file. | Run `spinup restore <path>`, or `spinup eject <path>` to keep the edit. |
| `managed file is missing` | You deleted a managed file. | Run `spinup restore <path>`. |
| `file contains conflict markers` | A merge from `spinup upgrade` is not finished. | Edit the file, remove the markers, and run `spinup check` again. |
| `newer template available` | A newer template version is in the cache. | Run `spinup upgrade`, or pass `--allow-behind` in CI. |
| A template is not available offline. | The cache lacks the template version. | Run `spinup cache warm`, then retry. |

## See also

- [`diff`](diff.md) shows what changed in each file.
- [`upgrade`](upgrade.md) moves the app to a newer template.
- [Generated CI](../ci.md) runs `spinup check` on every push.
- Flag reference: [`check`](../site/commands/check.md)
