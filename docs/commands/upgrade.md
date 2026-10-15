# Upgrade

`spinup upgrade` applies template changes in a separate Git worktree.
The main checkout keeps its files and branch.

```console
$ spinup upgrade --plan --continue
Error: Invalid upgrade flags
  --plan, --continue, and --abort are mutually exclusive
  Try: select only one upgrade mode
[exit 2]
```

Run `spinup upgrade --plan` to inspect every step.
The command writes `.spinup/plan.json` with file decisions and no file contents.
It also prints the kit notes from `MIGRATION.json`.
The plan does not require confirmation.

Run `spinup upgrade --yes` to apply the upgrade.
Use `--to <version>` to select an exact target.
Without `--to`, the command selects the newest loadable version.
Each intervening minor uses its latest available patch.
The final step uses the exact target.

The command requires Git 2.44.0 or later and a valid `spinup.lock`.
Commit pending changes before the upgrade.
Use `--allow-dirty` to permit changes in the main checkout.
The worktree starts from `HEAD`.
It does not include uncommitted changes from the main checkout.
Add `.spinup/` to `.gitignore` before the upgrade.

The worktree uses `.spinup/upgrade/<target>`.
The branch uses `spinup/upgrade-<target>`.
Each step commits the template changes and the lock separately.
If `go fix` changes files, the command creates a separate commit.

If files conflict, the command exits with code 3 and lists the paths.
Resolve the markers in the upgrade worktree.
Then run `spinup upgrade --continue --yes` from the main checkout.
The command checks every conflicted file before it continues.

Run `spinup upgrade --abort --yes` to remove the worktree and its branch.
The command also removes `.spinup/upgrade.json`.
Without `--yes`, apply, continue, and abort require confirmation in a terminal.
Without a terminal, these commands require `--yes`.

Use `--force-managed` to overwrite edited managed files.
Use `--no-verify` to skip verification.
Use `--commit-conflicts` to commit conflict markers and continue.
Use `--offline` to prevent template downloads.

After success, run the printed `git merge spinup/upgrade-<target>` command.
Then remove the worktree with `git worktree remove <worktree>`.
The upgrade branch remains available for inspection.
