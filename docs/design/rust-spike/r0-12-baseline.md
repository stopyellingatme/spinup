# R0.12 Size and start baseline

Issue: #20. Spike code: `spikes-rs/r0-12-baseline/`.

## Question

How large are release builds of the Rust CLI skeleton and of the reference API with the full kit?
How fast do they start, how much memory do they use, and how large are their images?
How do these numbers compare with Go (`bin/spinup` is 28 MB)?

## Method

The workspace has three members:

- `cli-skel` (binary `spinup`): the Go command tree as clap derive stubs. `tools/gen_commands.py` reads `spinup --help` of the Go binary and writes `src/commands.rs`. The tree has 111 leaf commands, 20 groups, and 188 flags. The brief said "about 60 commands". I used the real tree, so the size is an upper bound.
- `cli-skel` also links minijinja, toml, toml_edit, the R0.4 client (ureq 3.5, rustls with the `ring` provider), zstd, tar, sha2, ed25519-dalek, clap_complete, inquire, regex, semver, walkdir, and dirs. Real code paths use each crate: `cache list`, `new`, `self-update`, `plugin verify`, `doctor`, and `completion`. The other commands print a stub line.
- `cli-skel/build.rs` builds three embedded archives from `internal/templates/packs/`. Each archive is a deterministic tar (299 pack files plus `VERSION`, 1,515,008 bytes) with zstd level 19. The three versions differ in `VERSION` and in one file each. Each archive is about 195 KB.
- `kit` and `todo-api`: the R0.6 app with the full kit. I copied the R0.7 kit (auth, mail, OIDC, rate limit) and added the R0.8 `jobs.rs`, `telemetry.rs`, and the River job migration. `app.rs` is the R0.8 file plus the R0.7 `ConnectInfo` change. `serve` starts auth, the reset routes (with `SMTP_URL`), OIDC routes, and telemetry. `.sqlx/` comes from R0.7. `cargo sqlx prepare --check --workspace` passes against `spinup_r12`.
- `todo-api` features: `mimalloc` (the global allocator) and `auth` (on by default). `--no-default-features --features jobs,telemetry` gives `serve` without auth. I added it to explain the start time.

The release profile is `lto = true`, `codegen-units = 1`, `strip = true`, and `panic = "abort"`.

The Go baselines:

- `go-spinup`: `go build -trimpath ./cmd/spinup` in this worktree (27,998,914 bytes, the 28 MB of `bin/spinup`). `go-spinup-stripped` adds `-ldflags "-s -w"`, as the GoReleaser config does.
- `go-api-stripped`: the Go golden app with the same features. I copied `testdata/golden/api/api` and laid the `auth`, `auth/oidc`, `jobs`, `mail`, and `gen-resource-auth` golden files over it. The golden trees have no generated code. I copied `internal/gen/api.gen.go`, `internal/store/`, the handlers, the routes, and `openapi/` from the dogfood app (`/Users/king/Documents/code/todo`, read only). The kit is a `replace` to the worktree `kit/`. The build flags are the `Dockerfile.api` flags: `CGO_ENABLED=0 -trimpath -ldflags "-s -w"`.
- `go-api-base`: the golden `api` `main.go` (no auth, no mail, no jobs) in the same module.
- The Go app ran against its own database `spinup_r12_go` (goose and River migrations). I dropped it after the run.

Images (Docker Desktop 27.4, linux/arm64 VM with 10 CPUs):

- musl: `cargo zigbuild --release --locked --offline -p todo-api --target aarch64-unknown-linux-musl` on the host (the R0.14 command). `docker/Dockerfile.musl` copies the binary into `gcr.io/distroless/static-debian13:nonroot`.
- glibc: `docker/Dockerfile.glibc` builds in `rust:1.99.1-slim-trixie` with `--network=none`. The crates come from `cargo vendor --offline --locked` (519 MB) as the build context `vendor`. A dependency layer builds the manifests with stub sources (the cargo-chef method without cargo-chef). The final stage is `gcr.io/distroless/cc-debian13:nonroot`.
- Go: `docker/Dockerfile.goapi` copies the linux/arm64 Go binary into distroless static.
- Cold: no Rust target directory for the musl target, and `docker build --no-cache`. Warm: one comment line added to `todo-api/src/todos.rs`, then a rebuild with the cache. Three warm runs each.
- `docker/compose.yml` runs each Rust image with `read_only: true` and `healthcheck: ["CMD", "/api", "healthcheck"]`.
- Linux RSS: each Linux binary runs `serve` in the Rust image for 3 s. The script reads `VmRSS` and `VmHWM` from `/proc`.

`measure.py host` and `measure.py images` do all measurements. The raw results are in `measurements/host.json`, `measurements/images.json`, and `measurements/host-build.log`.
Help times are 50 runs after 5 warm-up runs. Start times are 20 runs from spawn to the first 200 from `/healthz`. The script polls every 0.5 ms.

