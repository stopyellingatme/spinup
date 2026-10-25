# R0.14 release

Issue: #22. Spike code: `spikes-rs/r0-14-release/`.

## Question

Can the Rust builder produce all six release archives with the ADR 0012 names?
Do auditable metadata, syft, and cosign remain compatible with the release process?

## Method

The binary uses clap and implements `--version` and `--self-check`.
The self-check constructs the R0.4 HTTP client and tests random generation and zstd compression.
It uses `ureq` 3.5.0, rustls 0.23.46, and the explicit `ring` provider.
It makes no network request.

The release profile uses thin LTO, one codegen unit, stripped symbols, and abort on panic.
The spike configuration uses `builder: rust`, `tool: cargo`, and `command: zigbuild`.
It lists six target triples.
The post-build hook runs `cargo auditable zigbuild` and replaces the artifact before packaging.
Syft reads the resulting binary and writes SPDX JSON.

The host runs Darwin 25.4.0 on arm64 with Rust 1.99.1 and LLVM 23.1.1.
The other tools are cargo-zigbuild 0.24.0, Zig 0.17.0, cargo-auditable 0.7.7, GoReleaser 2.18.3, syft 1.54.1, and cosign 3.1.3.
Cargo uses the shared cache and this lane's target directory.
Builds use four jobs.
GoReleaser uses one concurrent task.
Other sessions used the machine during these measurements.

The required six-target command failed during prerequisite validation.
GoReleaser initially invoked rustup to install the missing Windows arm64 target.
The attempt failed, and no target was installed.
The spike now supplies an offline guard that checks installed targets without invoking rustup.
The guarded command also fails on Windows arm64.

A native snapshot then reached cosign without a signing key.
Cosign failed during initialization of its trust cache.
No signature was produced.
Subsequent commands used `--skip=publish,sign`.
These flags preserve the SBOM step and skip both signing steps.
This corrects the assumption that snapshot mode alone skips signing.
The binary signer ran before packaging.
The checksum signer remained configured but did not execute after that failure.

GoReleaser also ignored `CARGO_TARGET_DIR` when it copied the built binary.
The probe links `./target` to the lane target directory.
The workaround permits the installed targets to complete.
The probe tested each target separately, then assembled all five available targets in one release.
The combined release has one checksum file.

The local unit brief supplies issue #22's requirements.
The remote issue was not read because the lane must remain offline.
No Go source, workflow, or file outside this unit changed.

## Numbers

| Acceptance item | Observed result |
|---|---:|
| Required targets | 6 targets |
| Linked targets | 5 targets |
| Archives with the required names | 5 archives |
| Archives from the full six-target command | 0 archives |
| SPDX documents | 5 documents |
| Verified SHA-256 entries in the combined release | 10 entries |
| Archives containing a release license | 0 archives |
| Executed Darwin self-checks | 2 targets |
| Actual cosign signatures | 0 signatures |
| Copied free crate names | 3 names |
| Copied taken crate names | 1 name |
| Passing Rust integration tests | 2 tests |

The archive version is `0.0.0-r0.14`.
These are the exact output names:

| Archive | Size | SBOM packages |
|---|---:|---:|
| `spinup_0.0.0-r0.14_linux_amd64.tar.gz` | 1,450,373 bytes | 38 packages |
| `spinup_0.0.0-r0.14_linux_arm64.tar.gz` | 1,321,146 bytes | 38 packages |
| `spinup_0.0.0-r0.14_darwin_amd64.tar.gz` | 1,368,439 bytes | 38 packages |
| `spinup_0.0.0-r0.14_darwin_arm64.tar.gz` | 1,234,243 bytes | 38 packages |
| `spinup_0.0.0-r0.14_windows_amd64.zip` | 1,439,432 bytes | 42 packages |
| `spinup_0.0.0-r0.14_windows_arm64.zip` | not measured: target unavailable | not measured: no binary |

Each archive contains the binary, `README.md`, and `CHANGELOG.md`.
The repository has no matching release `LICENSE` file.
The spike retains the existing license glob and records the missing file.
It does not invent a license.

Each timing includes command execution, prerequisite checks, builds, packaging, and syft when reached.
These measurements are single observations, not benchmark distributions.
Darwin arm64 and the combined release reused compiled artifacts.
The other successful target builds began without target-specific artifacts.
Shared host dependencies were already available after the initial native attempt.
The load columns show the one-minute load average before and after each command.

| Command or target | Result | Elapsed | Load before | Load after |
|---|---|---:|---:|---:|
| Required six-target snapshot | failed: implicit target install | 0.844 s | 10.30 | 10.30 |
| Guarded six-target snapshot | blocked: Windows arm64 | 1.227 s | 7.26 | 7.26 |
| Final six-target snapshot with signing skipped | blocked: Windows arm64 | 1.334 s | 12.13 | 12.13 |
| Linux amd64 | passed | 51.118 s | 10.07 | 10.91 |
| Linux arm64 | passed | 47.784 s | 10.91 | 14.81 |
| Darwin amd64 | passed | 38.426 s | 14.81 | 13.42 |
| Darwin arm64 | passed, warm | 2.921 s | 13.42 | 12.75 |
| Windows amd64 | passed | 45.356 s | 12.75 | 11.66 |
| Windows arm64 | blocked before build | 0.556 s | 11.66 | 11.66 |
| Combined five-target release | passed, warm | 13.734 s | 8.94 | 10.13 |
| Snapshot without signing skip | failed at cosign | 6.866 s | 11.01 | 11.25 |
| Prebuilt configuration check | rejected | 0.035 s | 12.76 | 12.76 |
| Available archive verification | passed | 0.136 s | 10.13 | 10.13 |
| Full acceptance verification | failed: missing sixth archive | 0.138 s | 10.13 | 10.13 |
| Darwin amd64 self-check | passed through Rosetta | 2.354 s | 12.81 | 12.18 |
| Darwin arm64 self-check | passed | 0.192 s | 12.18 | 12.18 |
| `spike-check.sh r0-14` | passed | 11.595 s | 7.85 | 8.98 |
| Workspace clippy with warnings denied | passed | 3.574 s | 8.98 | 8.66 |

