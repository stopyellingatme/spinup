# Release spinup

A CLI release and its kit tag identify the same commit.
Their major and minor versions must match when the kit changes.
Patch versions can differ for independent releases.
The tag workflow publishes matching versions for both modules.

## Prepare the signing key

Create the key pair once:

```sh
go run ./scripts/release/keygen
```

The command writes the private key to stdout.
It writes the public key to stderr.
Both values use base64 encoding.
The command does not create files.

Store the private key in the GitHub secret `SPINUP_TEMPLATES_SIGNING_KEY`.
Decode the public key and add its 32 bytes to `store.TrustedKeys`.
Commit this manual change before the release.
Never commit the private key.

Create the secret `HOMEBREW_TAP_TOKEN` with write access to `stopyellingatme/homebrew-tap`.
The workflow uses `GITHUB_TOKEN` for releases, attestations, the kit tag, and GHCR.
Stable releases update the Homebrew cask `spinup` under `Casks/`.
Snapshots and prereleases generate the cask without updating the tap.

## Bump the kit minor

The file `kit/api/minor.txt` holds the kit minor in development, for example `0.1`.
The kit API guard reads this file to decide if a deprecated API can go.
See `docs/policy/kit-compat.md`.

When you release a new minor, set the file to the next minor in the same commit.
Do not change the file for a patch release.
Then run `SPINUP_GOLDEN_UPDATE=1 go test ./internal/apicompat` and commit the result.

## Check a candidate

Move the release notes from `Unreleased` to a section such as `## [0.8.0-rc.1] - 2026-10-15`.
Keep an empty `Unreleased` section.
Include the breaking changes and the upgrade steps.
Commit the release notes before the check.

Run the checks from the repository root:

```sh
scripts/release/check.sh 0.8.0-rc.1
make build lint
make doctest
go test -race ./scripts/release/...
actionlint .github/workflows/release.yml
shellcheck install.sh scripts/release/*.sh
sh -n install.sh
```

The release check requires a clean tree, release notes, absent tags, and a trusted signing key.
Use `--snapshot` to omit the checks for the key.
Use `--kit-version X.Y.Z` to check an independent kit candidate.
The default kit candidate equals the CLI candidate.
The workflow uses `--tagged` because its CLI tag already exists.
This mode requires that tag to identify `HEAD`.
It accepts an existing kit tag only when that tag identifies the same commit.

Build a snapshot with cached Go modules and installed syft:

```sh
GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOPROXY=off goreleaser release --snapshot --clean --skip=publish,sign,docker
```

The `--skip=docker` option avoids image downloads during the offline check.
The other skip options make the snapshot policy explicit.
GoReleaser also disables signing and publication for snapshots.
Snapshots never create a template signature, even when the secret exists.

GoReleaser 2.18.3 uses `homebrew_casks` for the tap and `dockers_v2` for the image.
The image contains both Linux architectures under one version tag.
Snapshots disable the image build and push.
Run `goreleaser check` to validate the configuration without deprecated sections.

Inspect the artifacts under `dist/artifacts/` and the extra assets under `dist/extra/`.
The separate directories prevent the hook outputs from conflicting with GoReleaser initialization.
Remove `dist/` after the check.

Check the binary metadata:

```console
$ spinup version --json > version.json
$ test -s version.json
```

## Publish the candidate

Create and push the CLI tag after the checks succeed:

```sh
git tag v0.8.0-rc.1
git push origin refs/tags/v0.8.0-rc.1
```

The workflow builds binaries for Linux, macOS, and Windows on amd64 and arm64.
It publishes archives, SPDX SBOMs, SHA-256 checksums, and cosign bundles.
The bundles use GitHub OIDC with keyless signing.
The workflow attests the archives and publishes the CI image.
It then pushes `kit/v0.8.0-rc.1` on the same commit.
A failure before the final step leaves the kit tag unpublished.
Correct the failure before another attempt.

The repository currently has no root `LICENSE` file.
The archive uses the optional `LICENSE*` glob and tolerates its absence.
The maintainer decides which license to add.
The archives use `scripts/release/README.md` as their README.

The release includes `templates-v<version>.tar.zst` and its raw ed25519 `.sig` file.
The helper uses `store.WriteArchive` and validates the result with `store.ReadArchive`.
It includes `kit/MIGRATION.json` when that file exists.
Before each release, set its `version`, list the kit steps (`codemod`, `fix-inline`, or `manual`), and list new behavior versions in `behaviors`.
`spinup upgrade --plan` prints both.
The release also includes `compat.json`, `stack-versions.json`, and `install.sh`.
The minimum CLI in `compat.json` equals the release version.
The supported behavior version is `1`, as defined by the current kit configuration.

The CI image uses the image digests and tool versions from `stackversions.yaml`.
It checks the checksums for sops and age before installation.
It runs as a user without root privileges.
Review the image pins when the stack manifest changes.

## Install a release

Download the installer from a trusted release before execution.
Run the installer with an explicit version and destination:

```sh
sh install.sh --version 0.8.0-rc.1 --dir "$HOME/.local/bin"
```

Without `--version`, the installer resolves the latest stable GitHub release.
It verifies the archive checksum before extraction.
When cosign is available, it verifies the checksum bundle against this repository's release workflow.
It requires HTTPS for downloads and redirects.
It replaces the installed binary only after verification succeeds.
