# R0.1 YAML editing

Issue: #9. Spike code: `spikes-rs/r0-01-yaml/`.

## Question

Can a Rust YAML editor reproduce Go output and retain comments across the five production families?
Compare `yamlpath` plus `yamlpatch`, `yaml-edit`, and a small editor that uses tree-sitter source spans.

## Method

The oracle generates 32 fixtures offline.
Each fixture contains `input.yaml`, `edit.json`, and `expected.yaml`.
The input uses Go formatting.
The edit specifies paths, operations, and YAML fragments for new values.
An append retains collection style unless the edit requests block style.

The helper copies the production node editors into its own package.
It imports only the standard library and `go.yaml.in/yaml/v3`.
For each production fixture, it compares fragment operations with the corresponding production edit.
It writes the production output only when these results agree.
The Go test regenerates every fixture and compares all three files.

The five production families are:

| Family | Production reference | Fixtures | Edit |
|---|---|---|---|
| SOPS | `internal/secrets/init.go:131` | `sops-01` to `sops-05` | Replace recipients, insert recipients, or append an environment rule. |
| Alerts | `internal/gen/alert/alert.go:65` | `alert-01` to `alert-04` | Append a rule and create the custom group when required. |
| Compose | `internal/maintain/pg/compose.go:25`, writer at line 75 | `compose-01` to `compose-04` | Change the image, mount, data path, and external volume. |
| User data | `internal/provision/userdata.go:16`, encoder at line 38 | `userdata-01` to `userdata-04` | Insert synthetic SSH host keys and the complete rotation tooling. |
| Rotation | `internal/provision/userdata_rotate.go:96` | `rotation-01` to `rotation-04` | Append the complete script, sudoers file, and activation command. |

The supplemental fixtures cover the exact codemod example and `EditYAML` at `internal/codemod/yaml.go:16`.
They also cover insertion, deletion, null conversion, aliases, flow mappings, literal strings, Unicode, and unchanged output.
The SSH fixture tests the scalar replacement at `internal/server/server.go:193`.
`internal/maintain/pgupgrade/cleanup.go:100` only parses Compose for validation.
It does not write YAML.
The validation at `internal/maintain/pg/compose.go:152` also performs no edit.

The Rust test runs all three candidates against all 32 fixtures.
It requires equal bytes and retention of every original YAML comment for a pass.
It checks comments from Go nodes, excluding shell comments inside strings.
An adapter error counts as a failure.
`observations.json` records every outcome and the first unequal byte or error.
A passing comparison test confirms these recorded outcomes, including failures.

The adapters use native library operations.
The splice adapter edits syntax spans and uses the edit fragments for inserted values.
No adapter reads the expected output.
The prototype leaves unsupported policies explicit rather than inventing output.
These results measure the adapters, not an exhaustive limit of each library.

Issue #9 was unavailable locally.
The lane prohibits network access, so this work uses the supplied issue brief and binding unit row.
The repository has no `lat.md/` directory.
No lat command ran.

## Numbers

The machine ran macOS 26.4.1 on arm64 with Rust 1.99.1.
The C compiler was Apple clang 21.0.0.
Each clean build used a separate empty target, the cached registry, and four jobs.
The measurement disabled `RUSTC_WRAPPER`.
It retained the default Cargo development profile.
The machine was busy with other sessions.
These samples do not support a reliable comparison of build speed.

| Candidate | Passes | Byte equality | Comment checks passed | Complete production families | Adapter lines |
|---|---:|---:|---:|---:|---:|
| yamlpath+yamlpatch | 9/32 | 9/32 | 18/32 | 0/5 | 53 lines |
| yaml-edit | 11/32 | 11/32 | 26/32 | 0/5 | 78 lines |
| tree-sitter splice | 17/32 | 17/32 | 18/32 | 1/5 | 215 lines |

The shared harness contains 160 lines.
The command entry contains 4 lines.
Line counts include comments and blank lines after `cargo fmt`.
They exclude the Go oracle, tests inside the oracle, fixture files, and dependency code.
An error fails the comment check because it produces no output.

| Production family | yamlpath + yamlpatch | yaml-edit | Splice |
|---|---:|---:|---:|
| sops | 3/5 | 3/5 | 5/5 |
| alert | 1/4 | 1/4 | 2/4 |
| compose | 1/4 | 1/4 | 1/4 |
| userdata | 0/4 | 0/4 | 1/4 |
| rotation | 0/4 | 0/4 | 1/4 |

P means equal bytes with comments retained.
F means unequal bytes, a missing comment, or an adapter error.

