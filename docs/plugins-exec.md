---
title: Executable plugins
parent: Build your app
nav_order: 16
---

# Executable plugins

An executable plugin is a program that adds generators and `spinup doctor` checks to your app.
Use one when a [plugin pack](plugins.md) is not enough, for example when a generator must read your code to decide which files to write.
You write the program in Go with the plugin SDK.

{: .warning }
An executable plugin runs with the full privileges of your user account. It can read your files and run programs. spinup gives it no sandbox. Read the source and build the binary yourself before you trust it.

Scaffold a plugin, build it, trust it, and run its generator:

<!-- doctest: skip -->
```console
$ spinup plugin new hello --kind exec
$ cd hello && make build && cd ..
$ cd todo
$ spinup plugin add path:../hello --trust-exec hello
$ spinup g hello-hello Ada
Created 1, updated 0 files
$ cat greetings/Ada.txt
Hello, Ada!
```

A dry run of the scaffold shows what you get, and needs no Go build:

```console
$ spinup plugin new hello --kind exec --dry-run
...
```

## Scaffold a plugin

`spinup plugin new <name> --kind exec` writes a Go module, a generator, a check, a test, and a Makefile.

| Option | Effect | Example |
| --- | --- | --- |
| `--out` | Choose the folder. The default is `./<name>`. | `--out plugins/hello` |
| `--platform` | Choose the target as `GOOS-GOARCH`. The default is your platform. | `--platform linux-amd64` |
| `--sdk-path` | Use a local copy of `plugin-sdk`. | `--sdk-path ~/code/spinup/plugin-sdk` |

