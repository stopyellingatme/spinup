---
title: Script test isolation
parent: Contributing
nav_order: 12
---

# Script test isolation

Each testscript gets its own `HOME`, so scripts cannot change each other's files or caches.
This page explains how the pnpm and Bun caches work in scripts, and what to record when a script fails under load.

```sh
make script RUN=pnpm_cache_isolated
```

## HOME and the Go caches

Each script has its own `HOME` under `$WORK`.
This keeps generated files and configuration separate between scripts.
The harness gives Git an identity for commits in temporary repositories.
The harness preserves the real Go caches for offline builds.

## pnpm caches

`SPINUP_PNPM_HOME` records the real home before the harness changes `HOME`.
The wrapper at `scripts/bin/pnpm` uses this home to find corepack and pnpm data.
Without this path, offline installs would use an empty home.

The harness gives each script its own `npm_config_cache_dir` at `$WORK/pnpm-cache`.
The harness selects the real cache in this order:

1. Use the caller's `npm_config_cache_dir`, when present.
2. On macOS, use `<home>/Library/Caches/pnpm`.
3. On other platforms, use `${XDG_CACHE_HOME:-<home>/.cache}/pnpm`.

The harness copies `lockfile-verified.jsonl` into the script cache.
If the source file is missing, the harness creates an empty file.
pnpm can append verification records without changing the real file.
When the real `metadata` directory exists, the harness links it into the script cache.
Offline installs read this metadata through the link.
The caller's cache override selects the seed source, never the writable destination.

To use another cache or store for one run, set the variable before the command:

```sh
npm_config_cache_dir=/data/pnpm-cache SPINUP_PNPM_STORE=/data/pnpm-store make script RUN=pnpm_cache_isolated
```

The store has its own selection rules.
An explicit `SPINUP_PNPM_STORE` takes priority over `npm_config_store_dir`.
Otherwise, the harness uses the default store of the platform:
`<home>/Library/pnpm/store` on macOS, and `${XDG_DATA_HOME:-<home>/.local/share}/pnpm/store` on other platforms.
Treat this shared store as read-only during script tests.
Use offline installs with dependencies that already exist in the store.
Do not warm or repair the shared store from a script.

## Bun caches

The `web_variants` script uses `cache.ts` from its txtar archive.
This helper selects cached packages from `bun.lock`.
It copies those packages from the real Bun cache into `$WORK/bun-cache`.
The script selects this copy through `BUN_INSTALL_CACHE_DIR` and `--cache-dir`.
The install uses `--backend=copyfile` to keep the shared cache unchanged.

## Failures under load

Capture the full pnpm stderr when a script fails under load.
Record the selected `SPINUP_SCRIPT_RUN` name or glob.
Record the names and number of scripts that ran in parallel.
Include the command, operating system, pnpm version, and cache overrides.
Preserve the complete failure log before a repeat run replaces it.

Use this template in the issue:

```text
Script:       <SPINUP_SCRIPT_RUN name or glob>
Parallel:     <names and number of scripts that ran together>
Command:      <the exact command>
System:       <operating system, pnpm version>
Overrides:    <npm_config_cache_dir, SPINUP_PNPM_STORE, BUN_INSTALL_CACHE_DIR>
pnpm stderr:  <the full output>
```

The following commands are manual examples.
The documentation test does not execute this block.

<!-- doctest: skip -->
```console
$ SPINUP_SCRIPT_RUN=pnpm_cache_isolated go test -race ./internal/testutil/scripttest/...
$ make script RUN=pnpm_cache_isolated
```

## Backup lock tests

Keep the backup lock tests serial.
Do not add `t.Parallel()` to these tests.
Use `lockScriptTimeout` for each lock script timeout.
This bound allows extra time when the machine has a heavy load.
Do not reduce it to shorten a test run.

## See also

- [Contributing](index.md)
- [Docker-in-Docker deploy suite](dind.md)
- [Browser end-to-end suite](e2e.md)
