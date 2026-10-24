# R0.5 format interop

This workspace compares Rust with the Go product.
The Go oracle generates the inputs and expected results.

Run these commands from the repository root:

```sh
go test -race ./internal/oracle/... -update
scripts/rust/spike-check.sh r0-05
```

Run this command from this directory:

```sh
cargo clippy --workspace --all-targets --offline --locked -- -D warnings
```

## Fixtures

The oracle owns these files under `testdata/oracle/`:

| File | Contents |
| --- | --- |
| `locks.json` | Raw lock inputs and canonical output strings |
| `diffs.json` | File names, byte inputs, deletion flags, and expected diff bytes |
| `quotes.json` | String inputs and the output from `strconv.Quote` |
| `paths.json` | Lexical inputs, acceptance results, and normalized paths |
| `plan-paths.json` | Planned paths and acceptance results |
| `rooted-paths.json` | Filesystem scenarios and results from Go diff and apply |
| `archive-entries.json` | Tar paths, modes, types, and contents from Go |
| `templates-v1.2.3.tar.zst` | The Go archive |
| `templates-v1.2.3.tar.zst.sig` | The detached Ed25519 signature |
| `test-key.pub` | The public test key |

JSON encodes byte fields as base64.
If a diff case has `encoding: "zstd"`, decompress its byte fields after base64 decoding.
This rule applies to `old`, `new`, and `expected`.
The compression keeps the boundary fixtures small.

The generator uses a fixed PCG seed.
Each oracle test generates all fixtures twice and compares their bytes.
The update flag writes only these fixture files.
Other units can add separate fixture files.

The signing seed is public test data in `archive_test.go`.
Never use this seed for a release.
The generator does not read a release key.

## Archive verification with Go

Run these commands from the repository root:

```sh
cargo run --manifest-path spikes-rs/r0-05-interop/Cargo.toml --bin write-archive --offline --locked -- "$CARGO_TARGET_DIR/r0-05-rust.tar.zst"
go test -race ./internal/oracle/... -rust-archive "$CARGO_TARGET_DIR/r0-05-rust.tar.zst"
```

The Go reader validates the Rust archive.
The test compares every decoded entry with the Go archive.
Compressed bytes can differ between Go and Rust.

## Platform checks

The `path-probe` member uses the same path implementation as the main crate.
It excludes the archive dependency on the C compiler.
Run these commands from this directory:

```sh
cargo check -p r0-05-paths --all-targets --target x86_64-unknown-linux-gnu --offline --locked
cargo check -p r0-05-paths --all-targets --target x86_64-pc-windows-gnu --offline --locked
```

A target check does not execute a test.
Run the full spike check on native Linux and Windows runners.
Use a Windows runner with Developer Mode or `SeCreateSymbolicLinkPrivilege`.
Preserve fixture bytes during checkout.
Use the pinned Go and Rust toolchains.
Install the C compiler that `zstd-sys` needs before the offline check.

The adapter adds the Go policy to `cap-std`.
It rejects final symlinks and collisions under case folding.
It permits internal parent symlinks during diff and removal.
It rejects parent symlinks during writes.
It converts an absolute internal link target to a relative capability path.
The raw capability test also rejects parent, symlink, and absolute escapes.

This spike checks valid schema 2 lock inputs.
It does not implement schema migration or full lock validation.
The path checks cover the regression cases, not every Unicode fold class.
The spike does not port atomic apply or rollback.

## Measurements

Use `measure.py NAME COMMAND ...` to record a check.
The script writes the duration, exit status, and uptime before and after the command.
The result file contains the selected measurements.
These durations describe verification under shared load.
They are not performance benchmarks.
