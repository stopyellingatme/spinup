---
title: upgrade — move an app to a new version
parent: Command guides
---

# upgrade — move an app to a new version

`spinup upgrade` brings the template files of your app up to a newer template version.
It works in a separate Git worktree and branch, so your main checkout stays as it is until you merge.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup upgrade --plan
Already up to date.
```

## Upgrade in four steps

1. Commit your work. Check that `.gitignore` lists `.spinup/`. New apps already list it.
2. Preview the change: `spinup upgrade --plan`.
3. Apply it: `spinup upgrade --yes`.
4. Merge the branch that the command prints, then remove the worktree.

<!-- doctest: skip -->
```console
$ spinup upgrade --yes
...
Next: git merge spinup/upgrade-0.2.0
Worktree: .spinup/upgrade/0.2.0
Remove it with: git worktree remove .spinup/upgrade/0.2.0
$ git merge spinup/upgrade-0.2.0
$ git worktree remove .spinup/upgrade/0.2.0
```

The branch stays after the merge, so you can still inspect it.
To upgrade the spinup binary itself, use [self-update](self-update.md).
New to the terms managed, scaffold, and seed file? Read [Concepts](../concepts.md).

## Preview with --plan

`--plan` shows what the upgrade would do and changes nothing.
It writes `.spinup/plan.json` with the decision for each file, but no file contents.
It also prints the notes for the kit library.
It does not ask for confirmation.

```console
$ spinup upgrade --plan
Already up to date.
```

When a newer version exists, the plan counts each action and lists the files:

<!-- doctest: skip -->
```console
$ spinup upgrade --plan
Upgrade 0.1.0 -> 0.2.0
merge: 3
  api/cmd/api/main.go (...)
overwrite: 12
  deploy/compose.yml
create: 4
  ...
keep: 98
  README.md (seed)
```

| Action | Meaning |
| --- | --- |
| `overwrite` | A managed file that you did not edit gets the new version. |
| `merge` | You edited the file, so spinup merges the old, your, and new versions. |
| `create` | The new version adds the file. |
| `keep` | The file is a seed or scaffold file, so your copy stays. |

## Choose the target version

By default the upgrade goes to the newest version that spinup can load.
It steps through each minor version with that minor's latest patch, then the exact target.
Use `--to <version>` for another target.

<!-- doctest: skip -->
```console
$ spinup upgrade --to 0.2.0 --yes
```

## Apply the upgrade

`spinup upgrade --yes` needs Git 2.44 or later and a valid `spinup.lock`.
The worktree starts from `HEAD`, so commit first.
Without `--yes`, the command asks for confirmation in a terminal.
Without a terminal, it needs `--yes`.

| Flag | What it does | Example |
| --- | --- | --- |
| `--allow-dirty` | Allow uncommitted changes in the main checkout. They are not in the worktree. | `spinup upgrade --yes --allow-dirty` |
| `--force-managed` | Overwrite managed files that you edited. Your edits are lost. | `spinup upgrade --yes --force-managed` |
| `--no-verify` | Skip the build and tests in the worktree. | `spinup upgrade --yes --no-verify` |
| `--offline` | Never download template versions. | `spinup upgrade --yes --offline` |

{: .warning }
`--force-managed` replaces your edits in managed files with the new template. Run `--plan` first and read the `overwrite` list.

The worktree is `.spinup/upgrade/<target>` and the branch is `spinup/upgrade-<target>`.
Each step commits the template changes and the lock as separate commits.

## If files conflict

If a file has changes that spinup cannot merge, the command exits with code 3 and lists the files.
Fix them in the upgrade worktree, then continue from the main checkout.

1. Open each listed file in the worktree.
2. Remove the `<<<<<<<`, `=======`, and `>>>>>>>` lines. Keep the code you want.
3. Run `spinup upgrade --continue --yes`.

<!-- doctest: skip -->
```console
$ spinup upgrade --yes
Error: Upgrade has conflicts
  api/cmd/api/main.go
  Try: resolve the markers in .spinup/upgrade/0.2.0, then run spinup upgrade --continue
[exit 3]
$ vim .spinup/upgrade/0.2.0/api/cmd/api/main.go
$ spinup upgrade --continue --yes
```

The command checks every conflicted file before it continues.
`spinup module add --continue` follows the same flow.

To give up, run `spinup upgrade --abort --yes`.
It removes the worktree, the branch, and `.spinup/upgrade.json`.

Use `--commit-conflicts` to commit the markers and fix them later.
Do not merge the branch until the markers are gone.

## What an upgrade changes

An upgrade does these things in the worktree, in this order:

1. It runs the codemods that belong before the template change. A codemod is a small script that rewrites your code for a new version. Each one prints its ID and a description, and the lock records it, so it runs once.
2. It applies the template changes to the files, then runs the codemods that belong after them.
3. It runs `go mod tidy`, which can need the network for modules that are not cached.
4. If the app has `web/package.json`, it runs `pnpm install --lockfile-only` in `web/` to refresh the lockfile. If `web/package.json` has a conflict, it skips this. Run `pnpm install` after you fix the conflict.
5. It runs `go fix ./...` in `api/` and commits those changes on their own.
6. It checks the result: `go build`, `go vet`, and `go test` in `api/`, then every local doctor check.

If a check fails, the commits stay in the worktree.
Fix the code there and run `spinup upgrade --continue --yes` to check again.

### Local kit

If you set `SPINUP_KIT_REPLACE`, the upgrade writes that local kit path to `api/go.mod`.
If you did not, it keeps a local kit `replace` that the file already has.
If that path does not exist, the upgrade stops and tells you.
The upgrade raises a Go requirement only when the new stack pins a higher version.
It moves the kit requirement to the target version.
`spinup module add` follows the same rules.

## Update UI components

The shadcn-svelte components in `web/src/lib/components/ui/` are seed files, so a normal upgrade leaves them alone.
Add `--ui` to merge the new component versions too.

<!-- doctest: skip -->
```console
$ spinup upgrade --ui --yes
```

The merge compares the old render, your file, and the new render.
Files that you did not edit get the new version.
Components that you deleted stay deleted, and new components are created.
A component that you edited and that changed gets conflict markers and exit code 3.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `the Git tree has uncommitted changes` | Commit your work, or pass `--allow-dirty`. |
| `--plan, --continue, and --abort are mutually exclusive` | Pass only one of them. |
| Exit code 3 | A file conflicts. Resolve it, then run `spinup upgrade --continue --yes`. |
| A verification error after the merge step | Fix the code in the worktree. Run `spinup upgrade --continue --yes`. |
| The target version is not available offline | Run `spinup cache warm <version>` online. |

```console
$ spinup upgrade --plan --continue
Error: Invalid upgrade flags
  --plan, --continue, and --abort are mutually exclusive
  Try: select only one upgrade mode
[exit 2]
```

## See also

- [self-update — update the spinup binary](self-update.md)
- [cache — manage template versions](cache.md)
- [module — add optional features](module.md)
- [Upgrade guide](../upgrading.md)
- [Flag reference: upgrade](../site/commands/upgrade.md)