| Fixture | yamlpath + yamlpatch | yaml-edit | Splice |
|---|---|---|---|
| alert-01 | F | F | F |
| alert-02 | P | P | P |
| alert-03 | F | F | F |
| alert-04 | F | F | P |
| codemod-delete | F | F | F |
| codemod-insert | F | F | F |
| codemod-noop | P | P | P |
| codemod-set | F | F | P |
| compose-01 | F | F | F |
| compose-02 | F | F | F |
| compose-03 | P | P | P |
| compose-04 | F | F | F |
| edge-alias | F | P | P |
| edge-flow | F | P | P |
| edge-literal | P | P | P |
| edge-null | F | F | F |
| edge-replace | F | F | F |
| edge-ssh | P | P | P |
| edge-unicode | P | P | P |
| rotation-01 | F | F | F |
| rotation-02 | F | F | P |
| rotation-03 | F | F | F |
| rotation-04 | F | F | F |
| sops-01 | P | P | P |
| sops-02 | P | P | P |
| sops-03 | F | F | P |
| sops-04 | F | F | P |
| sops-05 | P | P | P |
| userdata-01 | F | F | F |
| userdata-02 | F | F | P |
| userdata-03 | F | F | F |
| userdata-04 | F | F | F |

| Candidate | Clean build | Load before | Load after | C compile sum | Load during C compilation |
|---|---:|---:|---:|---:|---|
| yamlpath+yamlpatch | 29.804 s | 20.58 | 35.07 | 3.064 s | 30.44 to 34.73 |
| yaml-edit | 33.302 s | 35.07 | 23.43 | 0.000 s | No C compilation |
| tree-sitter splice | 30.515 s | 23.43 | 16.65 | 0.534 s | 16.65 to 16.65 |

Load values are the one-minute averages from `uptime`.
The C sum adds compiler process durations and can include concurrent work.
It excludes compiler feature probes.
The clean build includes the compiler wrapper and its probes.
`build-cost.json` contains the exact commands, all compiler calls, and the complete `uptime` output.

| Candidate | C source | Compile duration | One-minute load |
|---|---|---:|---:|
| yamlpath+yamlpatch | lib.c | 2.474 s | 30.44 |
| yamlpath+yamlpatch | parser.c | 0.380 s | 34.73 |
| yamlpath+yamlpatch | scanner.c | 0.210 s | 34.73 |
| tree-sitter splice | parser.c | 0.101 s | 16.65 |
| tree-sitter splice | lib.c | 0.360 s | 16.65 |
| tree-sitter splice | scanner.c | 0.073 s | 16.65 |

Both the splice candidate and `yamlpath` compile tree-sitter C sources.
The runtime compiles `lib.c`.
The YAML grammar compiles `parser.c` and `scanner.c`.
The build links these objects into the binary.
It requires a C compiler during the build.
`yaml-edit` uses rowan and required no C compilation in this measurement.

The splice candidate passes all SOPS cases and the block-form rotation case.
Its remaining errors include flow appends, comment relocation, nested creation, and null conversion.
It also lacks Go map serialization for Compose.
Its deletion loses descendant comments.

The native adapters change scalar quoting and whitespace in some cases.
Both retain the existing SOPS comments but lose quote style in `sops-04`.
Neither native adapter reproduces the complete rotation output.
Their failures include flow style, literal-string whitespace, and null conversion.
`yamlpatch` rejects append operations on flow sequences.

The Compose oracle exposes a conflict in the acceptance rule.
The Go map writer sorts keys and discards comments.
A Rust editor cannot retain those comments and also produce equal bytes for that input.
The matrix keeps both checks and records the failure.
`compose-03` contains no comments and passes every candidate.

The following checks passed:

- `scripts/rust/spike-check.sh r0-01`
- `cargo clippy --workspace --all-targets --offline --locked -- -D warnings`
- `go test -race ./spikes-rs/r0-01-yaml/oracle`
- `make build lint`

Docker and PostgreSQL are not required by this spike.
No network command ran.

## Verdict

Verdict: soft

No candidate passes every fixture in all five production families.
The splice candidate completes one family and passes 17 of 32 fixtures.
Neither library provides sufficient Go parity through these adapters.
The result changes the R1.5 implementation plan.
It does not establish a hard no-go for the Rust rewrite.

## Recommendation

Use tree-sitter source spans as the basis for the R1.5 editor.
Do not adopt the current prototype as the production editor.
Retain the Go oracle and the complete fixture matrix.
Add tests for generated files before accepting the production editor.

Estimate 600 to 800 adapter lines in total, compared with the current 215 lines.
Estimate another 200 to 350 test lines and three to five engineering days.
These are planning estimates, not measurements.
The estimate covers the bounded spinup edits, not a general YAML serializer.

Implement these missing behaviors:

- Create intermediate mappings and convert null nodes.
- Convert flow collections and emit nested flow values with Go quoting rules.
- Preserve comments when deleting entries or replacing descendants.
- Match literal strings, empty lines, aliases, anchors, and scalar styles.
- Preserve unchanged bytes and reject invalid output.

Keep map serialization separate from node editing for Compose.
Record a Rust parity exception if Compose must retain comments.
Otherwise, document that the Compose compatibility writer removes comments like Go.
Keep the Go product unchanged in either case.
The main loop must resolve this acceptance conflict before the R1.5 gate.

Repeat the build measurements on an idle machine before using them for build budgets.
The recorded busy-machine samples answer the build-cost requirement only for this session.
