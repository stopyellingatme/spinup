# Upgrade corpus

Each corpus preserves the generated app from one template version and one answer set.
Release corpora start at v0.3, as critic resolution 19 requires.
The `0.0.0-dev/api` corpus is synthetic.
It does not establish support for an earlier release.

The directories use semantic versions without a `v` prefix:

```text
<template-version>/<answer-set>/
  tree/         Frozen generated files, including spinup.lock
  edits/        User files that overwrite paths in tree/
    DELETE      Optional list of paths to remove
  expect.toml   Exact conflicts, orphans, and tombstones
_next/          Overlay for the synthetic target version
locks/          Schema 1 and schema 2 lock fixtures
```

The `tree/` directory includes dotfiles, empty files, and permission bits.
It excludes `.env`, Git metadata, binaries, and caches.
The lock preserves the original answers and file hashes.
Do not regenerate a frozen release tree with a newer CLI.

The `edits/` directory mirrors the app paths.
Each regular file replaces the corresponding app file.
Each nonempty line in `DELETE` removes one relative path after the replacements.
Do not use absolute paths or parent directory references.

The expectation file contains three arrays:

```toml
conflicts = ["api/internal/routes/routes.go"]
orphans = []
tombstones = ["README.md"]
```

The synthetic edits add `GET /custom`, change a managed Compose port, and delete the seed `README.md`.
They also change the package comment in `routes.go`.
The `_next/` overlay changes that comment and the Compose health interval.
The matrix retains both comments when it resolves the expected conflict.
It checks that the route and both Compose changes survive.

The `_next/` overlay mirrors `packs/` and `stackversions.yaml` from a template version directory.
Its optional `DELETE` file uses the same relative-path rules.
The helper copies the current embedded packs before it applies this overlay.
The synthetic target version is `0.0.1`.
Future release corpora will need a target version after their frozen version.

The schema 1 fixture comes from an M1 app generated with the CLI at commit `6541a8f`.
The schema 1 fixture omits `packs`.
The schema 2 fixture adds `base`, `api`, and `ci`, in that order.

To freeze a release corpus:

1. Use the released CLI from v0.3 or later.
2. Generate the default app in a temporary directory with `--no-git`.
3. Remove `.env` and confirm that no secrets remain.
4. Copy all remaining files into `<version>/<answer-set>/tree/`.
5. Add representative user files under `edits/`.
6. Declare the exact conflicts, orphans, and tombstones in `expect.toml`.
7. Make the release templates available through an embedded archive or the local template store.
8. Select a target version after the frozen version.
9. Run the matrix with `SPINUP_MATRIX_BUILD=1`.

Keep the per-PR matrix limited to default answers.
Reserve additional answer combinations for the nightly matrix.
See `docs/contributing/upgrade-matrix.md` for the checks and digest review procedure.
