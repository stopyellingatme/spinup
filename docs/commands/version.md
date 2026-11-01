---
title: version — print the versions
parent: Command guides
---

# version — print the versions

`spinup version` prints the version of the spinup you run. Use it to check an install, or to add a version to a bug report.

```console
$ spinup version
...
```

The output is one line:

```text
spinup v1.2.3 (commit 8de7430a1c65d7768591016f57d1466d245cb65c, built 2026-11-01T18:09:19Z, go1.27.2)
```

The line shows the spinup version, the Git commit, the build date, and the Go version that built the binary.
A development build shows a version like `v0.0.0-20261101180919-8de7430a1c65`.

The version of the [kit](../reference/kit.md) library is always the same as the version of spinup.
The generated app records the version of the kit in `api/go.mod`. [`doctor`](doctor.md) warns when the two differ.

## Get JSON

Use `--json` when a script reads the version:

```console
$ spinup version --json | grep -o '"kit"'
"kit"
```

The object has the keys `version`, `commit`, `date`, `go`, and `kit`.

## Check for a newer release

`--check` asks GitHub for the newest stable release and says whether you are behind. It needs the network.

<!-- doctest: skip -->
```console
$ spinup version --check
```

To install the newer release, run [`spinup self-update`](self-update.md).

## Flags

| Flag | What it does | Example |
| --- | --- | --- |
| `--json` | Print the version as JSON. | `spinup version --json` |
| `--check` | Look for a newer stable release. | `spinup version --check` |

## See also

- [`self-update`](self-update.md) installs a newer release.
- [Install spinup](../get-started/install.md) shows how to install and update.
- Flag reference: [`version`](../site/commands/version.md)
