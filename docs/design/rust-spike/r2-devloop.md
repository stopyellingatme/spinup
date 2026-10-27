# R2 Dev loop re-measure

R2 gate item 4 measures the R0.11 handler-body loop again, with the real kit and the generated Rust API. The limit is still 5 s for the median. The R0.11 numbers are 5458 ms (own driver, default profile) and 3381 ms (the shipped dev profile, ADR 0019 §4).

## Method

- `make rs-devloop` runs `scripts/rust/devloop.py` with the 20 resources of `spikes-rs/r0-11-devloop/gen/resources.txt`, 2 series, and 10 measured edits in each series.
- The script runs `spinup new` with the kit through `SPINUP_KIT_REPLACE` and `--modules ci,auth,jobs,mail`. Then it runs one `spinup g resource` for each resource line.
- It adds `GET /api/greeting` to `api/src/routes.rs`. The handler runs a `query_scalar!` count of the first resource table and returns a marker, such as `mark-0003`.
- It creates a new database from `DATABASE_URL`, runs `spinup db prepare`, and builds the API with `cargo build` and the shipped `[profile.dev]` of `api/Cargo.toml`.
- The driver is the R0.11 own driver, inline in the script (`spinup dev` comes in R3c). It waits 10 ms, runs `cargo build`, stops the old app with SIGTERM, starts the new binary, and polls `/healthz` every 2 ms. It has no file watcher and no `listenfd`.
- A sample starts just before the script writes the edit. It ends at the first `200` answer from `/api/greeting` with the new marker. A second thread polls every 5 ms with a new connection.
- Each series has 1 warm-up edit, then the measured edits, with 1.5 s of quiet between edits. The script waits while the 1-minute load average is 4 or more (at most 20 minutes). It records `uptime` before and after each series.
- `docs/design/rust-spike/r2-devloop.json` holds the samples, the median, the p90 (nearest rank), the step medians (build, stop, ready, response), the load, and `pass` for each series.
- The script removes the app process, the temporary app, and the database on exit and on Ctrl-C.

## Numbers

The main loop adds the numbers at the R2 gate.
