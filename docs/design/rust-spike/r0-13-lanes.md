# R0.13 Lane build cost and disk

Issue: #21 (label `hard-no-go`). Spike code: `spikes-rs/r0-13-lanes/`.

## Question

Can the Mac carry the heaviest planned load: two Rust lanes and the R2/R3 gate at the same time?
Hard no-go (b): peak disk of all targets, `CARGO_HOME`, `~/.cargo/registry`, and caches above 40 GB, or a 1-minute load average above 14, or swap above 2 GB.
Compare plain per-lane target dirs, `sccache`, and APFS clone seeds. Test whether the sccache key survives worktrees at different paths. Count test binaries. Extrapolate to 50k lines. Say whether crates must merge.

## Method

The spike workspace has the R0.12 members `cli-skel`, `kit`, and `todo-api` (copies, for `spike-check`). Three Python scripts do the work. They write everything under `$CARGO_TARGET_DIR/lanes`, outside the repository.

- `tools/gen_lanes.py cli` writes the scaled CLI workspace: the 44 library crates of the crate table in `rust-plan.md` §1 with the same workspace edges, the bin crate `spinup` (the R0.12 `cli-skel`: 111 clap stub commands and three embedded template archives), `spinup-kit` (the R0.12 full kit), and the small crates `spinup-plugin-sdk` and `spinup-kit-testkit`. That is 48 crates. Each library crate has generated modules. Each module has a serde record, an error enum, a generic function, one function that uses a `cli-skel` dependency (toml, toml_edit, regex, minijinja, sha2, semver, walkdir, zstd, tar, ed25519-dalek, or ureq with rustls), one function that uses a type of a workspace dependency, three unit tests, and two tests in `tests/it/`. Each crate has exactly one integration test binary, `tests/it/main.rs`. The script runs `cargo update --offline --workspace` and `cargo fmt --all`. The code passes `cargo clippy --all-targets -- -D warnings`, and all 1162 tests pass.
- Two sizes: `--lines 22000` gives 27,345 CLI lines (the "27k" runs), and `--lines 47000` gives 52,890 CLI lines (the "53k" runs). CLI lines are all `.rs` lines outside `spinup-kit`. The kit adds about 8,000 lines.
- `tools/gen_lanes.py gate` writes the gate tree from the R0.11 app (the full kit and 20 generated resources): one shared kit path, and 8 apps, each its own workspace: `api` (telemetry only), `auth`, `jobs`, `mail` (auth, jobs, telemetry), three `gen-resource` trees with 2, 5, and 10 resources, and `corpus` with 20 resources. The apps ship the R0.11 dev profile (`debug = 0`, `strip = "symbols"`, dependencies at `opt-level = 1`). The R0.11 `.sqlx/` covers every query, so `SQLX_OFFLINE=true` builds need no database. A ninth tree, `ref/todo-rs` (2 resources), is the `rs-ref-check` target.
- `tools/measure.py run` starts two lanes and the gate at the same time, each under `nice -n 10`:
  - Lane A checks `spinup-kit` and `spinup-gen-app`. Lane B checks `spinup-deploy` and `spinup-cli` (which depends on every CLI crate). Each lane has its own worktree copy and its own `CARGO_TARGET_DIR`. A lane runs the lane check: `cargo fmt --check`, `cargo clippy --locked --offline -p .. --all-targets -- -D warnings`, `cargo nextest run --locked --offline -p ..`, and `cargo test --doc` (the parts of `make rs-lint` and `make rs-test`; no `rs-build`). Then it makes warm cycles until the gate ends: it changes a constant in one of its crates, and it runs the same four commands. A cycle starts at most every 30 s. The first `plain` run had no pacing (40 cycles back to back).
  - The gate runs under one shared `CARGO_TARGET_DIR`: `cargo build --locked --offline --all-targets` for each of the 8 apps, `cargo zigbuild --release --target aarch64-unknown-linux-musl` of `api` (fat LTO), `docker build` of the musl binary on `gcr.io/distroless/static-debian13:nonroot`, and `cargo check --locked --offline` of `ref/todo-rs` (the `rs-ref-check` workload). Then it changes a constant in the kit and runs all steps again (the warm pass).
  - A sampler thread records every 5 s: `du -sk` of both lane dirs (worktree and target), the gate dir, `CARGO_HOME`, the sccache dir (sccache runs), and the seed dirs (clone runs). It also records the 1-minute load average (`os.getloadavg`), swap use (`sysctl vm.swapusage`), and the free space of the volume.
