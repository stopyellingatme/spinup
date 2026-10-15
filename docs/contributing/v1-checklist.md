# v1.0 checklist

The v1.0 gate is in `docs/design/architecture.md` section 21.
This page lists each item, how to check it, and its status on 2026-10-15.
Update the status column in the same pull request that changes a status.

## The four gate items

| Gate item | How to check it | Status |
| --- | --- | --- |
| Two minors with no breaking kit change | The kit API guard (`internal/apicompat`, `docs/policy/kit-compat.md`) compares each kit release with the one before. `.github/workflows/kit-compat.yml` builds the previous frozen app against the new kit. The "Breaking" section of `CHANGELOG.md` for each release must be empty for the kit. | Remains. The guard is in place. No release exists yet, so no minor has passed. Tag v0.2 and v0.3 (below), and keep the kit change list clean. |
| All doc commands tested | `go test ./internal/cli -run TestEveryCommandIsDocumented` fails when a visible command has no mention in `docs/commands/`, or when a page there has no runnable block. `make doctest` runs the blocks. | Met. The test is in the repository, and every visible command is documented. |
| A published support policy | `docs/policy/support.md` and `docs/policy/behavior-version.md`. `docs/site/` publishes them with the Pages workflow. | Met in the repository. The site is live only after the GitHub repository is public (below). |
| A dogfood app upgraded across two or more minors | Create the app with v0.1 or v0.2. Add local edits. Run `spinup upgrade` to v0.3 and then to the newest minor. Run `spinup doctor`, the app tests, and `spinup deploy` to a test VM. | Remains. The app in `/Users/king/Documents/code/todo` exists, but no real minor release exists to upgrade across. |

## Release work for a maintainer

A maintainer must do these steps. No agent can do them.

| Item | How to check it | Status |
| --- | --- | --- |
| Tag v0.2 and v0.3 | `git tag -l 'v0.*'` and `git tag -l 'kit/v0.*'` list both tags. `scripts/release/check.sh <version>` passes. The release workflow publishes the assets of `docs/contributing/release.md`. | Remains. No tag exists. |
| Freeze the corpus | `testdata/corpus/<version>/` holds the real output of each release (`testdata/corpus/README.md`). The upgrade matrix runs it. | Remains. Only the synthetic `0.0.0-dev` corpus exists. Release corpora start at v0.3. |
| Create the signing key | `go run ./scripts/release/keygen`. Store the private key in the secret `SPINUP_TEMPLATES_SIGNING_KEY`. Add the public key to `store.TrustedKeys`. `scripts/release/check.sh` passes without `--snapshot`. | Remains. `store.TrustedKeys` is empty. |
| Add a LICENSE file | The file `LICENSE` exists at the repository root. GoReleaser archives include it. | Remains. The file does not exist. The owner must choose the license. |
| Create the GitHub repository | The repository `stopyellingatme/spinup` is public, with private vulnerability reporting on. The Pages workflow publishes `docs/site/`. | Remains. |
| Create the Homebrew tap | The repository `stopyellingatme/homebrew-tap` exists. The secret `HOMEBREW_TAP_TOKEN` has write access. A stable release updates `Casks/spinup`. | Remains. |
| Name a second owner | `MAINTAINERS.md` lists two owners and the release rota. | Remains. One owner is listed. |
| Run the end-to-end suites | `make e2e`, `make dind`, `make app-gate`, and `make vuln` pass on the release commit. | Remains. Run them at the release gate. They need Docker and network access. |

## Checks that are already met

| Item | How to check it | Status |
| --- | --- | --- |
| Behavior version reaches the app | `go test ./internal/devloop ./internal/deploy ./internal/project`. `docs/policy/behavior-version.md` describes the rule. | Met. |
| Deprecation window of one minor | `docs/policy/support.md`. The kit deprecation tests of the API guard. | The policy is published. The tests belong to the kit API guard work. |
| Upgrade matrix | `go test -race ./internal/upgrade/ -run 'Matrix|LockSchema'` | Met for the synthetic corpus. Real corpora follow the releases. |
| `pnpm test` passes in a new app with no test files | The web `vite.config.js` sets `passWithNoTests: true`. | Met. |

## Order of work

1. Add `LICENSE`, the second owner, and the signing key. Commit them.
2. Create the GitHub and tap repositories.
3. Tag v0.2. Freeze its corpus. Install it on the dogfood app.
4. Tag v0.3 a month later. Upgrade the dogfood app from v0.2 to v0.3 with local edits.
5. Check that the kit has no breaking change across both minors.
6. Run the end-to-end suites. Tag v1.0.
