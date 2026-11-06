---
title: plugin new — scaffold a plugin
parent: Command guides
---

# plugin new — scaffold a plugin

`spinup plugin new <name>` creates a ready-to-edit plugin directory.
Use it when you want to share your own generator or template files across apps.

```console
$ spinup plugin new paper
...
```

The new directory `./paper` is a working pack plugin with one generator, `paper-note <Name>`.
To install it in an app, see [plugin add](plugin.md).

## Create a pack plugin

A pack plugin holds templates and declarative generators.
It has no program, so spinup runs none of its code.

```console
$ cd paper
$ ls -A
README.md
files
pack.toml
plugin.toml
$ cd ..
```

| File | What it holds |
| --- | --- |
| `plugin.toml` | The name, version, and generators |
| `pack.toml` | The file classes of the template files |
| `files/` | The templates, such as `files/note.txt.tmpl` |
| `README.md` | Notes for the plugin author |

The generator `paper-note <Name>` writes `notes/<Name>.txt` into an app.

The default destination is `./<name>`.
The command accepts an empty directory and refuses a directory with files.
Use `--out <directory>` to choose another place.
Use `--dry-run` to check the plan without writing files.

Run it twice and the second run fails, because `./paper` has files now.
The dry run shows the plan for another directory and writes nothing.

```console
$ spinup plugin new paper
...
[exit 1]
$ spinup plugin new notes --out plugins/notes --dry-run
...
```

## Create an executable plugin

Use `--kind exec` when you need code, such as a check or a generator that makes decisions.
The scaffold adds a Go module with `main.go`, `main_test.go`, a `Makefile`, and a `.gitignore`.

```console
$ spinup plugin new greetings --kind exec --dry-run
...
```

The executable plugin has these parts:

| Part | Name |
| --- | --- |
| Declarative generator | `greetings-note <Name>`, writes `notes/<Name>.txt` |
| Executable generator | `greetings-hello <Name>`, writes `greetings/<Name>.txt` |
| Check | `greetings-ready` |

{: .warning }
An executable plugin runs with your full user privileges. Read the code before you trust a plugin, including your own.

The module needs Go 1.27 or later.
Two flags change how the module is set up:

| Flag | What it does | Example |
| --- | --- | --- |
| `--sdk-path <dir>` | Use a local checkout of `plugin-sdk` instead of a published version. | `spinup plugin new greetings --kind exec --sdk-path ~/code/spinup/plugin-sdk` |
| `--platform <os-arch>` | Name the target in `plugin.toml`. The default is your platform. | `spinup plugin new greetings --kind exec --platform linux-amd64` |

Without `--sdk-path`, edit `go.mod` and replace the placeholder SDK version with a published one before you build.

## Build and test

Run these in the plugin directory:

<!-- doctest: skip -->
```console
$ make build
$ make test
```

`make build` writes the program to `bin/<name>_<os>_<arch>`.
`make test` builds a temporary program and runs the conformance kit from the SDK.
The kit checks that the plugin answers the protocol correctly and returns safe, repeatable plans.
It cannot prove that a plugin is safe, and it does not restrict what the program can do.

### Test with your own arguments

The kit calls each generator with the argument `sample`, and it also sends traversal strings and a 1 MiB string.
A plugin can reject these arguments or return a safe plan.
If a generator needs valid arguments, pass them in `main_test.go`.
Use the `Answers` field of `conformance.Options` for project answers.

```go
conformance.RunWithOptions(t, binary, conformance.Options{
	Args:    map[string][]string{"greetings-hello": {"world"}},
	Timeout: 10 * time.Second, // the default is 5 seconds
})
```

Each probe also checks the handshake, error responses, response limits, and cancellation.
It compares two identical plan calls byte for byte.
It rejects unsafe output paths, invalid classes, invalid modes, and duplicate paths.

## Install in an app

In an app, add the plugin from its directory.
A pack plugin needs `--trust`.
An executable plugin needs `--trust-exec <name>`.

<!-- doctest: skip -->
```console
$ spinup plugin add path:../paper --trust
$ spinup plugin add path:../greetings --trust-exec greetings
$ spinup g greetings-hello sample
```

## Share a plugin

1. Build the program for each platform that you support.
2. Put the source files and the built binaries in one archive. Every binary in `plugin.toml` must exist.
3. Keep the whole plugin tree under 5 MiB.

Do not commit the binaries to Git, because they are build output.
The `.gitignore` in the scaffold keeps `bin/` out.
Add the built binaries only to the archive that you distribute.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| The command refuses the directory | The directory is not empty. Use another `--out`. |
| `go build` cannot find the SDK | Pass `--sdk-path`, or set a published SDK version in `go.mod`. |
| `plugin add` says a binary is missing | Run `make build`, or build for the platform in `plugin.toml`. |
| The plugin is over the size limit | Remove files and keep the tree under 5 MiB. |

## See also

- [plugin — add and manage plugins](plugin.md)
- [Plugins](../plugins.md)
- [Executable plugins](../plugins-exec.md)
- [Flag reference: plugin new](../site/commands/plugin-new.md)
