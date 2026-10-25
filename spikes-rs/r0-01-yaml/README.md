# R0.1 YAML editor comparison

Run the oracle from the repository root:

```sh
go run ./spikes-rs/r0-01-yaml/oracle
go test -race ./spikes-rs/r0-01-yaml/oracle
make build lint
```

Run the comparison from this directory:

```sh
cargo run --offline --locked
cargo clippy --workspace --all-targets --offline --locked -- -D warnings
```

The command prints the full matrix as JSON.
`observations.json` records the measured baseline.
The Rust test repeats every comparison and checks this baseline.
A passing test confirms the recorded outcomes, including candidate failures.
It does not mean that every candidate has parity.

Each fixture has three files:

- `input.yaml` contains Go formatting.
- `edit.json` specifies the operations and YAML fragments for the new values.
- `expected.yaml` contains the Go output.

`fixtures/cases.json` supplies the oracle inputs.
The oracle uses copies of the production edits for the five production families.
It verifies that the fragment operations produce the same output.
It uses the copied `EditYAML` code for the codemod cases.
It uses the scalar replacement from `printCloudInit` for the SSH case.
The copies import only the standard library and `go.yaml.in/yaml/v3`.
The oracle test compares regenerated files with every committed fixture.

An append operation retains the collection style unless `block` is true.
This distinction matters for alert groups and rotation lists.
A pass requires equal bytes and every original YAML comment.
The comment check uses comments from Go nodes.
It excludes shell comments inside literal strings.
An error counts as a failure.

The adapters use native library operations where available.
They do not read the expected output.
The splice prototype rejects operations that need an unimplemented policy.
The result document lists these limits.

Measure a clean build for each feature:

```sh
python3 measure-build.py
```

The script creates separate targets under the parent of `CARGO_TARGET_DIR`.
It disables the Rust compiler cache and retains the Cargo source cache.
It builds each candidate in sequence with four jobs from the lane environment.
Its compiler wrapper measures C compilation and records the load from `uptime`.
It writes `build-cost.json` here.
The targets and full build logs stay outside the repository.
Run the script once per measurement series.
A busy machine can change these times substantially.
