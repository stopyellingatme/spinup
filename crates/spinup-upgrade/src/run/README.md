# Upgrade integration

Create `Runner` with the project root, template source, subprocess runner, merger, cancellation signal, and output writer.
The defaults use `source::Packs` and the null plugin source.

Call `plan` to preview the chain and save `.spinup/plan.json`.
Call `run` to apply the chain in an isolated worktree.
Call `continue_upgrade` or `abort` to recover the recorded operation.
Call `add_modules` with `module::ModuleOptions` for a same-version upgrade.

Apply each registered extension's `args` function to the upgrade command.
Pass the parsed `ArgMatches` through `Runner::matches`.
The registered extension ID is `ui`.
The state records the `--ui` choice for recovery after a restart.

Set `interactive` only when a terminal is available.
Supply a `Prompter` for interactive confirmation.
Use `Options::yes` to bypass confirmation.

The recovery state has mode 0600.
The state machine writes project files through VFS and saves the lock through the manifest crate.
Temporary directories contain only the files that planning reads.
The directory guard removes those files when the preview finishes.

The state machine calls `cargo::phases()`.
R1 provides empty phases and keeps the Go dependency files according to the planner's decisions.
When the old version is unavailable, planning receives `None`.
The required `Step::from` field then contains the version name and an empty pack tree.
The UI extension rejects that empty tree when it needs the old render.
