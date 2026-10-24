# R0.2 minijinja conversion

Issue: #10. Spike code: `spikes-rs/r0-02-minijinja/`.

## Question

Can minijinja compile all pack templates and preserve the neutral golden files?
Can a Go converter preserve the template functions and whitespace rules?

## Method

The converter uses `text/template/parse`.
It copies the complete pack tree, including hidden directories.
It converts template sources and output paths.
It reads paths through the existing TOML parser.
It preserves all other metadata.

The converter reads inline strings through Go syntax trees.
The evaluator resolves string concatenations and the single-return `formMarkup` helper.
It does not execute generator code.
The committed inventory lists every converted inline template.

The final delimiters are `[[ expression ]]`, `[% statement %]`, and `[# comment #]`.
HTML escaping is disabled.
Undefined field access fails.
The renderer preserves the final newline before normalization.

Go parses the whitespace trim markers before conversion.
The converter writes the resulting text nodes without additional trimming.
It converts pipelines into nested calls.
It preserves the root context through `data`.
It changes the local context for `with` and `range`.
It converts map indexing into `get`.

Rust implements each function that the pack sources use.
The helpers include `lower`, `upper`, `quote`, `join`, `indent`, `default`, and `has`.
They also include `image`, `npm`, `gomod`, and `uses`.
The case helpers are `snake`, `kebab`, `camel`, and `pascal`.
The converter also handles `printf`, comparisons, boolean operations, assignments, and sorted iteration.
The resource pages use a separate `page_indent` helper.

Go writes the expected helper outputs through the existing renderer.
Go also writes its Unicode categories and simple case mappings.
Rust uses these tables instead of Rust Unicode categories.
The oracle covers acronym boundaries, empty collections, missing indexes, Unicode, control characters, and stack lookups.
The string inputs contain valid UTF-8.

The Go fixture records validated answers and enabled file specifications.
It uses the same answers and selections as the Go golden tests.
Rust renders the converted source and output path.
It compares normalized bytes, output inventories, and declared modes with the existing golden trees.
Rust does not call Go during rendering.

The neutral pack set is `base`, `web`, `ci`, `deploy`, `monitoring`, `backups`, `secrets`, and `tofu`.
The comparison also includes the internal `_sample` fixture.
The case set includes `web-variants` and `web-embed`.
The excluded golden cases are `api`, `api-openapi`, `jobs`, `auth`, `mail`, and every `gen-resource*` case.

The `web-embed` comparison excludes the Go files under `api/`.
Those files belong to the excluded API pack.
It also excludes `deploy/Dockerfile.api` and `deploy/compose.dev.yml`.
Both files belong to the API pack.

The other case selections match the Go golden tests exactly.
The backups comparison selects the same scheduled backup files as its Go golden test.
The tofu comparison removes the `deploy/infra/tofu/` prefix.

The machine runs Darwin 25.4.0 on arm64.
Go is 1.27.2.
Rust is pinned to 1.99.1.
The machine is shared and was not idle.
Cargo used four build jobs and a separate target directory.
Hardware memory details were not measured because the sandbox denied `sysctl`.

Regenerate the converted files with:

```sh
go run ./scripts/rust/convert-templates
```

Regenerate the Go oracle fixtures with:

```sh
go test -race ./scripts/rust/convert-templates/... -update
```

The ordinary Go tests check fixture freshness and the complete converted tree.
The Rust tests compile every template and metadata path.
They enforce complete equality for the three required groups.
They also enforce the overall threshold.

## Numbers

The checks ran on the shared machine.
The load values came from `uptime` immediately before each command.
The durations measure the complete warm command, including its checks.
They are not renderer benchmarks.
The captured outputs are in `spikes-rs/r0-02-minijinja/checks.json`.

| Check | Result | Duration | Load average, one minute |
|---|---|---:|---:|
| `scripts/rust/spike-check.sh r0-02` | pass | 21.280 seconds | 18.73 |
| `cargo clippy --workspace --all-targets --offline --locked -- -D warnings` | pass | 0.751 seconds | 14.54 |
| `make build lint` | pass | 9.380 seconds | 15.22 |
| `go test -race ./scripts/rust/convert-templates/...` | pass | 5.189 seconds | 14.96 |

