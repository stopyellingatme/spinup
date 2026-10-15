# Executable plugins

Executable plugins add generators and doctor checks through JSON-RPC 2.0.
They run with the full privileges of your user account.
They can read files and run programs with those privileges.
The protocol does not provide an operating system sandbox.

The host validates every returned path before it applies a plan.
The generator runner supports `--dry-run`, `--diff`, and `--force`.
The host rejects protected files, path escapes, duplicate paths, and unsafe permissions.
The lock records generated files with `plugin/<name>/@exec/<generator>` as the template.
`spinup upgrade` preserves these files and their lock entries, including local edits.

## Metadata and trust

Declare `kind = "exec"` in `plugin.toml`.
Keep each executable inside `bin/` in the plugin tree.
The approved tree hash covers the executable bytes and permissions.
The existing plugin limits still apply, including the 5 MiB limit for the tree.

```toml
name = "hello"
version = "1.0.0"
api = 1
spinup = ">=0.0.0"
kind = "exec"

[exec]
binaries = { "linux-amd64" = "bin/hello_linux_amd64", "darwin-arm64" = "bin/hello_darwin_arm64" }
```

Keep `pack.toml` in the plugin tree, even when the plugin has no templates.
Use Go names for the platform keys, such as `linux-amd64`.
A missing executable for the host platform produces an error with corrective instructions.

Inspect the source and hash before you grant trust.
At a terminal, type the plugin name to confirm trust.
For an automated operation, supply `--trust-exec <name>`.
The `--trust` flag cannot approve an executable plugin.
The committed allowlist line ends with ` exec`.
An allowlist line without this marker cannot approve execution.

## Protocol

The SDK module is `github.com/stopyellingatme/spinup/plugin-sdk`.
Implement the four methods of `sdk.Handler`.
Call `sdk.Serve(handler)` from the executable entry point.
Use stderr for diagnostics.
Keep stdout exclusively for protocol responses.

Each request and response is one JSON object followed by a newline.
The host sends a handshake before each method invocation.
The handshake must return the approved name, version, and API 1.
The supported capabilities are `generator` and `check`.
Unknown capabilities cause an error.
The host starts a fresh process for each invocation.
Handlers must not depend on state from a previous invocation.

| Method | Parameters | Result |
| --- | --- | --- |
| `handshake` | `null` | `name`, `version`, `api`, `capabilities` |
| `describe` | `null` | `generators`, `checks` |
| `plan` | `generator`, `args`, optional `answers` | `files`, optional `next` |
| `check` | `check` | `findings` |

Each generator description has `name`, `short`, `usage`, and positional `args` names.
Each check description has `name` and `title`.
Each file has `path`, `data_base64`, `class`, and numeric `mode`.
Use `managed`, `scaffold`, or `seed` for the class.
Use numeric permission bits for the mode.
For example, decimal `420` represents mode `0644`.
A zero mode uses `0644`.
The host rejects special permission bits.
The SDK encodes `sdk.File.Data` as base64 automatically.
The optional `next` array contains at most two suggested commands.
The host displays these commands without executing them.

Each finding has `status`, `message`, and an optional `try` instruction.
Use `ok`, `warn`, `fail`, or `skip` for the status.
The host combines findings into a doctor result.
A failed finding takes precedence over a warning.

The host enforces a 60-second limit for each call.
If the plugin exits without a response, the host reports the exit immediately.
The error retains a nonzero exit status.
Cancellation terminates the process group on Unix.
A response cannot exceed 16 MiB before its newline.
Invalid JSON, unexpected response IDs, and API mismatches cause errors.
Diagnostics receive a `[plugin <name>]` prefix on each line.
The diagnostic stream also has a 16 MiB limit.

The host runs the executable in the project root.
Its environment contains only existing `PATH`, `HOME`, `LANG`, and `TMPDIR` values, plus `SPINUP_PLUGIN_API=1`.
The host stages the verified executable in a private temporary directory.
It deletes this directory after the process exits.
The process runner replaces the inherited environment on every supported operating system.
On Windows, Go also supplies the required `SYSTEMROOT` variable.
On Windows, cancellation terminates the direct child process.

## Integration

`internal/plugin/rpc.ProjectGenerators` adds trusted exec generators beside declarative generators.
`internal/plugin/rpc.ProjectChecks` returns checks for a project.
`internal/plugin/rpc.RegisterProjectChecks` registers those checks through the doctor registry.
`spinup doctor` locates the project root and registers checks from trusted executable plugins.
It calls `describe` to discover checks, then calls `check` for each check alongside the other local checks.
Each report uses the ID `plugin/<name>/<check>` and includes findings and corrective instructions.
`--json` includes these reports, and `--offline` requires cached plugin bytes.
Projects without executable plugins start no plugin processes.
`spinup doctor --remote` runs only the remote checks.
The `plugin add` and `plugin update` commands accept `--trust-exec <name>`.

The fixture in `testdata/plugins/exec-hello/src/` demonstrates an SDK handler.
The host tests compile the fixture into a temporary `bin/` directory.
The repository contains no compiled plugin binaries.

## Checks

```console
$ spinup plugin --help
Manage trusted template pack plugins
...
$ spinup generate --help
Generate application files
...
```
