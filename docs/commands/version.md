# Version

`spinup version` prints the version of the spinup CLI.
The kit version is equal to the CLI version.

```console
$ spinup version --json
{
...
}
```

The text output starts with `spinup <version>`.
It also shows the commit, the build date, and the toolchain of the build.
A development build shows the version `dev`.
Use `--json` to receive the build information as JSON.
The JSON object has the keys `version`, `commit`, `date`, and `kit`.
It also has one key for the toolchain.

Use `spinup version --check` to check for a newer stable release.
Offline mode rejects the check with exit code 2.

A command that spinup does not know fails with exit code 2.
The error tells you the next step.

```console
$ spinup nope
Error: Invalid usage
...
  Try: spinup --help
[exit 2]
```

`spinup completion <shell>` writes a shell completion script.
The shells include `bash`, `fish`, `powershell`, and `zsh`.
