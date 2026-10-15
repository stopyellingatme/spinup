# Upgrade matrix

Each pull request tests upgrades from the frozen apps in `testdata/corpus/`.
The matrix uses a temporary Git repository and the real upgrade runner.
It applies user edits, checks the exact conflicts, resolves them, and continues the upgrade.
It checks the final lock, preserved edits, tombstones, and clean Git state.
The build mode also runs `go build`, `go vet`, and `go test` in the upgraded API.

The current corpus is synthetic.
Real release corpora start at v0.3.
See `testdata/corpus/README.md` for the format and freeze procedure.

## Run the checks

Run these commands from the repository root:

```sh
go test -race ./internal/upgrade/ -run 'Matrix|LockSchema'
SPINUP_MATRIX_BUILD=1 go test -race ./internal/upgrade/ -run 'Matrix|LockSchema'
go test ./internal/templates/ -run PacksDigest
actionlint .github/workflows/upgrade-matrix.yml
make build lint
make doctest
```

The matrix sets `SPINUP_KIT_REPLACE` to the repository kit.
It uses the local Go toolchain and disables module downloads.
Before you run it, populate the module cache through the normal project installation procedure.
The CI workflow caches the kit dependencies before it starts the offline matrix.

The `coverage` job requires at least 70% statement coverage in each selected package.
It includes `merge`, `upgrade`, `manifest`, and `migrate`, with their subpackages.
It prints each package percentage and fails if any package is below the threshold.

## Review template changes

`internal/templates/packs.digest` records one SHA-256 digest and one migration decision for each pack.
The digest includes every embedded file, including metadata and dotfiles.
The test sorts paths and hashes each path, content length, and content.
File boundaries use NUL delimiters.

After a template change, run:

```sh
SPINUP_GOLDEN_UPDATE=1 go test ./internal/templates/ -run PacksDigest
```

The update writes current digests.
It preserves the migration field for unchanged packs.
It writes `REVIEW` for new or changed packs and fails until you make a decision.

Review the pack diff and the upgrade effects.
Replace `REVIEW` with a registered migration ID when a codemod is necessary.
Use `none` when no codemod is necessary.
Explain the `none` decision in the pull request.
The initial digest baseline uses `none` because it introduces no template change.

Run the digest test again without the update variable.
Commit the template changes, reviewed digest decisions, and applicable corpus edits together.
Do not replace a frozen release tree to hide a regression.
The digest guard verifies a recorded decision.
Human review establishes whether that decision is correct.

## Generate a synthetic starting app

This example tests the generation commands in a temporary directory:

```console
$ spinup new corpus --api-only --module example.com/corpus --no-git
...
$ test -f corpus/spinup.lock
$ test -f corpus/api/internal/routes/routes.go
$ rm corpus/.env
$ test ! -e corpus/.env
```

Use the generated files as the synthetic tree when you deliberately refresh the development corpus.
Preserve dotfiles and file permissions when you copy them.
Refresh the schema fixtures only when their intended inputs change.
