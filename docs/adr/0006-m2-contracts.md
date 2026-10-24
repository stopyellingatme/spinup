# ADR 0006: M2 contracts (upgrade engine)

Status: accepted (2026-10-14)

The M2 units run in parallel. These decisions fix the contracts between them. The main loop owns the four contract files below. Units implement them and never edit them.

## 1. Contract files

| File | Holds | Implemented by |
|---|---|---|
| `internal/templates/source.go` | `templates.Version`, `templates.Source`, `ErrVersionUnavailable` | M2.6 (`internal/templates/store`) |
| `internal/merge/contract.go` | `merge.Input`, `merge.Result`, `merge.Merger`, `merge.MaxSize` | M2.1 (`NewGit`) |
| `internal/upgrade/contract.go` | `Action`, `FileDecision`, `Plan`, `Options`, `Step`, `Phases`, `Extension` | M2.2 (planner), M2.4 (state machine), M2.8 (phases) |
| `internal/migrate/contract.go` | `Stage`, `Ctx`, `Change`, `Migration` | M2.3 (registry and runner) |

## 2. Lock schema 2

The lock gains `packs`: the template packs of the app in render order. `spinup new` writes it. A schema 1 lock (from M1) migrates to `base`, `api`, plus the packs named by the `modules` answer. An upgrade re-renders exactly these packs, with the lock answers.

## 3. Template versions

- A version string is semantic, with no `v` prefix. A development build of the CLI renders `0.0.0-dev`.
- The current build's own packs (`templates.Embedded()` with `stackversions.MustLoad()`) are always a loadable version: the CLI's template version.
- A release archive is `templates-v<version>.tar.zst`: a deterministic tar (sorted paths, zero times, uid and gid 0, modes kept) of `packs/...`, `stackversions.yaml`, and an optional `MIGRATION.json`, compressed with zstd. The CLI embeds the archives of the last three released versions.
- Older versions download from `https://github.com/stopyellingatme/spinup/releases/download/v<version>/templates-v<version>.tar.zst`, with an Ed25519 signature in `templates-v<version>.tar.zst.sig` over the archive bytes. The public keys are compiled into the CLI (`store.TrustedKeys`). Unit M8.3 adds the release key and signs the archives. Downloads land in `<user cache dir>/spinup/templates/` and are verified again on every load.
- `--offline` never downloads. A missing version is an error with `Try: spinup cache warm`.
- Test and development hook: `SPINUP_TEMPLATES_DIR=<dir>` adds every `<dir>/<version>/` that holds `packs/` and `stackversions.yaml` (and optionally `MIGRATION.json`) as a loadable version. Script tests build synthetic versions with it.

## 4. The upgrade run

- Preconditions: a git repository, a clean tree (unless `--allow-dirty`), a valid lock, `min_cli` satisfied, and git 2.44.0 or later (`git merge-file --diff-algorithm` arrived in 2.44).
- The upgrade runs in a git worktree at `.spinup/upgrade/<to>` on the branch `spinup/upgrade-<to>` (critic resolution 1). The user's checkout is never changed. `.spinup/` is git-ignored.
- The state file `.spinup/upgrade.json` (in the main checkout) records the chain, the current step, the worktree, and the status. `--continue` and `--abort` read it. `--abort` removes the worktree and deletes the branch, after confirmation.
- Chaining: when the app is more than one minor behind, each step goes to the latest patch of the next minor, and the last step goes to `--to` (default: the newest loadable version). Each step makes its commits on the branch.
- When the step finishes with no conflict markers, the CLI prints `git merge spinup/upgrade-<to>` as the next step. Conflicts exit 3 (unless `--commit-conflicts`). The user resolves them in the worktree and runs `spinup upgrade --continue`.
- When the OLD version cannot be loaded, `BuildPlan` still works: unchanged files (disk hash equals lock hash) take NEW, and changed files merge against an empty base, so they become conflicts with the reason `no base version`. The runner (`Run` and `--plan`) does not accept such a plan. It fails before it writes, names the files, and ends with `Try: run spinup cache warm <version>, or set SPINUP_TEMPLATES_DIR ...`, because a whole-file conflict hides the real difference.
- `api/go.mod`, `api/go.sum`, `api/tools.mod`, and `api/tools.sum` are never merged as text, because `go mod tidy` changes the `require` block of the app. When the app changed one of them, the plan keeps the app file (`keep`, reason `go.mod: the dependency step applies the template changes`). The Deps phase then raises the kit to the higher of the template version and the kit pin (never lowers it), raises each stack-pinned module, raises the `go` line to the line of the new render, and runs `go mod tidy`. When the app did not change the file, the plan overwrites it.
- `SPINUP_KIT_REPLACE`: when it is set, the Apply phase adds the kit `replace` directive to `api/go.mod` again, as `spinup new` does.

## 5. Codemods and kit notes

- Codemods live in `internal/migrate`, one per file named `NNNN_slug.go`, registered in `init()`. They edit code only through `internal/codemod` (Go AST, comment-keeping YAML, TOML, and JSON editors), never through regular expressions.
- `MIGRATION.json` (kit notes, shown by `upgrade --plan`): `{"schema":1,"version":"0.2.0","steps":[{"id":"...","desc":"...","kind":"codemod|fix-inline|manual","migration":"<codemod id, for kind codemod>"}]}`.