Machine: Apple M1 Max, 10 cores, 64 GB, macOS 26.4.1. Rust 1.99.1. Go 1.27.2. PostgreSQL 18 on 127.0.0.1:55433.
No other lane ran. The script waits until the 1-minute load average is below 2 before each group. During the host measurements, the load average was 1.76 to 2.38 (other sessions on the machine keep it near 2). Each table gives the load range.

## Numbers

### Binary size (aarch64-apple-darwin unless named)

| Binary | Bytes | MB |
|---|---:|---:|
| Rust `cli-skel` (`spinup`), with 3 archives (about 585 KB) | 5,387,072 | 5.4 |
| Go `spinup`, `-trimpath` (as `bin/spinup`) | 27,998,914 | 28.0 |
| Go `spinup`, `-trimpath -s -w` (release flags) | 19,781,906 | 19.8 |
| Rust `todo-api`, full kit | 9,318,912 | 9.3 |
| Rust `todo-api`, full kit, mimalloc | 9,470,496 | 9.5 |
| Rust `todo-api`, no auth | 7,944,640 | 7.9 |
| Go API, same features, `-s -w` | 26,931,778 | 26.9 |
| Go API, same features, no `-s -w` | 38,821,170 | 38.8 |
| Go API base (no auth, mail, jobs), `-s -w` | 24,300,098 | 24.3 |
| Rust `todo-api`, linux musl static (zigbuild) | 9,764,576 | 9.8 |
| Rust `todo-api`, linux glibc (in Docker) | 9,843,256 | 9.8 |
| Go API, linux/arm64, `-s -w` | 25,952,416 | 26.0 |

The Rust CLI is 19 % of the 28 MB Go binary, and 27 % of the stripped Go release binary.
The Rust API is 35 % of the Go API with the same features.

### CLI start (load 1.97 at the start and end)

| Binary | `--help` median | `--help` p90 | `version` median | `--help` max RSS |
|---|---:|---:|---:|---:|
| Rust `spinup` | 4.74 ms | 5.28 ms | 4.04 ms | 4.3 MB |
| Go `spinup` (28 MB) | 13.28 ms | 15.06 ms | 13.18 ms | 21.8 MB |
| Go `spinup`, stripped | 13.38 ms | 14.55 ms | 14.02 ms | 21.8 MB |

Rust `cache list` (sha256 check, zstd decode, and tar read of all three archives): median 7.88 ms, p90 7.98 ms, max RSS 11.0 MB.

### API start and memory (host, load 1.76 to 2.38)

| Binary | `/healthz` median | p90 | Idle RSS | Idle footprint | Footprint after 2000 requests |
|---|---:|---:|---:|---:|---:|
| Rust, full kit | 124.75 ms | 126.91 ms | 75.3 MiB | 67.8 MiB | 68.1 MiB |
| Rust, full kit, mimalloc | 124.39 ms | 127.10 ms | 75.3 MiB | 67.8 MiB | 7.5 MiB |
| Rust, no auth | 8.97 ms | 10.16 ms | 9.8 MiB | 3.3 MiB | 3.5 MiB |
| Go, same features | 51.82 ms | 53.05 ms | 86.9 MiB | 73.4 MiB | 85.8 MiB |
| Go base (no auth) | 12.42 ms | 13.67 ms | 19.9 MiB | 7.7 MiB | 13.1 MiB |

"Footprint" is the macOS physical footprint from `vmmap -summary`. Idle means 3 s after the first answer.

The startup argon2id dummy hash (64 MiB, t=3, p=4) causes most of the full-kit start time and memory. Both kits compute it in `Auth::new`, so logins for unknown emails take the same time. The Rust `argon2` crate computes the 4 lanes on one thread (R0.7: 98 ms against 54 ms in Go). Without auth, Rust starts in 9 ms and Go in 12 ms.
On macOS, the system allocator keeps the freed 64 MiB block (one dirty `MALLOC_LARGE` region). mimalloc returns it after some traffic.

Linux (in the Rust image, 3 s after start, load below 2 at the start of each run):

| Binary | VmRSS | VmHWM (peak) |
|---|---:|---:|
| Rust musl, full kit | 7.3 MiB | 68.1 MiB |
| Rust glibc, full kit | 8.1 MiB | 69.6 MiB |
| Go static, same features | 83.7 MiB | 83.7 MiB |

On Linux, the freed hash memory goes back to the system with both musl and glibc. The Rust API then uses about 8 MiB. The Go API keeps its heap (84 MiB).

### Images (linux/arm64)

| Image | Size | Cold build | Warm build (3 runs) | Load (1 min) |
|---|---:|---:|---:|---:|
| musl, distroless static | 12.1 MB | 136.7 s zigbuild + 1.0 s `docker build` | 63.5 to 64.2 s zigbuild + 0.2 to 0.3 s `docker build` | 1.46 to 3.19 |
| glibc, distroless cc (built in Docker) | 48.1 MB | 111.6 s | 57.3 to 58.0 s | 1.46 to 7.0 |
| Go, distroless static (binary copied) | 28.3 MB | 0.5 s `docker build` (Go build not in this time) | not measured | 7.0 |

