# Create a plugin

Run `spinup plugin new <name>` to create a declarative plugin.
The default destination is `./<name>`.
Use `--out <directory>` to select another destination.
The command accepts an empty directory and refuses a non-empty directory.
Use `--dry-run` to validate the scaffold without writing files.

```console
$ spinup plugin new paper
...
$ spinup plugin new paper
...
[exit 1]
$ spinup plugin new greetings --kind exec --dry-run
...
$ spinup plugin new greetings --kind exec
...
$ spinup plugin new invalid --kind unknown
...
[exit 1]
```

Each scaffold contains `plugin.toml`, `pack.toml`, `files/`, a declarative generator, and a README.
The declarative generator is `<name>-note <Name>`.
It creates `notes/<Name>.txt`.

Use `--kind exec` to include a Go module, an executable generator, a check, and a conformance test.
The executable generator is `<name>-hello <Name>`.
It creates `greetings/<Name>.txt`.
The check is `<name>-ready`.
The module requires Go 1.26 or later.

For a local checkout, supply `--sdk-path /path/to/spinup/plugin-sdk`.
This adds a local SDK replacement to the generated module.
Otherwise, replace the SDK placeholder version with a published version before building.
Run `make build` in the scaffold to build the declared binary.
Run `make test` to build a temporary binary and run the conformance kit.
The Makefile disables workspace mode for the separate module.

The default executable target matches the current platform.
Use `--platform linux-amd64` to select a different target.
Each binary resides at `bin/<name>_<os>_<arch>`.
Every binary declared in `plugin.toml` must exist before installation.
The complete plugin tree must fit within 5 MiB.
Do not commit binaries.
Include binaries in the archive that you distribute.

Executable plugins run with your full user privileges.
Review the source and binaries before you grant trust.
In a generated app, run `spinup plugin add path:/path/to/plugin --trust-exec <name>`.
For a declarative plugin, use `--trust`.
Run `spinup g <name>-hello sample` for the executable example.
Run `spinup g <name>-note sample` for the declarative example.

The SDK exports `conformance.Run(t, binary)`.
It checks the handshake, descriptions, findings, error responses, response limits, and cancellation.
It compares the bytes from two identical plan calls.
It rejects unsafe output paths, invalid classes, invalid modes, and duplicate paths.
It compares tree hashes around each protocol call.
The tree includes the working directory and its temporary parent.

The probes replace each positional argument with traversal strings and a one MiB string.
A plugin can reject these arguments or return a safe plan.
Use `conformance.TraversalArguments()` and `conformance.HugeArgument()` in additional tests.
Use `conformance.RunWithOptions` to supply valid arguments or project answers for a specific generator.
The default argument is `sample`.
The default process timeout is five seconds.
The cancellation probe stops a process that waits for a complete request.

The kit detects failures in its probes.
It cannot prove that a plugin is safe for every input.
It does not restrict the privileges of the plugin process.
Run untrusted binaries only in an isolated environment that you control.
