# The Rust engine (R1)

This page describes the R1 engine of the Rust `spinup` binary.
R1 covers the commands `new`, `check`, `diff`, `eject`, `restore`, and `upgrade`.
The Go binary stays the reference until R5.
The code blocks on this page run only against the Rust binary.

## Create an application

`spinup new` renders template packs into a new directory.
The packs are `base`, `api`, `web`, `ci`, and `deploy`.
Use `--api-only` to omit the `web` pack.
The command writes `spinup.lock` with lock schema 3.
The lock holds the hash of each rendered file.

<!-- doctest: only rust -->
```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive | grep -o 'Created todo'
Created todo
$ cd todo
$ ls spinup.lock spinup.toml
spinup.lock
spinup.toml
```

The output is the same on each run with the same inputs.
The render does not use the clock, random values, or map order.
Use `--dry-run` to print each path with its file class.
The classes are `managed`, `scaffold`, and `seed`.

<!-- doctest: only rust -->
```console
$ cd ..
$ spinup new preview --module example.com/preview --no-git --non-interactive --dry-run | grep '^.gitattributes'
.gitattributes (managed)
$ test ! -e preview
```

## Check for drift

`spinup check` compares each tracked file with the hash in `spinup.lock`.
Only `managed` files count as drift.
You can edit `scaffold` and `seed` files without a report.
If the project has drift, the command exits with code 4.

<!-- doctest: only rust -->
```console
$ cd todo
$ spinup check
$ echo '# mine' >> .gitattributes
$ spinup check
.gitattributes: managed file differs from its lock hash (drift)
...
[exit 4]
```

## Show a difference

`spinup diff [path]` prints a unified diff between the lock state and the file.
If nothing changed, the command prints nothing.

<!-- doctest: only rust -->
```console
$ spinup diff .gitattributes
diff a/.gitattributes b/.gitattributes
...
+# mine
```

## Keep an edit with eject

`spinup eject <path>` takes ownership of a managed file.
After the command, the upgrade engine does not overwrite the file.
The check no longer reports the file as drift.
If the path is not managed, the command exits with code 1.

<!-- doctest: only rust -->
```console
$ spinup eject .gitattributes
$ spinup check
$ spinup eject .gitattributes
Error: cannot eject ".gitattributes": the path is not managed
  Try: select a managed path with spinup diff --json
[exit 1]
```

## Get a file back with restore

`spinup restore <path>` renders the file again from the template.
If the file is missing, the command writes it.
If the file exists and differs, the command stops.
Use `--force` to replace the edited file.

<!-- doctest: only rust -->
```console
$ rm renovate.json
$ spinup restore renovate.json
$ test -f renovate.json
$ echo '{}' > renovate.json
$ spinup restore renovate.json
Error: cannot restore "renovate.json": the existing file differs from the template
  Try: spinup restore renovate.json --force
[exit 1]
$ spinup restore renovate.json --force
```

## Upgrade a template

`spinup upgrade` applies a new template version in a separate Git worktree.
The R1 engine needs lock schema 3 and templates with the `minijinja` marker.
It rejects Go locks and Go template archives.
Cargo dependency changes and build verification start in R2.

The three upgrade modes exclude each other.

<!-- doctest: only rust -->
```console
$ spinup upgrade --plan --continue
Error: Invalid upgrade flags
  --plan, --continue, and --abort are mutually exclusive
  Try: select only one upgrade mode
[exit 2]
```

If no upgrade is in progress, `--continue` and `--abort` stop with an error.

<!-- doctest: only rust -->
```console
$ spinup upgrade --abort
Error: load upgrade state: no upgrade is in progress
  Try: start spinup upgrade
[exit 1]
```

### A conflict, continue, and abort

The next steps cannot run in a doctest block.
They need a second template version, so the block uses the `text` format.

```text
$ spinup upgrade --yes --no-verify
...
[exit 3]
```

If the user and the template changed the same lines, the command exits with code 3.
It lists the conflicted paths.
The worktree is `.spinup/upgrade/<target>` and the branch is `spinup/upgrade-<target>`.
The main checkout does not change.
The command records its state in `.spinup/upgrade.json`.

To continue, do these steps:

1. Edit each conflicted file in the upgrade worktree.
2. Remove the conflict markers.
3. Run `spinup upgrade --continue --yes` in the main checkout.

If a file still has markers, `--continue` exits with code 3 again.
When the upgrade is complete, the command prints a `git merge` command.
Run that command to merge the upgrade branch.

To stop, run `spinup upgrade --abort --yes`.
The command removes the worktree, the branch, and `.spinup/upgrade.json`.
