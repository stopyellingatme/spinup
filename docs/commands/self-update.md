---
title: self-update — update spinup
parent: Command guides
---

# self-update — update spinup

`spinup self-update` downloads the newest stable release of spinup, checks it, and replaces the binary you run.
Use it to get new commands and fixes.
To move an app to new templates, use [upgrade](upgrade.md) after you update.

<!-- doctest: skip -->
```console
$ spinup self-update
Updated spinup to 0.10.0
```

## Check without installing

`--check` asks whether a newer release exists and installs nothing.
It also works for Homebrew and development binaries.
`spinup version --check` gives the same answer for stable releases.

<!-- doctest: skip -->
```console
$ spinup self-update --check
spinup 0.10.0 is available (current 0.9.0)
$ spinup self-update --check
spinup 0.10.0 is up to date
$ spinup self-update --check --json
{"current":"0.9.0","version":"0.10.0","available":true,"updated":false}
```

Add `--json` for a structured result, as shown above.
Add `--dry-run` to see what the command would do.

```console
$ spinup self-update --check --offline
Error: Cannot check spinup releases
  offline mode disables downloads
  Try: remove --offline and unset SPINUP_OFFLINE
[exit 2]
```

## Choose a version or channel

| Flag | What it does | Example |
| --- | --- | --- |
| `--version <version>` | Install one release. | `spinup self-update --version 0.10.0` |
| `--channel next` | Include prereleases. The default channel is `stable`. | `spinup self-update --channel next` |
| `--require-signature` | Fail unless `cosign` can verify the release. | `spinup self-update --require-signature` |

A development build (`dev`, or a version with `-alpha.`) must pass `--version`.

## Homebrew and development builds

If Homebrew installed spinup, the command does not replace the binary.
It prints `brew upgrade --cask spinup`.
Run that command instead.

## How the download is verified

1. The command downloads the archive and `checksums.txt` from the GitHub release.
2. It checks the archive against the SHA-256 checksum before it unpacks the binary.
3. If `cosign` is installed, it verifies the signature of `checksums.txt` against the release workflow.
4. If `cosign` is missing, it prints a warning. `--require-signature` turns the warning into an error.

A failed check stops the update and leaves your binary as it was.
The new binary is written next to the old one, then renamed over it.
If the rename fails, the command restores the old binary.
If the directory is not writable, change its permissions or reinstall spinup.

## Update notices

During normal terminal use, spinup checks for a new release at most once every 24 hours.
It sends one anonymous request to the GitHub releases API and no telemetry.
The notice appears on stderr, and a failed check stays silent.

There is no notice without a terminal, with `CI` set, with `--json`, or with `--offline`.
To turn the notice off, set `SPINUP_NO_UPDATE_CHECK=1`.
An explicit `--check` still works.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `offline mode disables downloads` | Remove `--offline` and unset `SPINUP_OFFLINE`. |
| A development build asks for a version | Pass `--version <version>`. |
| `cosign` warning | Install `cosign`, or pass `--require-signature` to make it an error. |
| Permission denied on replace | Reinstall spinup in a directory you can write to. |

## See also

- [upgrade — move an app to a new version](upgrade.md)
- [Install spinup](../get-started/install.md)
- [Flag reference: self-update](../site/commands/self-update.md)
