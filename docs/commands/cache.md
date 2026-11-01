---
title: cache — manage template versions
parent: Command guides
---

# cache — manage template versions

`spinup cache` manages the template versions that `upgrade`, `diff`, and `module add` need.
Spinup keeps them in a cache on your machine.
Use it to download versions before you go offline, to see what you have, or to start clean.

```console
$ spinup cache list
0.0.0-dev	current
```

The first column is the template version.
The second column is where spinup loads it from.

| Source | Meaning |
| --- | --- |
| `current` | The version built into this spinup binary. It is always available. |
| `extra` | A version in the directory of `SPINUP_TEMPLATES_DIR`. |
| `embedded` | An archive packed into the binary. |
| `cache` | A version that you downloaded. |

## List versions

`spinup cache list` shows each version that spinup can load, oldest first.
It never downloads anything.
Add `--json` for a JSON list.

```console
$ spinup cache list --json
[
  {
    "version": "0.0.0-dev",
    "source": "current"
  }
]
```

## Download versions

`spinup cache warm <version>...` downloads the versions you name.
Without arguments, it downloads the version in the `spinup.lock` of the nearest project.
Write versions without a `v`, such as `0.2.0`.
Use `--dir <directory>` to pick another project.

<!-- doctest: skip -->
```console
$ spinup cache warm 0.2.0
```

Each download is signed.
Spinup checks the signature when it downloads, and again each time it loads a cached archive.
A rejected download leaves no files in the cache.

## Clear the cache

`spinup cache clean` removes every downloaded archive and signature.
It prints the directory it removed.
Add `--dry-run` to see the directory first.

```console
$ spinup cache clean --dry-run
...
```

The cache directory is `spinup/templates` inside your user cache directory.

## Work offline

Pass `--offline`, or set `SPINUP_OFFLINE=1`, to forbid all downloads.
Run `cache warm` while you are online, and the other commands can then work offline.
If a version is missing offline, the error says which version to download.
`cache warm` itself refuses offline mode with exit code 2.

```console
$ spinup cache warm 0.2.0 --offline
Error: Cannot warm templates
  offline mode disables downloads
  Try: remove --offline and unset SPINUP_OFFLINE
[exit 2]
```

## Use local templates

If you write or test templates, point `SPINUP_TEMPLATES_DIR` at a directory of versions.
Each version needs `<directory>/<version>/packs/` and `<directory>/<version>/stackversions.yaml`.
A version can also hold `MIGRATION.json`.
Local versions need no signature.

```sh
SPINUP_TEMPLATES_DIR=$PWD/templates spinup cache list
```

## Where versions come from

When you ask for a version, spinup looks in this order and stops at the first match:

1. The current build.
2. The directory in `SPINUP_TEMPLATES_DIR`.
3. The archives embedded in the binary.
4. The user cache.
5. A signed download from the release server.

A download needs a build of spinup that holds the release signing key. Release builds have it.
A development build has no key and cannot download, so use `SPINUP_TEMPLATES_DIR` with it.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `offline mode disables downloads` | Remove `--offline` and unset `SPINUP_OFFLINE`. |
| `no trusted template signing keys are configured in this build` | You run a development build. Install a release with `spinup self-update`, or use `SPINUP_TEMPLATES_DIR`. |
| `template archive signature is invalid` | The download is damaged or not from a release. Try again, or update spinup. |
| `upgrade` cannot find a version offline | Run `spinup cache warm <version>` while online. |

## See also

- [upgrade — move an app to a new version](upgrade.md)
- [module — add optional features](module.md)
- [Flag reference: cache](../site/commands/cache.md), [list](../site/commands/cache-list.md), [warm](../site/commands/cache-warm.md), [clean](../site/commands/cache-clean.md)