- Profiles:
  - `plain`: new, empty target dirs for each lane and the gate.
  - `sccache`: `RUSTC_WRAPPER=sccache` 0.19.0 with its own `SCCACHE_DIR` (100 GB limit). `-cold` starts with an empty cache. `-warm` runs again with the cache of `-cold`, in new worktrees and new target dirs. `bd` adds `SCCACHE_BASEDIRS` (the common parent). `slot` keeps each target dir at one fixed path that both runs use (deleted before each run).
  - `clone`: `tools/measure.py seed` first builds one warm target dir of the whole workspace (`clippy --workspace --all-targets`, `nextest run --workspace`, `test --doc`). Each lane target is `cp -c -R` of that seed. The gate target is `cp -c -R` of the gate target of the first `plain` run. The worktrees are new copies at new paths.
  - `throttled`: lanes at `CARGO_BUILD_JOBS=2`, the gate at `CARGO_BUILD_JOBS=3` or `4`, and `NEXTEST_TEST_THREADS=2` or `4`. All other runs use the lane environment: `CARGO_BUILD_JOBS=4` and the nextest default (10 threads).
- `tools/measure.py pathkey` builds `spinup-render` and `spinup-upgrade` (95 Rust and 49 C compiles) with sccache and `CARGO_INCREMENTAL=0` in new target dirs. Each run changes the worktree path, the target path, both, or neither.
- `tools/measure.py single` runs lane B alone (cold and 2 warm cycles), then the whole-workspace clippy and `nextest --no-run`, at 27k and 53k lines. `tools/measure.py appprofile` builds the `auth` app cold with the shipped dev profile and with the default dev profile.
- `tools/summary.py` prints the tables below from `measurements/*.json`. The 5 s samples are in `measurements/samples/`.

Units: `du -sk` counts KiB. Every GB in this file is 10^9 bytes (KiB × 1024 / 10^9). The disk peak adds `~/.cargo/registry` (1.64 GB). No build here writes it, so the sampler does not read it again. Swap is in GiB, because `sysctl` prints MiB.

Machine: Apple M1 Max, 10 cores, 64 GB, macOS (Darwin 25.4.0), APFS. Rust 1.99.1, cargo-nextest 0.9.146, sccache 0.19.0, cargo-zigbuild 0.24.0, zig 0.17.0, Docker Desktop 27.4. No other lane ran. Other sessions kept the 1-minute load near 2.5 to 4 when this spike did not run. Each run waits until the 1-minute load is below 4. Each table gives `uptime` at the start and the end of each run. The swap baseline was 9178.81 MiB (8.96 GiB) at the start of every run, from other sessions.

## Numbers

### Peaks of the concurrent runs (two lanes and the gate)

"Jobs" is `CARGO_BUILD_JOBS` of a lane, of the gate, and the nextest test threads. "Peak du" is the largest sum of one sample, plus `~/.cargo/registry`. In clone runs, `du` counts the seed dirs and each clone in full, although the clones share blocks with their seed. "Free-space drop" is the largest drop of free space on the volume during the run. It is the physical cost, but other sessions also write to the volume.

