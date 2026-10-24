# R0 spikes

Each directory `r0-NN-<name>/` answers one R0 question of `docs/design/rust-plan.md` §4. Each directory is its own Cargo workspace, with its own `Cargo.lock`. The root workspace (from R1) excludes `spikes-rs`.

## Rules

- The main loop pre-seeded each workspace with its candidate crates. It ran `cargo fetch --locked` into the run's shared `CARGO_HOME`.
- Build offline: `CARGO_NET_OFFLINE=true`, `RUSTUP_TOOLCHAIN=1.99.1`, and `--offline --locked` on each cargo command.
- You may add workspace members and path dependencies in your own spike directory. You may also remove candidate crates. Run `cargo update --offline --workspace` to refresh `Cargo.lock`, then commit it.
- If you need a crate, a feature, or a version that does not resolve offline, stop. Report `blocked: <crate>`. Do not run `cargo add` with the network, `cargo install`, or `rustup`.
- Set `CARGO_TARGET_DIR` to your lane directory. Do not commit `target/`.
- Postgres tests use `DATABASE_URL` from the environment. Never start a database.

## Results

Write `docs/design/rust-spike/r0-NN-<name>.md` from `docs/design/rust-spike/TEMPLATE.md`, and `r0-NN-<name>.verdict.toml` from `TEMPLATE.verdict.toml`. The check of every spike is:

```
scripts/rust/spike-check.sh r0-NN
```

It verifies the five headings, the `Verdict:` line, and the TOML fields. Then it runs `cargo fmt --check` and `cargo test --workspace --offline --locked` in the spike directory.
