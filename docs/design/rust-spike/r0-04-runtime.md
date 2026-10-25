# R0.4 Sync or async

Issue: #12. Spike code: `spikes-rs/r0-04-runtime/`.

## Question

Should `spinup-exec` use threads with `ureq`, or tokio with `reqwest`?
Compare cancellation, streaming redaction, lock refresh, TLS, build cost, binary size, and glue code.

## Method

I read the plan context, fixed decisions, R0 unit graph, and stored body for issue #12.
The offline rule prevented access to the live issue.
I used `internal/exec/exec.go`, `process_unix.go`, and `process_windows.go` as the behavior reference.

Both binaries accept `LOCK SECRET PROGRAM [ARGS...]` or `--get HTTPS_URL`.
Each runner captures output and sends redacted bytes to separate stdout and stderr sinks.
Each Unix child starts in a new process group.
Cancellation kills that group with `SIGKILL`.
The runner continues to watch cancellation after the direct child exits, while descendants retain pipes.

The sync runner uses scoped threads and nonblocking Unix pipes.
It checks cancellation and pipes every 5 ms.
The async runner uses tokio pipes and `select!`.
Both variants bound pipe draining after child exit to 2 s on Unix.
Both variants rewrite the lock every 20 ms and stop refresh after success, cancellation, or a spawn error.

The shared redactor ports the Go replacement stages.
It sorts secrets by byte length, with the longest first.
It ignores empty secrets and replaces complete matches with `***`.
It retains partial suffixes across chunks and flushes unmatched bytes at EOF.
Tests cover all split positions and byte chunks, including Unicode, nested secrets, repeated prefixes, and replacement tokens.

The shared runner contract starts a child that starts a grandchild.
The grandchild writes a heartbeat until cancellation kills the group.
Tests also cover Ctrl-C, inherited pipes after parent exit, streamed bytes before exit, both output streams, and exit codes.
TLS tests build the clients without a connection.
Live HTTPS GET: not measured, because this lane prohibits network access.

The machine ran Darwin 25.4.0 on arm64, with Rust 1.99.1 and four Cargo jobs.
Other sessions used the machine during all measurements.
Hardware model and memory: not measured, because the sandbox denied `sysctl`.
The values below are single observations under load.
They are not idle-machine benchmarks.

Run the measurement tool from the repository root:

```sh
python3 spikes-rs/r0-04-runtime/measure.py release cross cross-zig lines
```

Each release build uses a separate, initially empty target directory.
The source cache stays warm, and `RUSTC_WRAPPER` is unset.
The release profile uses thin LTO, one codegen unit, symbol stripping, and abort on panic.
The warm build appends one newline to `main.rs`, rebuilds, and restores the original bytes.
This rebuild includes compilation and linking.
The tool deletes its temporary release targets after measurement.


Cargo commands run inside the spike workspace.
The tool runs these commands separately for each package:

```sh
cargo build --release -p sync-cli --bin sync-cli --offline --locked
cargo build --release -p async-cli --bin async-cli --offline --locked
cargo check -p sync-cli --target TARGET --offline --locked
cargo check -p async-cli --target TARGET --offline --locked
```

`measurements.json` stores the raw values, command results, and `uptime` output.
The target table below lists each checked target.
Glue counts include all runtime Rust sources in `src/`.
They exclude tests, manifests, and the measurement tool.
Both totals include the full shared source.

## Numbers

| Variant | Build | Wall time | Load average, 1 minute, before → after |
|---|---|---:|---:|
| Sync | Clean release | 15.581 s | 13.23 → 11.96 |
| Sync | Warm release rebuild | 4.288 s | 11.96 → 11.65 |
| Async | Clean release | 39.939 s | 20.83 → 16.10 |
| Async | Warm release rebuild | 5.837 s | 16.10 → 16.57 |

| Variant | Stripped binary | Physical source, variant only | Physical source, shared | Physical source, total |
|---|---:|---:|---:|---:|
| Sync | 1,921,600 bytes | 246 lines | 160 lines | 406 lines |
| Async | 3,838,320 bytes | 182 lines | 160 lines | 342 lines |

Both binaries are arm64 Mach-O executables.
`nm -U` finds only one defined symbol in each stripped binary.
Async saves 64 physical lines in this prototype.

The async size includes its default TLS provider and platform verifier.
The sync size includes `ring` and bundled WebPKI roots.
These measurements compare complete candidate bundles.
They do not isolate the cost of an event loop.

The host tests pass all 16 tests: 7 tests per variant and 2 shared redactor tests.
A deliberate inversion of the redactor match condition fails both shared tests.
I restored the original condition before final checks.
The initial stub tests also failed before implementation.