The build printed a cache-write warning because the sandbox denied a Go cache update.
The build and all three lint passes returned success.
A mutation check changed the snake separator.
The Go oracle rejected the mutation.
The original source was restored before the final checks.

All counts refer to the committed source and golden trees.

| Measure | Result |
|---|---:|
| Pack templates | 74 templates |
| Inline templates | 22 templates |
| Compiled templates | 96 templates |
| Go helper oracle | 91 equal cases |
| Golden cases | 23 cases |
| Golden files | 462 files |
| Equal files | 461 files |
| Unequal files | 1 file |
| Overall equality | 99.78354978354979 percent |
| Base equality | 100 percent |
| Web equality | 100 percent |
| Web variants equality | 100 percent |

Each row below names a golden case.
A case can include files from several packs.

| Golden case | Files | Equal files | Unequal files |
|---|---:|---:|---:|
| base | 6 | 6 | 0 |
| web | 116 | 116 | 0 |
| web-variants/node-pnpm-openapi-fetch | 8 | 8 | 0 |
| web-variants/node-pnpm-heyapi | 24 | 24 | 0 |
| web-variants/node-bun-openapi-fetch | 7 | 7 | 0 |
| web-variants/node-bun-heyapi | 23 | 23 | 0 |
| web-variants/embed-pnpm-openapi-fetch | 7 | 7 | 0 |
| web-variants/embed-pnpm-heyapi | 23 | 23 | 0 |
| web-variants/embed-bun-openapi-fetch | 6 | 6 | 0 |
| web-variants/embed-bun-heyapi | 22 | 22 | 0 |
| web-embed | 123 | 123 | 0 |
| ci/api-only | 3 | 3 | 0 |
| ci/full | 6 | 6 | 0 |
| ci/deploy-on-push | 6 | 6 | 0 |
| ci/web-scaffold/no-auth | 2 | 2 | 0 |
| ci/web-scaffold/auth | 2 | 2 | 0 |
| deploy | 11 | 11 | 0 |
| monitoring/lite | 17 | 17 | 0 |
| monitoring/full | 25 | 25 | 0 |
| backups | 9 | 9 | 0 |
| secrets | 2 | 2 | 0 |
| tofu | 6 | 6 | 0 |
| _sample | 8 | 7 | 1 |
| Total | 462 | 461 | 1 |

Files grouped by their source pack include repeated golden variants.

| Pack | Files | Equal files | Unequal files |
|---|---:|---:|---:|
| _sample | 8 | 7 | 1 |
| backups | 9 | 9 | 0 |
| base | 6 | 6 | 0 |
| ci | 21 | 21 | 0 |
| deploy | 22 | 22 | 0 |
| monitoring | 42 | 42 | 0 |
| secrets | 2 | 2 | 0 |
| tofu | 6 | 6 | 0 |
| web | 346 | 346 | 0 |

The unequal file is `_sample/api/cmd/demo/main.go`.
Its first difference occurs at byte offset 13.
Go inserts blank lines and formats the function body through `go/format`.
Rust preserves the unformatted template output.
No other unequal file exists in the measured set.

Special conversion rules apply to these sources:

| Source | Rule |
|---|---|
| base | Preserve the changed context inside `with` and the module indexes inside `range`. |
| deploy, ci, web workflows | Preserve branch assignments for the PostgreSQL image key. |
| web workflows | Support `printf "%.2s"` with precision in Unicode characters. |
| web pack fragments | Decode TOML literal strings before converting output paths. |
| backups | Preserve the literal closing brackets in the Python expression. |
| web Playwright configuration | Preserve the deliberately emitted opening brackets and the literal closing brackets. |
| inline job templates | Escape a literal bracket before the converted output marker. |
| inline resource templates | Resolve composed strings and preserve the page-specific indent arguments. |
| inline plugin scaffold | Escape TOML array headers that contain the expression delimiters. |

No pack needs a hand-written content replacement.

The delimiter audit found these literal clashes.
Paths for the packs are relative to `internal/templates/packs/`.
Inline paths identify the original Go variable or scaffold source.

