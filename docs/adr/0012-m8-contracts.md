# ADR 0012: M8 contracts (CI generation and release)

Status: accepted (2026-10-15)

The M8 units run in parallel. These decisions fix the contracts between them.

## 1. Release assets (unit M8.3)

A release `v<version>` of the CLI repository publishes these assets. Other units depend on the names.

| Asset | Content |
|---|---|
| `spinup_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) | the binary, README, LICENSE, CHANGELOG |
| `checksums.txt` | SHA-256 of every asset, in `sha256sum` format |
| `checksums.txt.sigstore.json` | the cosign keyless bundle of `checksums.txt` |
| `templates-v<version>.tar.zst` and `templates-v<version>.tar.zst.sig` | the template archive (`internal/templates/store`) and its ed25519 signature |
| `compat.json` | `{ "cli": "<version>", "kit": "v<version>", "min_cli_for_templates": ..., "behavior_versions": [...] }` |
| `stack-versions.json` | `internal/stackversions/stackversions.yaml` as JSON |
| `install.sh` | the installer (§3) |
| `*.sbom.json` | SPDX SBOMs from syft |

`<version>` has no `v`. `<os>` is `linux`, `darwin`, or `windows`. `<arch>` is `amd64` or `arm64`.

## 2. Versions and signing

- The CLI and the kit share the minor version (ADR 0001). A release tags `v<version>` and `kit/v<version>` on the same commit. `scripts/release/check.sh` fails when the kit has changes since the last kit tag and the tags would differ in minor, or when `CHANGELOG.md` has no section for the version.
- Binaries and `checksums.txt` are signed with cosign keyless signing in GitHub Actions (OIDC). `actions/attest-build-provenance` adds a provenance attestation.
- The template archive is signed with ed25519, because the CLI verifies it offline with the standard library (`internal/templates/store`). The private key is the GitHub secret `SPINUP_TEMPLATES_SIGNING_KEY` (base64). The maintainer creates the key pair once with `go run ./scripts/release/keygen` and adds the public key to `store.TrustedKeys`. That is a manual step, not done by a unit: a release fails when `TrustedKeys` is empty.
- `goreleaser release --snapshot --clean` must pass offline. It skips signing and publishing.

## 3. Installing spinup

- `install.sh [--version X.Y.Z] [--dir DIR]` detects the OS and the architecture, downloads the archive and `checksums.txt` from the GitHub release, verifies the SHA-256, verifies the cosign bundle when `cosign` is on `PATH`, and installs the binary. It never pipes a download into a shell itself.
- Generated apps install spinup in CI with a managed composite action `.github/actions/setup-spinup/action.yml` (ci pack). It reads the CLI version from `spinup.lock` (`cli_version`), downloads the matching archive and `checksums.txt`, and verifies the SHA-256 before it puts the binary on `PATH`. It needs no token.
- Homebrew: a tap `stopyellingatme/homebrew-tap`, formula `spinup`, written by goreleaser.
- The CI image `ghcr.io/stopyellingatme/spinup-ci:<version>` (`Dockerfile.ci`) holds spinup, Go, Node, pnpm, sops, and age at the pinned versions.

## 4. Generated workflows (unit M8.1)

The ci pack renders these files (all `managed`, actions pinned by SHA from `stackversions.yaml` through `[[ uses ]]`, top-level `permissions: contents: read`, each job with a timeout):

| File | When | Jobs |
|---|---|---|
| `.github/workflows/ci.yml` | push to main, pull requests | `api` (exists), `web` (when the web pack is on: `pnpm install --frozen-lockfile`, `pnpm check`, `pnpm test`), `spinup` (`spinup check --allow-behind`, `spinup doctor`) |
| `.github/workflows/e2e.yml` | pull requests, when the web pack is on | Playwright against the API and the web app with a Postgres service |
| `.github/workflows/deploy.yml` | manual (`workflow_dispatch` with an environment input) and push to main when `ci_deploy_on_push` is true | `spinup deploy --env <env> --yes` with the CI age key and the SSH key from secrets, in a GitHub environment of the same name |
| `.github/workflows/backup-verify.yml` | weekly, when the backups pack is on | `spinup backup verify --env <default env>` |
| `.github/workflows/upgrade-check.yml` | weekly | `spinup upgrade --yes --non-interactive` against the newest release; opens a pull request with `peter-evans/create-pull-request` when files change |

Secrets that the workflows read: `SPINUP_AGE_KEY` (the CI recipient of ADR 0009 §5), `SPINUP_SSH_KEY`, `SPINUP_SSH_KNOWN_HOSTS`. Only `deploy.yml` and `backup-verify.yml` read them, and only in their environment.

## 5. `spinup ci` (unit M8.2)

- `spinup ci generate` re-renders the ci pack's files from the lock answers into the project (a managed-file write through the normal plan and apply path, with `--dry-run` and `--diff`).
- `spinup ci verify` checks `.github/workflows/*.yml` and `.github/actions/*/action.yml`: every `uses:` is pinned by a 40-character SHA (local `./` actions excepted), every workflow sets top-level `permissions`, no job uses `pull_request_target` with a checkout of the head, and `actionlint` passes when it is on `PATH`. It exits 1 with one line per finding.
