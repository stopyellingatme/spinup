# Script test isolation

Each script has its own `HOME` under `$WORK`.
This keeps generated files and configuration separate between scripts.
The harness gives Git an identity for commits in temporary repositories.
The harness preserves the real Go caches for offline builds.

## pnpm caches

`SPINUP_PNPM_HOME` records the real home before the harness changes `HOME`.
The wrapper at `scripts/bin/pnpm` uses this home to find corepack and pnpm data.
Without this path, offline installs would use an empty home.

The U43-A seed gives each script its own `npm_config_cache_dir` at `$WORK/pnpm-cache`.
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

`SPINUP_PNPM_STORE` keeps its existing selection rules.
An explicit value takes priority over `npm_config_store_dir`.
Otherwise, the harness uses the default store for the platform.
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

Keep issue #43 part 2 open during the observation period.
Close it only after CI and the gates remain green for several weeks.
A single successful local run does not complete this requirement.