The self-check observations include process launch.
They do not measure a TLS handshake or isolate Rosetta overhead.
R0.13 must measure the load limit under its controlled workload.
These shared-machine observations cannot establish that limit.

The C dependencies are `ring` 0.17.14 and `zstd-sys` 2.1.2 with zstd 1.5.7.
Their bundled C and assembly compile into the binaries.
The binary uses no OpenSSL or `aws-lc-rs` crate.
Syft confirms `ring`, rustls, `ureq`, `zstd`, and `zstd-sys` in every available binary.

| Target | Runtime library evidence | Runner requirement |
|---|---|---|
| Linux amd64 musl | No ELF needed libraries | Build passes here. Linux execution remains untested. |
| Linux arm64 musl | No ELF needed libraries | Build passes here. Linux execution remains untested. |
| Darwin amd64 | `libSystem.B.dylib`, `libcharset.1.dylib`, `libiconv.2.dylib` | Build and self-check pass here through Rosetta. |
| Darwin arm64 | The same three OS libraries | Build and self-check pass on this native host. |
| Windows amd64 GNU | Windows DLLs and UCRT API sets | Build passes here. A Windows runner must test execution. |
| Windows arm64 | not measured: no binary | Provide a Windows arm64 runner or a provisioned cross toolchain. |

The Windows imports are `KERNEL32.dll`, `ntdll.dll`, `bcryptprimitives.dll`, `bcrypt.dll`, `ADVAPI32.dll`, and `WS2_32.dll`.
They also include `api-ms-win-core-synch-l1-2-0.dll` and six UCRT API sets.
The UCRT sets cover heap, locale, math, runtime, stdio, and environment.
No import names `libgcc_s_seh-1.dll`, `libwinpthread-1.dll`, or `libstdc++-6.dll`.
The literal rule against every system C library fails on Darwin and Windows.
Both Linux musl binaries satisfy that rule.

The installed toolchain lacks `aarch64-pc-windows-gnullvm` and `aarch64-pc-windows-msvc`.
The host also lacks `cargo-xwin` and `aarch64-w64-mingw32-clang`.
The gnullvm route needs the missing Rust target.
R0.4 also found a missing `aarch64-w64-mingw32-clang` compiler during its ordinary Cargo check.
Whether Zig supplies all necessary gnullvm link support remains unmeasured.
An LLVM-mingw toolchain is another candidate for that route.
The alternative uses `cargo-xwin` with the MSVC target and Windows SDK libraries.
Neither route was provisioned or proven here.
No target or tool was installed.

The main loop checked the following crate names on 2026-10-24.
This spike copies that result without a network query.

| Crate name | Availability |
|---|---|
| `spinup-kit` | free |
| `spinup-plugin-sdk` | free |
| `spinup-cli` | free |
| `spinup` | taken |

## Verdict

Verdict: soft

The Rust builder produces five named archives, auditable binaries, SBOMs, and correct checksums.
The six-archive acceptance bar remains unmet.
Windows arm64 lacks its target and cross toolchain.
The repository also lacks the license required inside each archive.
The installed GoReleaser edition rejects `builder: prebuilt`.
These findings require release changes and further validation.

## Recommendation

Keep the Rust builder and cargo-zigbuild for the five proven targets.
Keep the auditable hook and the binary SBOM step.
Use `--skip=publish,sign` for offline snapshots.
Keep the installed-target guard during offline probes.
Retain the target-directory workaround until GoReleaser supports the configured target directory.

Use a provisioned Windows arm64 runner for the remaining artifact.
Test the MSVC route there before selecting a permanent release target.
Alternatively, provision the gnullvm target outside this offline lane.
Check Zig link support before adding LLVM-mingw.
Test `ring`, zstd, and native execution before accepting that alternative.
Test Windows amd64 execution on a Windows runner too.
Run Linux execution checks on both target architectures.

A prebuilt fallback must import the native artifact before archiving, SBOM generation, and checksum generation.
The installed GoReleaser reports `invalid builder: prebuilt` for that configuration.
Use a release edition that supports the builder, or implement a separate packaging stage.
Do not present this fallback as tested or available in the installed edition.

Add a chosen release license through the main loop.
Clarify the C dependency rule to permit the required OS libraries.
Keep optional external crypto and compression libraries prohibited.
Repeat the full release and `python3 verify.py --require-six` after these blockers are resolved.
Real keyless signing remains untested because this lane has no signing identity or network access.
No workflow file was written, so actionlint has no applicable input.
Docker and Postgres were not required.
