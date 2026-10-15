# Template cache

`spinup cache` manages the template versions used by `upgrade` and `diff`.
The current build always provides its own template version.

```console
$ spinup cache list --offline
0.0.0-dev	current
$ spinup cache warm 0.2.0 --offline
Error: Cannot warm templates
  offline mode disables downloads
  Try: remove --offline and unset SPINUP_OFFLINE
[exit 2]
```

Use `spinup cache warm <version...>` to download missing versions.
Without arguments, `warm` reads the template version from the nearest project lock.
Use `--dir <directory>` to select a project.
Versions must use semantic version syntax without the `v` prefix.

The store selects sources in this order:

1. The current build.
2. The directory specified by `SPINUP_TEMPLATES_DIR`.
3. The archives embedded in the CLI.
4. The user cache.
5. A signed download from the release server.

`spinup cache list` prints each loadable version and its selected source.
The source is `current`, `extra`, `embedded`, or `cache`.
The list uses ascending semantic version order.
The command never downloads archives.
Use `--json` to receive the list as JSON.

`spinup cache clean` removes the directory for downloaded archives and signatures.
It prints the directory that it removed.
Use `--dry-run` to preview the removal.
The cache directory is `<os.UserCacheDir()>/spinup/templates`.

`--offline` prevents downloads.
`SPINUP_OFFLINE=1` also enables offline mode.
`cache warm` rejects offline mode with exit code 2.
A missing version in offline mode produces a recovery instruction.

Downloads require a valid Ed25519 signature over the archive bytes.
The store verifies the signature again whenever it loads a cached archive.
This development milestone has no trusted release key.
Downloads fail until a release includes a trusted key.
A rejected download creates no cache files.

For local versions, use `SPINUP_TEMPLATES_DIR=<directory>`.
Each version needs `<directory>/<version>/packs/` and `<directory>/<version>/stackversions.yaml`.
The version directory can also contain `MIGRATION.json`.
These local versions do not require release signatures.
