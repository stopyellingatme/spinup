---
title: restore — bring back a template file
parent: Command guides
---

# restore — bring back a template file

`spinup restore <path>` writes a file again, as the template made it.
Use it when you deleted a file by mistake, or when you want to undo your edits.

This command restores one file of your app. To restore a database backup, see [`backup restore`](backup-restore.md).

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ rm deploy/Caddyfile
$ spinup restore deploy/Caddyfile
$ ls deploy/Caddyfile
deploy/Caddyfile
```

The command also updates the hash and the mode of the file in `spinup.lock`.
If you removed the file on purpose, `restore` takes away that note too, so spinup manages the file again.

## Replace an edited file

If the file exists and differs from the template, the command stops and keeps your edits.
Add `--force` to replace them. Your edits are lost, so copy the file first if you want to keep them.

```console
$ echo "# my change" >> deploy/Dockerfile.api
$ spinup restore deploy/Dockerfile.api
Error: cannot restore "deploy/Dockerfile.api": the existing file differs from the template
  Try: spinup restore deploy/Dockerfile.api --force
[exit 1]
$ spinup restore deploy/Dockerfile.api --force
$ spinup check
```

{: .warning }
`--force` overwrites the file on disk. It cannot be undone, unless the file is in Git.

A file that you ejected stays a scaffold file after `restore`. The command keeps that class.

## Preview with --dry-run

`--dry-run` checks the path and the force rule, and writes nothing:

```console
$ spinup restore deploy/Dockerfile.api --force --dry-run
```

## Work offline

`--offline` never downloads a template. If the template of your lock is not in the cache, run `spinup cache warm` once.
`--dir` restores a file of an app in another folder.

```console
$ spinup restore deploy/Caddyfile --dir . --offline
```

## Flags

| Flag | What it does | Example |
| --- | --- | --- |
| `<path>` | The file to restore. | `spinup restore deploy/Caddyfile` |
| `--force` | Replace a file that you edited. | `spinup restore deploy/Caddyfile --force` |
| `--dry-run` | Validate the change and write nothing. | `spinup restore deploy/Caddyfile --force --dry-run` |
| `--dir <directory>`, `-C` | Use the app in this folder. | `spinup restore -C ~/code/todo deploy/Caddyfile` |
| `--offline` | Never download template versions. | `spinup restore --offline deploy/Caddyfile` |

## See also

- [`check`](check.md) tells you which files need a restore.
- [`diff`](diff.md) shows what you changed before you restore.
- [`eject`](eject.md) keeps your edit instead.
- Flag reference: [`restore`](../site/commands/restore.md)