| Run | Lines | Jobs (lane/gate/tests) | Wall | Peak du | load1 max | Swap increase | Free-space drop |
|---|---:|---|---:|---:|---:|---:|---:|
| plain | 27k | 4/4/10 | 456 s | 11.13 GB | **33.81** | 0.00 GiB | 8.27 GB |
| plain-paced | 27k | 4/4/10 | 430 s | 11.13 GB | **34.90** | 0.00 GiB | 8.26 GB |
| sccache-cold | 27k | 4/4/10 | 469 s | 12.28 GB | **37.45** | 0.00 GiB | 9.33 GB |
| sccache-warm | 27k | 4/4/10 | 420 s | 13.38 GB | **48.12** | 0.00 GiB | 9.57 GB |
| sccache-bd-cold | 27k | 4/4/10 | 448 s | 12.28 GB | **54.93** | 0.00 GiB | 9.43 GB |
| sccache-bd-warm | 27k | 4/4/10 | 402 s | 13.38 GB | **46.19** | 0.00 GiB | 9.38 GB |
| sccache-slot-cold | 27k | 4/4/10 | 448 s | 12.28 GB | **38.50** | 0.00 GiB | 9.44 GB |
| sccache-slot-warm | 27k | 4/4/10 | 261 s | 11.81 GB | **18.20** | 0.00 GiB | 7.80 GB |
| clone | 27k | 4/4/10 | 218 s | 30.32 GB | 10.27 | 0.00 GiB | 4.57 GB |
| clone2 | 27k | 4/4/10 | 217 s | 30.32 GB | 10.20 | 0.00 GiB | 4.58 GB |
| clone-53k | 53k | 4/4/10 | 222 s | 33.62 GB | **15.37** | 0.00 GiB | 5.06 GB |
| clone-53k-r2 | 53k | 4/4/10 | 220 s | 33.62 GB | 9.58 | 0.00 GiB | 5.09 GB |
| plain-throttled | 27k | 2/3/2 | 486 s | 11.12 GB | 8.09 | 0.00 GiB | 8.26 GB |
| plain-throttled-53k | 53k | 2/4/4 | 421 s | 11.61 GB | 10.52 | 0.00 GiB | 8.75 GB |
| clone-53k-throttled | 53k | 2/4/4 | 219 s | 33.61 GB | 7.06 | 0.00 GiB | 5.07 GB |
| clone-53k-throttled-r2 | 53k | 2/4/4 | 220 s | 33.62 GB | 8.37 | 0.00 GiB | 5.07 GB |

Bold values are above the limit of 14. No run came near the 40 GB disk limit. Swap did not grow in any run: each sample was 9178.81 MiB or 9170.81 MiB. The swap numbers are the increase over the baseline of each run. The absolute swap use was 8.96 GiB (9.62 GB) before and during every run, which is above 2 GB because of other sessions.

The load peak comes in the first 20 to 60 s, when the three cold starts compile the same dependencies at the same time. In `plain-paced`, the load was 18.95 at 20 s, 33.15 at 40 s, 23.77 at 120 s, and below 10 after 240 s. Load from the rustc processes and from the macOS executable scan (`XprotectService` and `syspolicyd` used 20 to 50 % of one CPU in a `ps` sample) adds up. The load average counts runnable threads, so it can go above the 12 compile jobs.

### Times of the concurrent runs

"Cold" is the first lane check (fmt, clippy, nextest, doc tests) or the first gate pass. "Warm" is one cycle after a one-line edit, or the gate pass after the kit edit.