The scaffold also has the declarative generator `hello-note`, as in a [plugin pack](plugins.md#write-your-own-plugin).
Without `--sdk-path`, the `go.mod` file holds a placeholder version of the SDK.
Replace it with a published version before you build.
The module needs Go 1.27 or later.
See [Create a plugin](commands/plugin-new.md) for every flag.

## Write the handler

The plugin implements four methods and calls `sdk.Serve`.
This is the scaffold, shortened to the parts you change:

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

type plugin struct{}

// Handshake says who the plugin is.
func (plugin) Handshake(context.Context) (sdk.HandshakeResult, error) {
	return sdk.HandshakeResult{Name: "hello", Version: "0.1.0", API: sdk.API,
		Capabilities: []string{sdk.CapabilityGenerator, sdk.CapabilityCheck}}, nil
}

// Describe lists the generators and checks.
func (plugin) Describe(context.Context) (sdk.DescribeResult, error) {
	return sdk.DescribeResult{
		Generators: []sdk.Generator{{Name: "hello-hello", Short: "Create a greeting", Usage: "hello-hello <Name>", Args: []string{"Name"}}},
		Checks:     []sdk.Check{{Name: "hello-ready", Title: "Check plugin readiness"}},
	}, nil
}

// Plan returns the files to write. It does not write them.
func (plugin) Plan(_ context.Context, p sdk.PlanParams) (sdk.PlanResult, error) {
	name := p.Args[0] // check the arguments first
	return sdk.PlanResult{Files: []sdk.File{{
		Path: "greetings/" + name + ".txt", Data: []byte("Hello, " + name + "!\n"), Class: "scaffold", Mode: 0644,
	}}}, nil
}

// Check reports findings for a doctor check.
func (plugin) Check(_ context.Context, p sdk.CheckParams) (sdk.CheckResult, error) {
	return sdk.CheckResult{Findings: []sdk.Finding{{Status: "ok", Message: "Plugin is ready"}}}, nil
}

func main() {
	if err := sdk.Serve(plugin{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

Rules for a handler:

- Check every argument before you build a path. The host also rejects unsafe paths.
- Return files from `Plan` and write nothing. The host applies the plan.
- Write diagnostics to standard error. Keep standard output for the protocol.
- Keep no state between calls. The host starts a new process for each call.

## The plugin.toml file

```toml
name = "hello"
version = "0.1.0"
api = 1
spinup = ">=0.0.0"
kind = "exec"

[exec]
binaries = { "darwin-arm64" = "bin/hello_darwin_arm64", "linux-amd64" = "bin/hello_linux_amd64" }
```

- `kind = "exec"` marks the plugin as executable.
- Each binary lives in `bin/`. The keys are Go platform names such as `linux-amd64`. A missing binary for your platform gives an error with a `Try:` line.
- Keep `pack.toml` in the tree, even when the plugin has no templates.
- The approved hash covers the bytes and the permissions of every binary.
- The whole plugin tree, binaries included, must be 5 MiB or smaller.
- Do not commit binaries. Give users an archive that holds the binaries and the source.

## Build, trust, and run

Run `make build` in the plugin folder. Build the other platforms with `make build GOOS=linux GOARCH=amd64`.
Then trust the plugin by name, in your app:

<!-- doctest: skip -->
```console
$ spinup plugin add path:../hello --trust-exec hello
```

At a terminal you can omit the flag. spinup shows the source and hash, and asks you to type the plugin name.
The `--trust` flag cannot approve an executable plugin.
`spinup plugin update <name> --trust-exec <name>` approves a new version in the same way.
The line in `.spinup/plugins.allow` ends with ` exec`. A line without that word cannot approve execution.
See [Plugins](plugins.md#add-a-plugin-to-an-app) for the files to commit, and [Plugin trust](policy/plugins.md) for the policy.

An executable generator behaves like any other:

- `--dry-run` lists the files, `--diff` shows them, and `--force` replaces a file that differs.
- The host rejects protected files, path escapes, duplicate paths, and unsafe permissions.
- The lock records each file under `plugin/<name>/@exec/<generator>`. `spinup upgrade` keeps these files and your edits.

## Add a doctor check

A plugin check runs with `spinup doctor`. Return findings from the `Check` method:

```go
return sdk.CheckResult{Findings: []sdk.Finding{
	{Status: "warn", Message: "No LICENSE file", Try: "add a LICENSE file"},
}}, nil
```

The status is `ok`, `warn`, `fail`, or `skip`. A failed finding wins over a warning.
Each check shows up with the id `plugin/<plugin>/<check>`:

<!-- doctest: skip -->
```console
$ spinup doctor
...
ok	plugin/hello/hello-ready	Plugin is ready
```

`spinup doctor --json` includes plugin checks.
An app with no executable plugins starts no plugin process.
`spinup doctor --remote` runs only the remote checks, not plugin checks.

## Reference: the protocol

The host talks to the plugin with JSON-RPC 2.0. The Go SDK hides this, but it helps when you debug.
Each request and response is one JSON object on one line, on standard input and output.
You can try it by hand:

<!-- doctest: skip -->
```console
$ echo '{"jsonrpc":"2.0","id":1,"method":"handshake"}' | ./bin/hello_darwin_arm64
{"jsonrpc":"2.0","id":1,"result":{"name":"hello","version":"0.1.0","api":1,"capabilities":["generator","check"]}}
```

| Method | Parameters | Result |
| --- | --- | --- |
| `handshake` | none | `name`, `version`, `api`, `capabilities` |
| `describe` | none | `generators`, `checks` |
| `plan` | `generator`, `args`, optional `answers` | `files`, optional `next` |
| `check` | `check` | `findings` |

- The host sends a `handshake` before each call. It must return the approved name, version, and API 1. The capabilities are `generator` and `check`. Any other capability is an error.
- A generator has `name`, `short`, `usage`, and `args`. A check has `name` and `title`.
- A file has `path`, `data_base64`, `class`, and `mode`. The SDK encodes `Data` as base64 for you.
- `class` is `managed`, `scaffold`, or `seed`. `mode` is a number: `420` is `0644`. A zero mode means `0644`. Special permission bits are rejected.
- `next` holds up to two commands that spinup shows to the user. It does not run them.

A `plan` call looks like this:

```json
{"jsonrpc":"2.0","id":2,"method":"plan","params":{"generator":"hello-hello","args":["Ada"]}}
{"jsonrpc":"2.0","id":2,"result":{"files":[{"path":"greetings/Ada.txt","data_base64":"SGVsbG8sIEFkYSEK","class":"scaffold","mode":420}]}}
```

### Limits

| Limit | Value |
| --- | --- |
| Time for one call | 60 seconds |
| One message, before its newline | 16 MiB |
| Diagnostic output | 16 MiB |
| Plugin tree | 5 MiB |

If the plugin exits without an answer, the host reports the exit and the exit status at once.
Invalid JSON, a wrong response id, or a wrong API number is an error.
Diagnostics get a `[plugin <name>]` prefix on each line.

The host runs the program in the project root with a clean environment: `PATH`, `HOME`, `LANG`, and `TMPDIR` if they exist, and `SPINUP_PLUGIN_API=1`.
On Windows, `SYSTEMROOT` is added as well.
It copies the verified binary to a private temporary folder and deletes it after the call.
When a call is cancelled, the host stops the whole process group on Unix and the direct child on Windows.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `--trust` is refused | `--trust` cannot approve an executable plugin. | Use `--trust-exec <name>`. |
| A missing binary error | `plugin.toml` lists a binary that is not built. | Run `make build` for that platform. |
| A protocol error with strange output | The plugin printed to standard output. | Print to standard error instead. |
| The call times out | The handler runs longer than 60 seconds. | Do less work in `Plan` or `Check`. |

## See also

- [Plugins](plugins.md) for packs, trust files, and upgrades
- [Plugin trust](policy/plugins.md)
- [Create a plugin](commands/plugin-new.md) and the [flag reference](site/commands/plugin-new.md)
- [Doctor](commands/doctor.md)
