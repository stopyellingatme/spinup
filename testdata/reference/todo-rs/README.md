# The frozen reference app (R0.9)

This directory is a copy of `spikes-rs/r0-09-ref-web` from the R2 pre-seed. It is frozen. Do not edit it.

The copy changes three relative paths for the deeper location: `run.sh` (the repository root), `kit/src/apidocs.rs` (the Scalar bundle), and `kit/tests/telemetry.rs` (the monitoring pack). Nothing else differs from the spike.

- It has its own Cargo workspace and lock. The root workspace excludes `testdata`.
- `make rs-ref-check` runs `cargo check` on it with `SQLX_OFFLINE=true` and its own target directory.
- The R2 parity criteria use it (ADR 0021 §12). Every SQL text of the generated `todo` resource equals the `query` field of the matching file in `.sqlx/`. The spec that the gate app emits equals `todo-api/openapi.gen.yaml`, except `info`.
- `make rs-app-check SET=full RESOURCE=1` copies `.sqlx/*.json` into the generated app.
- `run.sh` is the R0.9 browser check. It needs Postgres, pnpm, and Playwright.