The glibc cold time includes the first transfer of the 519 MB vendor context. The load average reached 4.0 after the cold glibc build and 7.0 after the third warm build. These are the loads of this build itself, on the 10-CPU VM.
The warm builds recompile `spinup-kit` and `todo-api` and run fat LTO. The fat LTO link is most of the warm time (the host rebuild after a `main.rs` edit took 51.6 to 62.3 s).
The Go image build time was not measured with a Go build in Docker: `go mod download` needs the network. The image only copies the host binary.

### Read-only compose check

| Service | `docker compose up --wait` | Health | `ReadonlyRootfs` | `/healthz` from the host | Container memory |
|---|---:|---|---|---|---:|
| `api-musl` | 1.8 s | healthy | true | 200 | 2.5 MiB |
| `api-glibc` | 1.8 s | healthy | true | 200 | 2.2 MiB |

`/api healthcheck` works as the exec-form health command in both distroless images, with a read-only root file system. The container memory is the cgroup value of `docker stats` at idle.

### Host release builds (load 2.75 to 8.48, caused by these builds)

| Build | Wall time |
|---|---:|
| `cli-skel`, first release build (shared dependencies cold) | 62.8 s |
| `todo-api`, first release build | 129.9 s |
| `todo-api` with mimalloc, after the full build | 64.2 s |
| `todo-api` after a `main.rs` edit | 62.3 s |

## Verdict

Verdict: go

The Rust CLI skeleton is 5.4 MB against 28 MB for Go, and `--help` takes 4.7 ms against 13.3 ms. The full-kit API is 9.3 MB against 26.9 MB. It uses about 8 MiB on Linux against 84 MiB. The static musl image is 12.1 MB, runs with `read_only: true`, and its `healthcheck` command works. One number is worse than Go: the full-kit start time to the first `/healthz` is 125 ms against 52 ms. The startup argon2 dummy hash on one thread causes it. Without auth, Rust starts faster than Go (9 ms against 12 ms). This cost is small and has a known fix, so it does not change the plan.

## Recommendation

- Keep the release profile (`lto = true`, `codegen-units = 1`, `strip = true`, `panic = "abort"`) for release and image builds. Fat LTO costs about 60 s for each warm image build. Use `lto = "thin"` or a separate dev image profile if R2 needs faster deploy iterations. This spike did not measure thin LTO.
- Use the static musl image on `gcr.io/distroless/static-debian13:nonroot` for `Dockerfile.api` (R2.18). It is 12.1 MB, against 48.1 MB for glibc on distroless cc. Keep `HEALTHCHECK CMD ["/api", "healthcheck"]` and `read_only: true` in production Compose.
- Musl in Docker: the `rust:1.99.1-slim-trixie` image has only `aarch64-unknown-linux-gnu`. A Dockerfile musl build needs `rustup target add <arch>-unknown-linux-musl` (network), or an image that already has the target. Offline, the musl build runs on the host with `cargo zigbuild`, as this spike does. The main loop can measure the in-Docker musl build with network access: add `RUN rustup target add aarch64-unknown-linux-musl` (and `apt-get install -y musl-tools`) to the build stage of `docker/Dockerfile.glibc`, build with `--target aarch64-unknown-linux-musl`, and copy the binary into distroless static.
- Kit (R2): compute the startup dummy hash off the start path. Use `spawn_blocking` after the listener is up, or a lazy `OnceCell`. Or compute the 4 lanes in parallel. Then the full-kit start is near the 9 ms of the no-auth app. Note that the Go kit pays 54 ms for the same hash.
- Do not make mimalloc the default allocator for generated apps. On Linux, the system allocators (musl and glibc) already return the hash memory, and the binary is 150 KB larger. The macOS RSS difference affects only local development.
- The CLI can embed several template archives. Three tar+zstd archives of the full packs add about 585 KB.
- Not done: x86_64 images (the Docker VM is arm64), thin LTO, `opt-level = "s"`, a Go API build inside Docker, and the in-Docker musl build (no network). The CLI has 111 leaf commands, not about 60, because it copies the Go tree.
- The database `spinup_r12` on 127.0.0.1:55433 holds the app tables for `cargo sqlx prepare --check --workspace`. The Docker images `r0-12/api-musl`, `r0-12/api-glibc`, and `r0-12/api-go` stay in the local Docker store (88 MB). Remove them with `docker rmi r0-12/api-musl r0-12/api-glibc r0-12/api-go`.
- To run the measurements again, build the binaries as in `measurements/host-build.log`, then run `measure.py host` and `measure.py images` (see the doc comment of `measure.py`).
