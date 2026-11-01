---
title: Install spinup
parent: Get started
nav_order: 1
---

# Install spinup

Install the `spinup` binary, then check that your machine has the tools that a generated app needs.
You need one command and about a minute.

```sh
curl -fsSL https://github.com/stopyellingatme/spinup/releases/latest/download/install.sh | sh
spinup version
```

The script puts `spinup` in `~/.local/bin`.
If `spinup version` says "command not found", add that folder to your `PATH`:

```sh
export PATH="$HOME/.local/bin:$PATH"   # add this line to ~/.zshrc or ~/.bashrc
```

## Install the tools first

spinup runs these tools on your machine. `spinup doctor` checks them for you.

| Tool | Version | Why you need it |
| --- | --- | --- |
| Go | 1.26 or later | Builds and runs the API |
| Node | 24 or later | Builds and runs the web app |
| pnpm | 12, through corepack | Installs the web packages |
| Docker | 27 or later | Runs PostgreSQL and Mailpit in development, and builds images for a deploy |
| git | 2.44 or later | Tracks your app, and `spinup module add` and `spinup upgrade` need it |

Node 24 includes corepack. Run `corepack enable` once, and `pnpm` works at the version that your app pins.
You do not install the Go tools of the app (`sqlc`, `goose`, `oapi-codegen`, `air`) yourself. The app pins them in `api/tools.mod`, and `go tool` runs them.

{: .note }
You build only an API? Then you do not need Node or pnpm. Create the app with `--api-only`.

## Install with the script

The script downloads the release for your system, checks its SHA-256 sum, and installs the binary.
It checks the signature too when `cosign` is on your `PATH`.
It does not run the archive that it downloads.

```sh
# the newest release, into ~/.local/bin
curl -fsSL https://github.com/stopyellingatme/spinup/releases/latest/download/install.sh | sh

# a chosen version, into a chosen folder (replace X.Y.Z with a release number)
curl -fsSL https://github.com/stopyellingatme/spinup/releases/latest/download/install.sh | sh -s -- --version X.Y.Z --dir /usr/local/bin
```

| Option | Meaning | Default |
| --- | --- | --- |
| `--version X.Y.Z` | The release to install | The latest release |
| `--dir DIR` | The folder for the binary | `~/.local/bin` |

You prefer to read the script before you run it? Download `install.sh` from the [releases page](https://github.com/stopyellingatme/spinup/releases), read it, and run `sh install.sh`.
The script needs `curl`, `tar`, and `sha256sum` or `shasum`.
Find all releases and their `install.sh` on the [releases page](https://github.com/stopyellingatme/spinup/releases).

## Install with Homebrew

Homebrew installs spinup as a cask from the `stopyellingatme/tap` tap:

```sh
brew install --cask stopyellingatme/tap/spinup
```

## Check the install

`spinup version` prints the version of the binary.
`spinup doctor` checks your tools. Run it in an app folder to check all of them.
Outside an app, the Node and pnpm checks print `skip`, because only an app with a web part needs them.

<!-- doctest: skip -->
```console
$ spinup version
spinup v0.9.0 (commit 1a2b3c4, built 2026-11-01T12:00:00Z, go1.27.2)
$ spinup doctor
ok	docker	Docker daemon is available
ok	go-version	go version go1.27.2 linux/amd64
ok	node-version	v24.21.0
ok	pnpm	pnpm 12.11.2
...
```

Each line starts with `ok`, `warn`, `fail`, or `skip`.
Fix each `fail` line before you run `spinup dev`. A `skip` line means that the check does not apply here, for example because you are outside an app.
See [Doctor checks](../reference/doctor-checks.md) for what each check does.

## Update

`spinup self-update` downloads the newest stable release, checks it, and replaces the binary.

<!-- doctest: skip -->
```console
$ spinup self-update --check
$ spinup self-update
```

| Flag | Effect |
| --- | --- |
| `--check` | Report a newer release and do not install it |
| `--version X.Y.Z` | Install a chosen release |
| `--channel next` | Include prereleases |
| `--require-signature` | Fail unless `cosign` can verify the release signature |

If you installed with Homebrew, `spinup self-update` does not replace the binary. It prints `brew upgrade --cask spinup`, and you run that command.
spinup also checks for a new release at most once a day, and only in a terminal. It sends one anonymous request to GitHub and no telemetry.
Set `SPINUP_NO_UPDATE_CHECK=1` to turn the check off.

Updating spinup does not change your app. To bring new templates into an app, read [Upgrade](../upgrading.md).

## Uninstall

Remove the binary. Your apps keep working, because a generated app does not need spinup to run.

```sh
rm ~/.local/bin/spinup            # installed with the script
brew uninstall --cask spinup      # installed with Homebrew
```

spinup keeps a template cache and its downloaded tools in your user cache and data folders.
Run `spinup cache --help` to see the cache commands.

## Windows

`spinup new`, `spinup generate`, `spinup build`, and `spinup upgrade --plan` run natively on Windows.
`spinup dev`, `spinup deploy`, and `spinup server` need WSL2, because they use Linux processes and tools.
The simplest way is to install spinup and all the tools inside a WSL2 distribution and work there.
Read the [Windows policy](../policy/windows.md) for the details.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `spinup: command not found` | The install folder is not on your `PATH` | Add `~/.local/bin` to `PATH`, or use `--dir` with a folder that is on it |
| The script says `cannot install spinup` | A tool, the network, or the version is wrong | Read the line that starts with `Try:` |
| `doctor` says pnpm is missing | corepack is off | Run `corepack enable` |
| `doctor` says Docker is not available | The Docker daemon is not running | Start Docker Desktop or the Docker service |

## See also

- [Your first app](first-app.md)
- [spinup self-update](../commands/self-update.md)
- [spinup doctor](../commands/doctor.md)
- [Windows policy](../policy/windows.md)