| Run | Lane A cold | Lane A warm median (n) | Lane B cold | Lane B warm median (n) | Gate cold | Gate warm | uptime at start | uptime at end |
|---|---:|---:|---:|---:|---:|---:|---|---|
| plain | 153 s | 5.5 s (40) | 47 s | 5.1 s (40) | 377 s | 79 s | 2.72 3.72 3.80 | 3.14 8.95 7.79 |
| plain-paced | 136 s | 5.2 s (10) | 48 s | 4.2 s (13) | 348 s | 81 s | 3.96 7.55 11.95 | 4.07 9.10 12.05 |
| sccache-cold | 186 s | 5.2 s (10) | 62 s | 4.4 s (14) | 385 s | 83 s | 3.04 8.14 7.56 | 9.10 13.15 11.81 |
| sccache-warm | 183 s | 5.8 s (8) | 61 s | 4.7 s (12) | 338 s | 81 s | 3.55 8.81 10.25 | 6.84 15.90 15.14 |
| sccache-bd-cold | 184 s | 4.8 s (9) | 60 s | 4.2 s (13) | 365 s | 82 s | 3.74 8.35 11.77 | 5.97 14.06 15.35 |
| sccache-bd-warm | 166 s | 5.1 s (8) | 55 s | 4.5 s (12) | 318 s | 82 s | 3.75 12.06 14.53 | 4.65 14.40 16.43 |
| sccache-slot-cold | 185 s | 5.0 s (9) | 61 s | 4.4 s (13) | 366 s | 81 s | 2.49 4.39 5.62 | 4.29 10.19 9.61 |
| sccache-slot-warm | 71 s | 4.9 s (7) | 30 s | 4.2 s (8) | 178 s | 82 s | 3.27 9.23 9.29 | 5.51 9.43 9.68 |
| clone | 42 s | 5.1 s (6) | 28 s | 3.3 s (7) | 136 s | 81 s | 3.95 9.42 13.82 | 5.17 8.12 12.30 |
| clone2 | 42 s | 5.2 s (6) | 28 s | 3.5 s (7) | 136 s | 80 s | 3.76 6.94 10.28 | 4.60 6.37 9.35 |
| clone-53k | 46 s | 5.5 s (6) | 34 s | 4.4 s (7) | 140 s | 81 s | 3.60 4.61 6.65 | 3.80 5.56 6.75 |
| clone-53k-r2 | 44 s | 5.4 s (6) | 33 s | 4.3 s (7) | 137 s | 82 s | 3.61 8.38 9.28 | 4.82 7.28 8.65 |
| plain-throttled | 198 s | 5.3 s (10) | 58 s | 3.4 s (15) | 403 s | 81 s | 3.97 5.13 6.27 | 2.63 4.51 5.68 |
| plain-throttled-53k | 213 s | 5.6 s (7) | 72 s | 5.2 s (12) | 338 s | 82 s | 3.84 6.16 7.37 | 3.46 6.30 7.25 |
| clone-53k-throttled | 61 s | 5.6 s (6) | 51 s | 4.8 s (6) | 135 s | 83 s | 3.94 6.65 8.34 | 4.18 5.76 7.60 |
| clone-53k-throttled-r2 | 62 s | 5.7 s (6) | 52 s | 5.2 s (6) | 135 s | 82 s | 3.49 5.31 6.63 | 6.56 6.32 6.80 |

Gate cold pass, per step (seconds):

| Run | api | auth | jobs | mail | res2 | res5 | res10 | corpus | musl | docker | ref-check |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| plain | 164 | 3 | 26 | 4 | 6 | 5 | 7 | 11 | 133 | 0.4 | 16 |
| sccache-slot-warm | 52 | 3 | 11 | 4 | 4 | 4 | 5 | 8 | 79 | 0.3 | 7 |
| clone | 23 | 4 | 13 | 3 | 4 | 4 | 6 | 9 | 66 | 1 | 3 |
| plain-throttled-53k | 136 | 3 | 26 | 4 | 4 | 5 | 6 | 10 | 128 | 0.3 | 16 |
| clone-53k-throttled | 20 | 3 | 13 | 4 | 4 | 5 | 8 | 9 | 66 | 0.3 | 3 |

The first app pays the dependency build. The other 7 apps share it under the one gate target and take 3 to 26 s each. The warm gate pass (kit edit) takes 79 to 83 s in every profile: the 8 app builds take 2 to 3 s each, and the musl fat-LTO release build takes about 60 s. The image is 10,516,882 bytes. The Docker step only copies the host binary, so it takes under 1 s. No lane or gate step exited with an error in any run.

### Disk per directory at the end of a run (GB)

| Profile | Lane A target | Lane B target | Gate target | Other |
|---|---:|---:|---:|---|
| plain, 27k | 2.87 | 1.01 | 4.74 | |
| plain-throttled-53k | 3.00 | 1.36 | 4.74 | |
| sccache, 27k | 2.87 | 1.01 | 4.74 | sccache dir 1.15 after one run, 2.25 after two runs |
| sccache-slot-warm, 27k | 2.50 | 0.90 | 4.73 | sccache dir 1.17 |
| clone, 27k | 5.90 | 5.47 | 7.43 | seed 4.28, gate seed 4.74 |
| clone, 53k | 7.00 | 6.81 | 7.43 | seed 5.14, gate seed 4.74 |

`CARGO_HOME` was 0.86 GB, and `~/.cargo/registry` was 1.64 GB. A clone took 7 to 10 s per target dir. A clone lane target is larger than a plain lane target, because the seed holds the whole workspace. The free-space drop of a clone run (4.6 to 5.1 GB) is less than that of a plain run (8.3 to 8.8 GB): the clones share blocks with the seed until cargo rewrites them.

### sccache key across paths (`pathkey`, load 3.55 to 6.98)

