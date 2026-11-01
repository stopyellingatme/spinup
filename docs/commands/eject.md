---
title: eject — take ownership of a file
parent: Command guides
---

# eject — take ownership of a file

`spinup eject <path>` stops spinup from managing one file. After that, the file is yours, and you can edit it freely.
Use it when a managed file does not fit your app and you want to change it.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ echo "# my change" >> deploy/Dockerfile.api
$ spinup check
...
[exit 4]
$ spinup eject deploy/Dockerfile.api
$ spinup check
```

The first `check` fails because you edited a managed file. After `eject`, `check` passes.

## What changes

The command changes the class of the file in `spinup.lock` from `managed` to `scaffold`. It does not touch the file.

| | Before | After |
| --- | --- | --- |
| Class in `spinup.lock` | `managed` | `scaffold` |
| `spinup check` on an edited file | Reports drift. | Accepts your edit. |
| `spinup upgrade` | Replaces the file. | Merges template changes into your edits. |

The command is quiet when it works. [`new`](new.md#file-classes) explains the three file classes.

{: .note }
Eject is one way. You keep your file, but you lose the automatic updates of the template for it.
After an upgrade, check the merge for this file with care.

## Preview with --dry-run

`--dry-run` checks the path and changes nothing:

```console
$ spinup eject deploy/Caddyfile --dry-run
```

## Errors

Only a managed file can be ejected. The command rejects scaffold files, seed files, and paths that the lock does not list:

```console
$ spinup eject api/cmd/api/main.go
Error: cannot eject "api/cmd/api/main.go": the path is not managed
  Try: select a managed path with spinup diff --json
[exit 1]
```

Run `spinup diff --json` to see the class of each file.

## Flags

| Flag | What it does | Example |
| --- | --- | --- |
| `<path>` | The file to take over. | `spinup eject deploy/Caddyfile` |
| `--dry-run` | Validate the change and write nothing. | `spinup eject deploy/Caddyfile --dry-run` |
| `--dir <directory>`, `-C` | Use the app in this folder. | `spinup eject -C ~/code/todo deploy/Caddyfile` |
| `--offline` | Never download template versions. | `spinup eject --offline deploy/Caddyfile` |

## See also

- [`restore`](restore.md) undoes your edit instead.
- [`check`](check.md) reports edits to managed files.
- [`upgrade`](upgrade.md) merges scaffold files and replaces managed files.
- Flag reference: [`eject`](../site/commands/eject.md)
