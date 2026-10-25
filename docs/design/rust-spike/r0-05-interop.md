# R0.5 Format interop

Issue: #13. Spike code: `spikes-rs/r0-05-interop/`.

## Question

Can Rust reproduce the Go lock and diff bytes?
Can Rust verify Go archives and reproduce the Go path rules?

## Method

The oracle is a Go test with an `-update` flag.
It calls the existing lock, archive, and VFS packages.
It uses `strconv.Quote`, which the template function uses directly.
It does not change the Go product.
The issue body comes from the accepted plan because this lane has no network access.

The lock fixtures cover HTML escapes, Unicode separators, nil collections, empty collections, number precision, collection ordering, and the final newline.
The Rust reader uses `serde_json::value::RawValue` to preserve number tokens.
It writes sorted objects with Go indentation and HTML escapes.
The writer normalizes the lock collections before serialization.
It preserves pack order.

The diff port follows the Go stack, patience anchors, subsequence selection, and hunk construction.
A fixed PCG seed generates the edit cases.
Additional cases cover repeated lines, crossed anchors, separate hunks, CRLF, missing final newlines, binary data, and invalid UTF-8.
Boundary cases cover the binary scan and the size limit.
Byte fields use base64, with additional zstd compression for large cases.

The Go archive uses a public test seed and a detached Ed25519 signature.
Rust verifies the compressed bytes with `ed25519-dalek`.
The tests reject changes to the archive and the signature.
Rust decodes the archive with `zstd` and `tar`.
The writer sorts entries and uses stable USTAR headers and single-thread compression.
The tests compare paths, contents, modes, entry types, owners, and modification times.
The Go reader also accepts the Rust archive and compares its decoded entries.

The path fixtures use the Go portable-path and symlink regressions.
They also include planned and disk collisions under case folding.
The Rust adapter adds the Go policy to `cap-std`.
It permits internal parent symlinks during diff and removal.
It rejects final symlinks, external parent symlinks, and parent symlinks during writes.
It converts absolute internal link targets to relative capability paths.
The raw capability test also rejects parent, absolute, and symlink escapes.

The native tests ran on macOS 26.4.1, Darwin 25.4.0, ARM64.
The toolchains were Rust 1.99.1 and Go 1.27.2.
Other sessions used the machine during these checks.
The measurement log records uptime before and after each timed command.
The CPU model and memory capacity were not measured because the sandbox rejected `sysctl`.
These check durations are not performance benchmarks.

## Numbers

| Measurement | Result |
| --- | --- |
| Canonical locks | 6 of 6 cases byte-equal, 100 percent |
| Diff fixtures | 262 of 262 cases byte-equal, 100 percent |
| Generated diff fixtures | 240 cases with a fixed seed |
| Quote fixtures | 267 cases generated with Go |
| Portable paths | 119 of 119 cases equal on macOS |
| Planned paths | 6 of 6 cases equal on macOS |
| Rooted paths | 20 of 20 scenarios equal for diff and apply on macOS |
| Path parity on macOS | 100 percent of oracle cases |
| Go archive | 6,893 bytes |
| Rust archive | 6,875 bytes |
| Decoded archive | 9 entries equal in both directions |
| Detached signature | 64 bytes, verified |
| Public test key | 32 bytes |
| Archive and signature tampering | Both rejected |
| Rust archive determinism | Two writes produce identical bytes |
| Oracle determinism | Two complete generations per test produce identical bytes |
| Linux target | Path library and tests compile for `x86_64-unknown-linux-gnu` |
| Windows target | Path library and tests compile for `x86_64-pc-windows-gnu` |
| Native platform tests pending | 2 platforms, Linux and Windows |
| Regression sensitivity | An inverted diff size limit fails the oracle test |

The timing table records seconds and the one-minute load average from uptime.
The JSON log contains the complete uptime output for each measurement.

| Check | Duration | Load before | Load after |
| --- | --- | --- | --- |
| Go build and lint | 7.044 s | 12.21 | 11.71 |
| Go oracle with frozen fixtures | 3.521 s | 10.27 | 9.77 |
| Go oracle twice and Rust archive validation | 5.196 s | 12.22 | 13.97 |
| Rust workspace clippy | 7.927 s | 11.97 | 12.39 |
| Rust archive write | 1.777 s | 13.96 | 13.96 |
| Linux path target check | 0.619 s | 11.89 | 12.62 |
| Windows path target check | 0.591 s | 12.62 | 12.62 |
| Full spike check | 2.321 s | 12.62 | 12.62 |

## Verdict

Verdict: go

Rust passes all local format and path checks.
The oracle is deterministic, and Go accepts the Rust archive.
The path library and its tests compile for Linux and Windows.
Native Linux and Windows execution remains a gate task for the main loop.
This result does not claim native execution on those platforms.

## Recommendation

Use `RawValue` for Go number tokens.
Do not route these tokens through floating-point values or `serde_json::Number`.
Even arbitrary precision mode changes exponent spelling.
Keep the HTML escape adapter, canonical collection rules, and trailing newline.

Use the direct patience diff port and the committed oracle fixtures.
Use `ed25519-dalek`, `zstd`, and `tar` for the archive format.
Compare Go and Rust archives by decoded entries.
Port the Go archive limits and unsafe-entry checks in R1.
Require repeated Rust writes to produce identical compressed bytes.

Use `cap-std` with the explicit Go path policy.
A capability alone does not enforce the device-name, case-collision, or final-symlink rules.
Preserve the distinction between writes and removal through internal parent symlinks.
The current case folding covers the regression cases, not every Unicode fold class.
A complete VFS port must also implement atomic apply and rollback.

Run these commands on native Linux and Windows runners before the final R0 decision:

```sh
scripts/rust/spike-check.sh r0-05
cd spikes-rs/r0-05-interop
cargo clippy --workspace --all-targets --offline --locked -- -D warnings
cd ../..
go test -race ./internal/oracle/...
```

Also run the archive verification commands in the spike README on both systems.
Use a Windows runner with Developer Mode or `SeCreateSymbolicLinkPrivilege`.
Use the pinned toolchains, cached dependencies, and a native C compiler for `zstd-sys`.
The cross checks cover the path member only.
They do not compile the archive dependency for those targets.

The spike checks valid schema 2 lock inputs.
Schema migration and full lock validation belong to R1.
The quote fixtures are ready for R0.2 and R1.
No plan change is required for the measured formats.