| Run | Worktree | Target dir | Rust hits / misses | C hits / misses | Time |
|---|---|---|---:|---:|---:|
| first build | x | t1 | 0 / 95 | 0 / 49 | 14.7 s |
| same paths again | x | t1 | 95 / 0 | 49 / 0 | 4.9 s |
| other target path | x | t2 | 0 / 95 | 49 / 0 | 12.1 s |
| other worktree path | y | t1 | 79 / 16 | 49 / 0 | 5.5 s |
| both other | y | t3 | 0 / 95 | 49 / 0 | 12.1 s |
| `SCCACHE_BASEDIRS`, first | z | t4 | 0 / 95 | 49 / 0 | 12.2 s |
| `SCCACHE_BASEDIRS`, other paths | w | t5 | 0 / 95 | 49 / 0 | 12.3 s |
| `SCCACHE_BASEDIRS`, same paths | z | t4 | 95 / 0 | 49 / 0 | 4.5 s |
| default `CARGO_INCREMENTAL` | x | t1 | 0 / 79 (16 incremental, not cacheable) | 49 / 0 | 12.2 s |

The Rust key holds the absolute target path. A new target path misses every Rust crate. A new worktree path with the same target path misses only the 16 workspace crates. `SCCACHE_BASEDIRS` does not change the Rust key in sccache 0.19.0 (it works for C). `CARGO_INCREMENTAL` is part of the key, and sccache 0.19.0 refuses `CARGO_INCREMENTAL=1`. With the cargo default, the workspace crates are incremental and sccache does not cache them. C code (zstd, ring) hits from any path.

This explains the concurrent runs: `sccache-warm` and `sccache-bd-warm` had 0 Rust hits of 1881 (new target paths). `sccache-slot-warm` had 1877 hits of 1881 (fixed target paths). It cut the gate cold pass from 366 s to 178 s, but its load peak was still 18.20.

### Test binaries

| Scope | Test binaries |
|---|---:|
| Whole CLI workspace, 48 crates (`nextest list --workspace`) | 95 |
| Lane A (`spinup-kit`, `spinup-gen-app`) | 3 |
| Lane B (`spinup-deploy`, `spinup-cli`) | 4 |
| Gate, per pass (8 apps × lib, bin, `api`, `jobs` tests) | 32 |

### Scale: 27k against 53k lines (one job at a time)

| Measurement | 27k lines | 53k lines | Load at start / end |
|---|---:|---:|---|
| Lane B cold (fmt, clippy, nextest, doc) | 28.5 s | 32.4 s | 3.71 / 6.06 and 3.61 / 5.18 |
| Lane B warm cycles | 2.7, 3.7 s | 3.0, 4.5 s | |
| Workspace clippy (after lane B) | 47.4 s | 49.4 s | |
| Workspace `nextest --no-run` (after lane B) | 89.2 s | 101.7 s | |
| Target dir after all of the above | 5.12 GB | 6.33 GB | |
| Seed: workspace clippy, nextest run, doc tests | 52.2, 93.8, 3.7 s | 54.5, 106.4, 4.5 s | 3.63 / 8.94 and 3.39 / 4.49 |
| Seed target dir | 4.28 GB | 5.14 GB | |
| Clone run, lane target dirs | 5.90, 5.47 GB | 7.00, 6.81 GB | |
| Clone run, peak du (logical) | 30.32 GB | 33.62 GB | |
| Plain throttled run, peak du | 11.12 GB | 11.61 GB | |

The CLI code adds about 1.2 GB to a full workspace target dir and about 1.1 GB to each clone lane target per 25k lines. The dependencies are most of the cold time: 25k more lines add about 4 s to the lane B cold check and 12 s to the workspace test build.

### App dev profile (the `auth` app alone, cold, `--all-targets`)

| Profile | Time | Target dir | Load at start / end |
|---|---:|---:|---|
| Shipped (R0.11: `debug = 0`, `strip`, deps at `opt-level = 1`) | 124.8 s | 1.02 GB | 3.66 / 6.56 |
| Default dev profile | 54.7 s | 1.99 GB | 3.53 / 4.39 |

## Verdict

Verdict: soft

