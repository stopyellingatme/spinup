# example

This plugin provides the declarative `example-note <Name>` generator.
The host applies its plan and records ownership in `spinup.lock`.

This executable plugin also provides `example-hello <Name>` and `example-ready`.
Executable plugins run with your full user privileges.
The conformance kit detects protocol failures and tree changes during its probes.
It is a test suite, not a security sandbox.
Review all code before you grant trust.

Use Go 1.26 or later.
The module uses the SDK from `../../plugin-sdk` in this repository.
Keep this relative replacement when you test the example in this repository.

Run `make test` to build the plugin and run the conformance kit.
Run `make build` to build the binary declared in `plugin.toml`.
The host requires every declared binary to exist.
The plugin tree, including binaries, must fit within the host limit of 5 MiB.
The committed metadata selects `linux-amd64`.
For another platform, change the target in the Makefile and the matching entry in `plugin.toml`.
Do not commit binaries.
Distribute an archive that contains the declared binaries and these source files.

In a generated app, run `spinup plugin add path:/path/to/example --trust-exec example`.
Run `spinup g example-hello sample` to create `greetings/sample.txt`.

The executable implements JSON-RPC 2.0 with API 1 through the SDK.
Keep stdout for protocol responses.
Send diagnostics to stderr.
Return files from `Plan` without writing them.
Validate arguments before you construct paths.

Run `spinup g example-note sample` to create `notes/sample.txt`.
Edit `files/note.txt.tmpl` to change the declarative output.
