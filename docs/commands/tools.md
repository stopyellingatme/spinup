# Install host tools

`spinup tools install` downloads the pinned `sops` and `age` releases.
It verifies the embedded SHA-256 digest before it installs any binary.

```console
$ spinup tools install --offline
Error: cannot install tools: offline mode disables downloads
  Try: remove --offline and retry
[exit 1]
```

Use `spinup tools install sops` or `spinup tools install age` for one tool.
Use `--force` to replace a tool that reports the pinned version.
The command stores binaries in `<user data dir>/spinup/bin`.
Set `SPINUP_TOOLS_DIR` to use another directory.

`spinup tools list` prints each pinned version and its installed version.
It also prints the managed path and the first matching `PATH` entry.
`missing` means the managed binary does not exist or cannot report a version.
Use `--json` for structured output.
