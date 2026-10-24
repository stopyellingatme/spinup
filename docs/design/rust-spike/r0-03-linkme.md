# R0.3 linkme and the wire list

Issue: #11. Spike code: `spikes-rs/r0-03-linkme/`.
The issue body and criteria are recorded in `rust-plan.md`, under “R0.3 linkme and the wire list”.

## Question

Does a distributed slice retain registrations from crates that the binary references only through `use x as _;`?
Does the registry test detect a missing wire line in debug, release, and fat LTO?

## Method

The workspace contains `registry`, `reg-a`, `reg-b`, and `app`.
The registration crates export no callable symbols.
The app names these crates only in `wire_gen.rs`.
Its Cargo manifest declares both dependencies, even when the wire lines are absent.
The registry sorts the IDs before comparison.

The test executes the actual app binary and compares its output with `app/tests/expected_registry.txt`.
This avoids extra imports from the test harness.
The fixture contains `reg-a` and `reg-b`.
A vector comparison also detects duplicate IDs.

`linkme` 0.3.37 is the default backend.
The `inventory` 0.3.25 feature selects the fallback independently.
The `fat` profile inherits release and sets `lto = "fat"` and `codegen-units = 1`.
The baseline disables both backends.

`verify.py` runs each backend in all three profiles.
It removes the first wire, the second wire, and both wires.
Each mutation must fail the registry assertion with exit code 101.
The script also checks the actual remaining IDs after each mutation.
It restores the original file and requires the restored test to pass.

The initial test used an empty wire file and failed with an empty ID list.
Adding both wire lines made the test pass.
The matrix repeats this proof for each backend and profile.

The host ran macOS 26.4.1 on arm64, with Darwin 25.4.0.
The compiler was Rust 1.99.1, with LLVM 23.1.1 and host `aarch64-apple-darwin`.
Builds used four jobs and a separate `target-R0.3` directory.
Other sessions shared the machine.
The native matrix observed a 1-minute load between 8.31 and 12.22.

Run these commands from the spike directory with the lane environment:

```sh
cargo update --offline --workspace
cargo fmt --all
python3 verify.py --output native-results.json
python3 verify.py --cross --output cross-results.json
```

The first cross-build attempt used the default linkers.
The Linux attempt reached Apple's linker, which rejected ELF options.
The Windows attempt could not find `x86_64-w64-mingw32-gcc`.
All twelve combinations passed `cargo check --workspace --all-targets`.

The second attempt used the bundled `rust-lld`:

```sh
export CARGO_TARGET_X86_64_UNKNOWN_LINUX_MUSL_LINKER="$(rustc --print sysroot)/lib/rustlib/aarch64-apple-darwin/bin/rust-lld"
export CARGO_TARGET_X86_64_PC_WINDOWS_GNU_LINKER="$CARGO_TARGET_X86_64_UNKNOWN_LINUX_MUSL_LINKER"
python3 verify.py --cross --output cross-lld-results.json
```

All six Linux builds passed.
All six Windows builds failed because Windows and MinGW libraries were absent.
The missing libraries include `kernel32`, `msvcrt`, and `mingw32`.
The six Windows combinations still passed the static check.

The JSON files contain each command, status, elapsed seconds, load samples, `uptime` output, and compiler details.
A successful cross script means that its static checks completed.
Its command records identify failed links explicitly.

## Numbers

The table gives complete executable sizes in bytes.
“Total cost” compares the wired executable with the executable that has no backend.
“Registration cost” compares the wired executable with the same backend without either wire line.
These deltas include code, metadata, alignment, enumeration, and printing.
They do not measure only the slice data.
The builds retain the default symbol settings.

| Backend | Profile | No backend, bytes | No wires, bytes | Wired, bytes | Total cost, bytes | Registration cost, bytes |
|---|---|---:|---:|---:|---:|---:|
| linkme | dev | 543336 | 562296 | 564040 | 20704 | 1744 |
| inventory | dev | 543336 | 556968 | 563016 | 19680 | 6048 |
| linkme | release | 435168 | 453232 | 453392 | 18224 | 160 |
| inventory | release | 435168 | 453216 | 454096 | 18928 | 880 |
| linkme | fat | 374144 | 377648 | 377824 | 3680 | 176 |
| inventory | fat | 374144 | 374144 | 378208 | 4064 | 4064 |

The next table gives elapsed time for the first complete workspace test in each matrix combination.
The cache state was mixed.
These times are observations, not controlled build benchmarks.
Each load value is the 1-minute load average.
The JSON records pair every other timing with its load samples and `uptime` output.

| Backend | Profile | Elapsed, seconds | Load before | Load after |
|---|---|---:|---:|---:|
| linkme | dev | 1.597 | 8.59 | 8.59 |
| inventory | dev | 1.660 | 9.26 | 9.26 |
| linkme | release | 7.672 | 9.26 | 9.01 |
| inventory | release | 1.725 | 9.01 | 8.77 |
| linkme | fat | 13.778 | 8.31 | 11.58 |
| inventory | fat | 11.147 | 10.89 | 12.22 |

| Result | Count |
|---|---:|
| Expected IDs | 2 IDs |
| Native backend/profile combinations passed | 6 combinations |
| Restored registry tests passed | 6 tests |
| Wire mutations rejected | 18 mutations |
| Cross-target static combinations passed | 12 combinations |
| Linux binaries linked with `rust-lld` | 6 binaries |
| Windows binaries linked | 0 binaries |
| Linux execution combinations measured | 0 combinations |
| Windows execution combinations measured | 0 combinations |

Linux and Windows execution: not measured.
This lane has no Docker access or external runner.
Windows link success: not measured because the offline environment lacks the required libraries.

## Verdict

Verdict: soft

Both backends retain both IDs on macOS arm64 in debug, release, and fat LTO.
Both backends lose registrations when their wire lines are absent.
The registry test detects every tested omission.
Linux linking succeeds with the bundled linker.
The three-platform acceptance bar remains open until Linux and Windows execute the matrix.

## Recommendation

Keep `linkme` and the generated wire list.
Keep the expected IDs in an independent fixture.
Require the registry test before each merge.
Keep `inventory` as a tested fallback.
It also requires wire lines.

macOS needed no extra linker flags or `RUSTFLAGS`.
The Linux cross-build needed only the target linker selection shown above.
Neither backend needed whole-archive flags or forced symbol retention.
Windows needs a native toolchain with its platform libraries.
No evidence from this lane justifies changing the registration design.

The main loop must run `scripts/rust/spike-check.sh r0-03` on Linux and Windows.
It must also run `python3 verify.py --output native-results.json` on each runner.
That script executes both backends, all three profiles, and the mutation checks.
Set an absolute `CARGO_TARGET_DIR` before each run.
On Windows, the script records unavailable load measurements explicitly.
Use a Windows GNU runner with MinGW libraries to match the cross target.

Record these remaining checks in the R0 gate before changing the verdict to `go`.
Do not treat a successful static check as proof of runtime registration.
