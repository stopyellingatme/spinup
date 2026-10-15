# Support policy

This page states what spinup supports.
It is the published support policy for the v1.0 release.
The numbers come from `internal/stackversions/stackversions.yaml`, `internal/compat`, and the template store.
If a number here and the code disagree, the code is correct. Report the page as a bug.

## Supported versions of spinup

spinup supports the last two minor versions of the CLI and the kit.
For example, when 0.5 is the newest minor, 0.5 and 0.4 are supported.
A security fix goes to both minors.
Other fixes go to the newest minor.
`SECURITY.md` says how to report a vulnerability.

## Release cadence

- A minor version ships each month.
- A patch version ships when a fix is ready.
- `CHANGELOG.md` has a "Breaking" section and an "Upgrade steps" section for each release.
- One tag publishes the CLI as `vX.Y.Z` and the kit as `kit/vX.Y.Z`.

## CLI and kit move together

The CLI and the kit must have the same major and minor version.
Patch versions can differ.
`spinup doctor` fails when the major or minor versions differ.
To fix this, run `spinup upgrade`, or run `go get github.com/stopyellingatme/spinup/kit@vX.Y.x`.
The kit `go.mod` states minimum dependency versions, so your app controls `go get -u`.

## Kit stability and deprecation

Until v1.0, the kit stays on v0.x.
The v1.0 release needs two minors with no breaking kit change.
After v1.0, a breaking change needs a new major version and a codemod.

The deprecation window is one minor.
A deprecated kit API stays for one minor, with a `// Deprecated:` comment and `//go:fix inline`.
The app logs one startup warning for a deprecated path.
The next minor can remove the API.

A new default behavior never changes an existing app.
See `docs/policy/behavior-version.md`.

## Toolchains and services

| Item | Supported |
| --- | --- |
| Go | 1.26 and 1.27. Generated apps declare `go 1.26.0`. CI tests both. |
| Node | 24. The generated image uses `node:24-bookworm-slim`. |
| pnpm | The pinned version, through corepack. Bun is an option for the web app. |
| PostgreSQL | 16, 17, and 18 for new apps (`db_version`). 18 is the default. |
| PostgreSQL checks | `spinup db doctor` accepts 14 to 18. It warns that 14 ends on 2026-11-12. It fails for 13 and older. |
| Operating system | Linux and macOS (amd64 and arm64). Windows runs only some commands natively, and `dev`, `deploy`, and `server` need WSL2. See `docs/policy/windows.md`. |

The same file pins the images and the tool versions.
Run `spinup docs compat` to print the compatibility matrix for your CLI.

## How long a template version stays upgradable

The CLI embeds the template archives of the last three releases.
`spinup upgrade` can also load an older archive from the signed release assets.
This needs network access, or a warm cache from `spinup cache warm`.
With `--offline`, only the embedded and cached archives are available.

If your app is more than one minor behind, `spinup upgrade` chains the steps.
It uses the latest patch of each minor between your version and the target.
It makes one commit for each step.
If the archive of an intervening minor is not loadable, the upgrade stops before it changes a file.

The upgrade matrix tests the frozen apps of each release in `testdata/corpus/`.
It also tests a chain from the oldest frozen release.
An app on a release with a frozen corpus can upgrade to the newest release.
For an older app, upgrade to the oldest supported minor first.

The CLI does not downgrade an app.
A template version needs a CLI at least as new as its `min_cli` value.

## Plugins

Plugins have their own API version.
See `docs/policy/plugins.md` for the trust rules.
Tier-2 plugins run with the full privileges of the user.