| Source | Line | Literal token | Treatment |
|---|---:|---|---|
| backups/files/deploy/backups/run.sh.tmpl | 41 | `]]` | Keep the text outside an expression. |
| web/files/web/playwright.config.ts.tmpl | 6 | `[[` and `]]` | Preserve the quoted action and closing text. |
| internal/plugin/scaffold/templates/plugin.toml.txt | 12 | `[[` and `]]` | Emit the opening token as a quoted expression. |
| internal/plugin/scaffold/templates/plugin.toml.txt | 18 | `[[` and `]]` | Emit the opening token as a quoted expression. |

Conversion also creates a potential opening marker at these boundaries:

| Inline template | Line | Boundary |
|---|---:|---|
| job/jobTemplate | 23 | `Job[{{ .Pascal }}` |
| job/jobTemplate | 24 | `Job[{{ .Pascal }}` |
| job/testTemplate | 18 | `Job[{{ .Pascal }}` |

The converter emits the literal bracket through a quoted expression.
No literal `[%` or `[#` clash exists in the source text.
TOML array headers in metadata remain literal because only metadata paths are templates.

The inline inventory contains these sources:

| Go source | Converted files under `packs/_inline/` |
|---|---|
| internal/gen/job/job.go | `job/registerTemplate.tmpl`, `job/jobTemplate.tmpl`, `job/testTemplate.tmpl` |
| internal/gen/resource/pages_server.go | `resource/listServerTS.tmpl`, `resource/itemServerTS.tmpl`, `resource/editServerTS.tmpl`, `resource/newServerTS.tmpl` |
| internal/gen/resource/pages_svelte.go | `resource/listSvelte.tmpl`, `resource/itemSvelte.tmpl`, `resource/editSvelte.tmpl`, `resource/newSvelte.tmpl`, `resource/fieldsSvelte.tmpl` |
| internal/gen/resource/pages_schema.go | `resource/schemaTS.tmpl` |
| internal/plugin/scaffold/templates/.gitignore.txt | `plugin-scaffold/.gitignore.tmpl` |
| internal/plugin/scaffold/templates/Makefile.txt | `plugin-scaffold/Makefile.tmpl` |
| internal/plugin/scaffold/templates/README.md.txt | `plugin-scaffold/README.md.tmpl` |
| internal/plugin/scaffold/templates/files_note.txt.tmpl.txt | `plugin-scaffold/files_note.txt.tmpl.tmpl` |
| internal/plugin/scaffold/templates/go.mod.txt | `plugin-scaffold/go.mod.tmpl` |
| internal/plugin/scaffold/templates/main.go.txt | `plugin-scaffold/main.go.tmpl` |
| internal/plugin/scaffold/templates/main_test.go.txt | `plugin-scaffold/main_test.go.tmpl` |
| internal/plugin/scaffold/templates/pack.toml.txt | `plugin-scaffold/pack.toml.tmpl` |
| internal/plugin/scaffold/templates/plugin.toml.txt | `plugin-scaffold/plugin.toml.tmpl` |

Three additional call sites accept dynamic template strings.
They contain no embedded template body to extract.
These are `plugin/render.execute`, `plugin/pack.checkTemplatePath`, and `render.execute`.
Their inputs are plugin sources, plugin paths, pack sources, or pack paths.
The converter handles the static pack sources and paths.
External plugin conversion needs the supplied plugin sources.

## Verdict

Verdict: go

All required compilation and equality targets pass.
The single mismatch belongs to an internal Go fixture.
The selected delimiters preserve the existing languages and workflow expressions.
No new crate or Go module was required.

## Recommendation

Use minijinja 3.0.3 with these delimiters and explicit Go helpers.
Keep converted packs generated.
Retain the Go fixture generator until the rewrite gate removes Go.
Port the manifest loader separately from the template renderer.

Keep the Unicode tables tied to the Go oracle version.
Reject unsupported syntax during conversion.
Named templates and declarations inside `if` or `with` are currently unsupported.
The current packs do not use those forms.
The converter does not claim support for arbitrary external Go templates.

Handle Go formatting only if later parity checks include Go source outputs.
The Rust API templates will need the formatter decision from R0.10.
No Docker or PostgreSQL check was needed.
No network command was run.