The two lanes and the gate fit the limits only with changed lane settings. With the planned settings (`CARGO_BUILD_JOBS=4` in each lane and in the gate, and the nextest default of 10 test threads), the 1-minute load went to 33.8 to 54.9 in every profile without clone seeds, which is 2.4 to 3.9 times the limit of 14. sccache did not help: it gave 0 Rust cache hits across new target paths, and with fixed target paths the load was still 18.2. Clone seeds kept the load at 9.6 to 10.3 in three of four runs, but one 53k run reached 15.4. Lanes at `CARGO_BUILD_JOBS=2` and 2 or 4 test threads, with the gate at 3 or 4 jobs, passed in all four runs: load 7.1 to 10.5 at 27k and 53k lines, with or without clone seeds. Disk stayed below the limit in every run: at most 33.6 GB logical with clones (5.1 GB physical) and 11.6 GB without. Swap did not grow in any run. The absolute swap use (8.96 GiB) comes from other sessions and was above 2 GB before the spike started. The plan can go on, but its lane defaults change, so the verdict is soft.

## Recommendation

- Lane environment (`rust-plan.md` §3): set `CARGO_BUILD_JOBS=2` and `NEXTEST_TEST_THREADS=4` in each lane. Set `CARGO_BUILD_JOBS=4` in the gate. Keep `LANE_NICE=10` and at most 2 lanes. The cost is 30 to 45 % on the cold lane A check without seeds (198 to 213 s against 136 to 153 s). The cold gate pass did not get slower (338 to 403 s against 348 to 377 s). Warm cycles do not change (3 to 6 s).
- `RUSTC_WRAPPER`: leave it unset. Remove "shared sccache" from the fixed defaults. The Rust key of sccache 0.19.0 holds the target path, so per-lane target dirs that the main loop deletes after a merge never hit. `SCCACHE_BASEDIRS` does not fix this for Rust. If a later milestone wants sccache, it must reuse fixed target paths per lane slot (1877 of 1881 hits in this spike), and it must still use the job limits above.
- `CARGO_INCREMENTAL`: leave it unset (the cargo default). Warm lane cycles take 3 to 6 s with it.
- Clone seeds: after each milestone pre-seed, the main loop builds one seed target of the whole workspace (`cargo clippy --workspace --all-targets`, `cargo nextest run --workspace`, 150 to 165 s). Each new lane runs `cp -c -R <seed> <lane target>` (7 to 10 s). This cuts the cold lane check from 136 to 213 s to 42 to 62 s, and the cold gate pass from 338 to 403 s to 135 to 140 s. Also clone the last gate target for the next gate. Count the disk with `df`, not `du`: `du` counts each clone in full.
- Disk: no limit problem. Delete each lane target after its merge, as planned. At the measured growth (about 3.3 GB logical per 25k lines in the clone profile), the logical `du` sum reaches 40 GB at about 100k CLI lines. The physical use stays near 5 GB.
- Crates: they do not need to merge. The 48-crate workspace has 95 test binaries, and a lane builds 3 or 4 of them. Keep one `tests/it/main.rs` per crate.
- Generated apps: keep the R0.11 dev profile. It doubles the cold app build (124.8 s against 54.7 s) and halves the target dir (1.02 GB against 1.99 GB). In the gate, only the first app pays this. The musl fat-LTO build is the largest warm gate step (about 60 s of the 80 s).
- The R2 gate re-measure (`rust-plan.md` §5, R2 gate, item 5) must run `tools/measure.py run` with the same job limits and say which settings changed.
- Not done: Docker builds that compile inside the image (offline, see R0.12), a run of `make rs-build` and `cargo deny` in the gate (the brief did not ask for them), the Docker VM disk (not in `du`), and memory pressure (only swap was sampled). The CLI crates are generated code. Real code has more generics and macros, so the per-line cost can be higher. Only one or two runs of each setting ran, and the load varies with other sessions (clone-53k: 15.4 and 9.6).
- Clean-up: all target dirs, seeds, and the sccache dirs of this spike are deleted. The sccache server is stopped. No Docker image of this spike stays.
- To run again: source the lane environment, then run `python3 spikes-rs/r0-13-lanes/tools/measure.py run <label> --profile plain|sccache|clone [--lines 47000] [--lane-jobs 2 --gate-jobs 4 --test-threads 4]` (a clone run needs `measure.py seed` first, and a gate seed from a `plain` run with `--keep-gate`). `tools/summary.py` prints the tables.
