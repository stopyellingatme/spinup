# Update spinup

Run `spinup self-update` to install the newest stable release.
Use `--channel next` to include prereleases.
Use `--version 0.10.0` to select a specific release.
Development builds require `--version`.
This includes `dev` builds and versions with `-alpha.`.

Use `spinup self-update --check` to check without installing.
Use `spinup version --check` to check for a newer stable release.
The checks work with Homebrew and development binaries.
Use `--json` for a structured result.
Use `--dry-run` to check without installing.

```console
$ spinup self-update --check --offline
Error: Cannot check spinup releases
  offline mode disables downloads
  Try: remove --offline and unset SPINUP_OFFLINE
[exit 2]
$ spinup version --check --offline
Error: Cannot check spinup releases
  offline mode disables downloads
  Try: remove --offline and unset SPINUP_OFFLINE
[exit 2]
```

The updater downloads the archive and `checksums.txt` from the GitHub release.
It verifies the archive with SHA-256 before it extracts the binary.
If `cosign` is available, it verifies `checksums.txt.sigstore.json` against the release workflow identity and the GitHub Actions issuer.
A failed verification stops the update.
If `cosign` is absent, the updater prints a warning.
Use `--require-signature` to require `cosign`.

The updater writes the new binary beside the current binary with mode `0755`.
It synchronizes the file before it renames the file over the current binary.
On Windows, it first renames the current binary to `.old`.
If replacement fails, it restores the old binary.
If the directory is not writable, use permissions that permit replacement or reinstall spinup.

If Homebrew manages the binary, the updater prints `brew upgrade --cask spinup`.
Run that command to update the cask.
The updater does not replace Homebrew or development binaries.

The CLI checks for releases at most once per 24 hours during terminal use.
It stores the timestamp at `<user-cache>/spinup/update-check`.
The check uses one anonymous request to the GitHub releases API.
It sends no telemetry.
The notice appears on stderr.
The check stops after two seconds, and failures remain silent.

The notice is disabled without a terminal, with `CI` set, with `--json`, or with `--offline`.
Set `SPINUP_NO_UPDATE_CHECK=1` to disable the notice.
Explicit checks still work when this variable is set.
