# Upgrade

`spinup upgrade` applies template changes in a separate Git worktree.
The main checkout keeps its files and branch.

The Rust CLI supports template upgrades, conflict recovery, codemods, and `--ui`.
It requires lock schema 3 and templates with the `minijinja` engine marker.
It rejects Go locks and Go template archives.
It updates the Cargo dependencies of `api/` and verifies the app with Cargo.

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
The Go CLI creates a separate commit when `go fix` changes files.
The Rust CLI has no fix step.

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

## What an upgrade changes

The upgrade runs codemods before and after the template changes.
Each codemod prints its ID and description.
The lock records completed codemods to prevent repeated changes.

### Rust dependency steps

These dependency and verification steps apply to the Rust CLI.
The Go CLI runs the steps in the next section.

The upgrade does not merge `api/Cargo.toml` and `api/Cargo.lock` as text.
If you did not edit `api/Cargo.toml`, the upgrade writes the new render.
If you edited the file, the upgrade keeps your file and edits it with a TOML editor.
The editor adds the new dependencies and features of the template.
The `default` feature list follows the template and keeps the features that you added.
The upgrade increases a dependency pin only when the target stack pins a higher version.
The upgrade never decreases a pin.
It also increases `rust-version` to the minimum Rust version of the stack.

For each increased pin, the upgrade prints `Dependency <name>: <old> -> <pin>`.
It then runs `cargo update -p <name>@<old> --precise <pin>` in `api/`.
This moves the lock to the pin and does not update other crates.
If only features or dependencies change, the upgrade runs `cargo metadata --format-version 1` once.
With `--offline`, these Cargo commands also get `--offline`.

If `SPINUP_KIT_REPLACE` is set, the upgrade writes the kit patch with that directory.
The kit patch is the `spinup-kit` entry of `[patch.crates-io]` in `api/Cargo.toml`.
If the variable is not set, the upgrade keeps the kit patch of the current `api/Cargo.toml`.
If a relative patch path points outside the repository, the upgrade writes the absolute path and prints a warning.
If the patch directory does not exist, the upgrade stops with a `Try:` line.
`spinup module add` follows the same rules.

If the app has `web/package.json`, the upgrade then refreshes the web lockfile, as the Go CLI does.

Verification runs `cargo clippy --locked --all-targets -- -D warnings` and `cargo test --locked` in `api/`.
The Cargo commands run without `RUSTFLAGS`, `CARGO_ENCODED_RUSTFLAGS`, and `LLVM_PROFILE_FILE`.
If the app has `web/package.json`, verification then runs the `check` and `build` scripts in `web/`.
It then runs every local doctor check against the worktree.
If the step has committed conflict markers, the upgrade skips verification and prints a line.
The error lists all failures and includes the last codemod ID for a file when available.
After repairs, run `spinup upgrade --continue --yes` to repeat verification.

### Go dependency steps

These dependency and verification steps apply to the Go CLI.
The Rust CLI does not run them.

If `SPINUP_KIT_REPLACE` is set, the upgrade restores the local kit replacement in `api/go.mod`.
If the variable is not set, the upgrade keeps a local kit replace that the current `api/go.mod` has.
If that replace points to a directory that does not exist, the upgrade stops with a `Try:` line.
`spinup module add` follows the same rules.
The upgrade increases direct Go requirements only when the target stack pins a higher version.
The upgrade does not request higher versions for other Go requirements.
For a release target, the kit requirement uses `v<target>`.
For a prerelease target, the kit requirement keeps its version.
The managed `api/tools.mod` file receives its pins through the template changes.

The upgrade runs `go mod tidy` with the user's environment.
This command can need network access for modules missing from the cache.
If the app has `web/package.json`, the upgrade then runs `pnpm install --lockfile-only --prefer-offline --ignore-scripts` in `web/`.
`web/pnpm-lock.yaml` is a seed file, so the upgrade regenerates it instead of merging it.
If `web/package.json` has a conflict, the upgrade skips this step and tells you to run `pnpm install` after you resolve it.
The upgrade then runs `go fix ./...` in `api/`.
Changes from `go fix` receive a separate commit.

Verification runs `go build ./...`, `go vet ./...`, and `go test ./...` in `api/`.
It then runs every local doctor check against the worktree.
The error lists all failures and includes the last codemod ID for a file when available.
Verification failures retain the commits for inspection and repair.
After repairs, run `spinup upgrade --continue --yes` to repeat verification.

```console
$ spinup upgrade --help
Upgrade app templates in a separate Git worktree
...
```

## Updating UI components

The shadcn-svelte components are seed files under `web/src/lib/components/ui/`.
A normal upgrade preserves these files.
If the target registry differs from the lock answer, the upgrade prints a note.

Run `spinup upgrade --ui --yes` to update the components during a template upgrade.
The command uses the old render, the worktree file, and the new render for a three-way merge.
Files without user changes receive the new render.
Deleted components stay deleted.
New components are created.
The lock records the target `shadcn_registry` default and the component entries.

Conflicting edits receive conflict markers and exit code 3.
The error lists the component paths in the upgrade worktree.
The step commits successful updates before verification.

```console
$ spinup upgrade --ui --plan --continue
Error: Invalid upgrade flags
  --plan, --continue, and --abort are mutually exclusive
  Try: select only one upgrade mode
[exit 2]
```
