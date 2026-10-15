# ADR 0005: M1 contracts (migrations, lockfiles, doc tests)

Status: accepted (2026-10-14)

The M1 units run in parallel. These decisions fix the contracts between them.

## 1. `spinup db migrate` runs the app binary

**Decision.** `spinup db migrate <up|down|status>` runs `go run ./cmd/api migrate <sub>` in the `api/` directory of the app. The CLI does not import pgx or goose.

**Why.**
- The app and the CLI then run the same migration code, from the kit version that the app pins.
- A CLI at kit minor N can operate an app at kit minor N-1 with no version skew.
- The CLI binary stays free of database drivers.

**Contract.**
- Kit `app` accepts named commands: `app.WithCommand(name string, run func(ctx context.Context, args []string) error)`.
- `(*app.App).Run(ctx, args)` runs the matching command when `args[0]` names one. It serves HTTP when `args` is empty.
- Kit `db` exports `db.MigrateCommand(fsys fs.FS, opts ...MigrateOption) func(ctx context.Context, args []string) error`. The generated `main.go` registers it as `migrate`.
- The CLI side owns the guards: `--i-know` for `down`, and `project.ConfirmProtected` when the target environment is protected.

## 2. The generated API module and its lockfiles

- The API module path is `<module>/api`. Its `go.mod` is at `api/go.mod`.
- The main loop owns `internal/templates/packs/api/pack.d/deps.toml` and its four sources: `go.mod.tmpl`, `go.sum`, `tools.mod.tmpl`, and `tools.sum`. `scripts/bump-stack.sh` writes them.
- `go.mod.tmpl` requires `github.com/stopyellingatme/spinup/kit v0.1.0`. The kit is not published yet. With `SPINUP_KIT_REPLACE=<dir>`, `spinup new` adds a `replace` directive for the kit to `api/go.mod` after the render. The lock records the hash of the file as written.
- The kit may import only the standard library, pgx/v5, goose/v3, and testcontainers-go. A new kit dependency needs a pre-seed commit and a `bump-stack` run.
- At the milestone gate, the main loop runs `BUMP_STACK_MODE=app make bump-stack`. Then it updates the `api` golden files.

## 3. Doc tests

`make doctest` runs every `console` code block in `docs/**/*.md`, except under `docs/design/` and `docs/adr/`.

- A line that starts with `$ ` is a command. The lines after it, up to the next command, are the expected output (stdout and stderr together).
- A line `...` in the expected output matches zero or more lines. Trailing spaces are ignored.
- A command runs with `bash -c` in the current directory of the page. A command of the exact form `cd <dir>` changes that directory for the later commands on the same page.
- Each page starts in a new temporary directory. `spinup` (built from the repository) is on `PATH`. `HOME` is the temporary directory. `SPINUP_NO_UPDATE_CHECK=1` and `NO_COLOR=1` are set.
- A command must exit 0, unless the last expected line is `[exit N]`. Then it must exit N.
- `<!-- doctest: skip -->` on the line before a block skips the block.
- `<!-- doctest: requires-env NAME -->` on the line before a block skips the block when the environment variable `NAME` is empty.