| Target | Sync, default tools | Async, default tools | Cause when blocked |
|---|---|---|---|
| `aarch64-apple-darwin` | Pass | Pass | None |
| `x86_64-apple-darwin` | Pass | Pass | None |
| `x86_64-unknown-linux-musl` | Blocked | Blocked | Missing `x86_64-linux-musl-gcc` |
| `aarch64-unknown-linux-musl` | Blocked | Blocked | Missing `aarch64-linux-musl-gcc` |
| `x86_64-pc-windows-gnu` | Blocked | Blocked | Missing `x86_64-w64-mingw32-gcc` |
| `aarch64-pc-windows-gnullvm` | Blocked | Blocked | Missing Rust `core` and target C compiler |

Default-tool checks pass 4 of 12 package checks.
Windows arm64 also reports a missing `aarch64-w64-mingw32-clang` compiler during the sync check.
No target libraries or compilers were installed.

The installed Zig version is 0.17.0.
The retry uses `zig-cc.py` to translate Rust target flags into Zig target flags.
The tool sets a target-specific `CC`, `AR="zig ar"`, and cache paths inside this lane's target directory.
It does not change the Cargo dependency graph.

| Target | Sync, Zig C tools | Async, Zig C tools |
|---|---|---|
| `x86_64-unknown-linux-musl` | Pass | Pass |
| `aarch64-unknown-linux-musl` | Pass | Pass |
| `x86_64-pc-windows-gnu` | Pass | Pass |

All 6 package checks pass with Zig for these 3 targets.
With macOS results included, 10 of 12 package checks pass across 5 targets.
The Windows arm64 target remains blocked by missing Rust libraries.
TLS compilation passes on those 5 targets for both `ring` and the reqwest provider.
No TLS handshake was attempted on a cross target.

The checks compile code but do not link executable files or perform TLS handshakes.
Linux and Windows execution: not measured, because the lane has no suitable native runner.
The Windows behavior below comes from source inspection.

## Verdict

Verdict: soft

Use a sync CLI core for `spinup-exec`.
The sync bundle is smaller and built faster in this observation.
Async needs fewer glue lines, but this core does not require an async runtime.
Windows arm64 validation remains incomplete.
The sync Windows runner also needs a console handler and bounded pipe draining.
This result does not establish a release blocker in either runtime model.

## Recommendation

Use threads, `ureq` 3.5.0, and rustls with an explicit `ring` provider for the CLI.
Keep `Runner` synchronous and independent from a global event loop.
Use this trait shape:

```rust
pub trait Runner {
    fn run(
        &self,
        cmd: &Cmd,
        cancel: &Cancellation,
        stdout: &mut (dyn std::io::Write + Send),
        stderr: &mut (dyn std::io::Write + Send),
    ) -> std::io::Result<Output>;
}
```

Install the signal handler once at the CLI boundary.
Pass cancellation into the runner and stop the refresh task before returning.
Preserve the Go rule for noninteractive Unix commands: kill the complete process group.
Port interactive commands separately, because they must retain the terminal process group.
The spike does not port the full Go command model or its error text.

Reqwest 0.13.5 enables `aws-lc-rs` through its `rustls` feature.
The resolved versions are `aws-lc-rs` 1.18.2 and `aws-lc-sys` 0.46.0.
The native build reports `Building with: CC`.
It requires a C compiler and assembly support.
It does not invoke CMake for this native build.

Its build script selects CMake for FIPS, disabled assembly, sanitizers, and some fallback paths.
CMake remains a build dependency even when the selected builder uses `cc`.
The selected `ring` provider also requires a suitable C toolchain.
Do not describe either candidate as a pure Rust build.

The cached sources establish these provider rules:

- `reqwest-0.13.5/Cargo.toml`, features `rustls` and `__rustls-aws-lc-rs`.
- `reqwest-0.13.5/src/tls.rs`, provider selection instructions.
- `aws-lc-sys-0.46.0/builder/main.rs`, builder selection.
- `aws-lc-sys-0.46.0/builder/cc_builder.rs`, compiler requirements.
- `ureq-3.5.0/src/tls/mod.rs`, explicit provider configuration.

On Windows, neither spike implements Job Objects.
Cancellation kills only the direct child, as the Go runner does.
Descendants can survive and retain inherited pipes.
The sync spike has no Windows console handler.
Its blocking Windows reader threads cannot guarantee bounded draining when descendants retain pipes.
Tokio handles Ctrl-C through the Windows console mechanism and can cancel pending async reads.

Unix process-group signals have no direct Windows equivalent.
A Windows process group alone does not provide descendant termination.
A production guarantee for descendant termination needs Job Objects, suspended creation, and assignment before the child resumes.
Test that implementation on a native Windows runner.

Record this choice in the R1 dependency list and the `spinup-exec` design.
Keep tokio in the generated axum API, as the fixed decisions require.
Use the Zig adapter for Linux musl and Windows x64 static checks.
Repeat the Windows arm64 check after the main loop provides its Rust target libraries.
Complete Windows runtime validation before the release gate.
The Go product and all files outside this unit remain unchanged.
