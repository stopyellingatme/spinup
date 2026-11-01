---
title: tools — install sops, age, and tofu
parent: Command guides
---

# tools — install sops, age, and tofu

`spinup tools` installs the helper programs that some spinup commands need: `sops` and `age` for [secrets](../deploy/secrets.md), and `tofu` (OpenTofu) for [infrastructure](../infra.md).
The command downloads the exact release that your spinup version expects. It checks the SHA-256 checksum of each file before it installs it.

<!-- doctest: skip -->
```console
$ spinup tools install
sops	3.13.3	installed	/home/you/.config/spinup/bin/sops
age	1.3.2	installed	/home/you/.config/spinup/bin/age
tofu	1.13.1	installed	/home/you/.config/spinup/bin/tofu
```

You do not need the command if you already have these programs. It exists so that every machine uses the same, checked versions.

## Install one tool

Name the tools to install. The names are `sops`, `age`, and `tofu`:

<!-- doctest: skip -->
```console
$ spinup tools install sops
sops	3.13.3	installed	/home/you/.config/spinup/bin/sops
```

If a tool already reports the pinned version, the command prints `current` and does not download it again.
Add `--force` to download it anyway:

<!-- doctest: skip -->
```console
$ spinup tools install sops --force
```

## List the tools

`spinup tools list` shows each pinned version, the version spinup installed, and the program that your shell finds first:

```console
$ spinup tools list
...
```

A row looks like this, with tabs between the columns:

```text
sops	pinned 3.13.3	installed 3.13.3	/home/you/.config/spinup/bin/sops	PATH /usr/bin/sops
```

Each row has the tool, the pinned version, the installed version, the spinup path, and the first match on your `PATH`.
`installed missing` means spinup has not installed the tool, or it cannot read its version.
Use `--json` for a script.

## Choose the install folder

By default, the binaries go to a `spinup/bin` folder in your user config folder (`~/.config` on Linux, `~/Library/Application Support` on macOS).
Set `SPINUP_TOOLS_DIR` to use another folder:

<!-- doctest: skip -->
```console
$ SPINUP_TOOLS_DIR=$HOME/bin spinup tools install
```

spinup runs the tools from this folder, and it does not change your `PATH`.

## Install on a machine without a network

`--offline` refuses to download. It stops with an error:

```console
$ spinup tools install --offline
Error: cannot install tools: offline mode disables downloads
  Try: remove --offline and retry
[exit 1]
```

## Flags

| Flag | What it does | Example |
| --- | --- | --- |
| `install [sops\|age\|tofu...]` | Install these tools. With no names, install all three. | `spinup tools install age` |
| `--force` | Download a tool again, also when the version is current. | `spinup tools install --force` |
| `list` | Show the pinned and installed versions. | `spinup tools list` |
| `--json` | Print the result as JSON. | `spinup tools list --json` |
| `--offline` | Refuse to download. | `spinup tools install --offline` |

## See also

- [Secrets](../deploy/secrets.md) uses `sops` and `age`.
- [Hetzner Cloud with OpenTofu](../infra.md) uses `tofu`.
- [`doctor`](doctor.md) checks the other tools on your machine.
- Flag reference: [`tools install`](../site/commands/tools-install.md), [`tools list`](../site/commands/tools-list.md)
