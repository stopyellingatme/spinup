# Go oracle fixtures

The oracle records the Go behavior for Rust parity tests.
It calls the product functions and preserves their outputs.
It does not change the product behavior.

Run the frozen checks with this command:

```sh
go test -race ./internal/oracle/...
```

Regenerate the fixtures with this command:

```sh
go test ./internal/oracle/... -update
```

The main loop owns the `make oracle` target.
The target must run the generator with `-update`.
Regeneration must produce no changes after the fixtures are frozen.
The test generates each fixture twice and compares the bytes.
R1 adds separate files and preserves all R0 fixtures.

## R1 formats

Each JSON file ends with one newline.
An empty `error` string means success.
Errors preserve the Go text, including each `Try:` line.
Rust tests can compare error categories when the Rust command requires different instructions.

| File | Input and output |
| --- | --- |
| `quotes-r1.json` | `input` contains bytes as base64. `expected` contains the exact string from the `quote` template function. |
| `case-words.json` | `input` contains a string. Each other field contains the output from the named template function. |
| `semver.json` | `parse` contains version fields, canonical strings, and errors. `compare` contains version pairs and results of -1, 0, or 1. |
| `compat.json` | `kit` records `CheckKit`. `min_cli` records `CheckMinCLI`. `supported` records `SupportedTemplateVersions`, including ordering and duplicates. |
| `when.json` | Each case contains an expression, answers, a Boolean result, and an error. |
| `hashes.json` | `input` and `normalized` contain bytes as base64. `binary` records detection. `hash` includes the `sha256:` prefix. |
| `clean-paths.json` | Each case contains an input path, the cleaned path, and the exact error. |
| `golden-modes.json` | Each case contains file paths, octal modes, content, manifest text, and an error from `testutil.Golden`. |

In `compat.json`, `other` means the kit version or the minimum CLI version for its containing group.
The semver cases use the 64-bit Go integer range of the supported CLI targets.
They preserve Go restrictions, including rejection of build metadata.

The quote cases include each raw byte and malformed UTF-8 sequences.
The R0 `quotes.json` file contains Unicode code points instead.
The case functions run through `render.Render` because the function map is private.
A sentinel preserves trailing whitespace when the renderer normalizes text.

The condition cases include both list representations from Go answers.
JSON encodes both `[]string` and `[]any` as arrays.
A separate Go test checks that these representations give equal results.
Conditions check every predicate, including branches with known results.

The hash cases cover LF, CRLF, CR, binary data, invalid UTF-8, and the 8000-byte boundary.
The clean-path cases extend the R0 portable corpus with exact error text.

## Golden modes

The oracle calls `testutil.Golden` inside temporary repositories.
It captures the actual writer output and tests the actual reader.
The repositories do not modify `testdata/golden/` in this project.

The writer sorts paths and omits files with mode `0644`.
Each remaining entry has four octal digits, one space, the full path, and one newline.
Paths can contain spaces and Unicode characters.
An empty manifest means that all files have mode `0644`.
A null `manifest` means that the manifest file is absent.

The reader accepts absent manifests and applies the mode `0644` by default.
The manifest overrides the permissions from the checkout.
The cases include mode mismatches, unknown paths, duplicate entries, invalid octal values, and unsupported bits.
They also include blank lines, missing final newlines, and CRLF rejection.
