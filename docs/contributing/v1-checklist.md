---
title: v1.0 checklist
parent: Contributing
nav_order: 3
---

# v1.0 checklist

This page lists what must be true before spinup v1.0, and how to check each item.
The status was last verified on 2026-11-01.
Update the status in the same pull request that changes it. To re-check the release items, run `git tag -l`.

```sh
git tag -l 'v0.*'
git tag -l 'kit/v0.*'
```

## Gate items

The v1.0 gate has four items.

- [ ] **Two minors with no breaking kit change.** The kit API guard compares each kit release with the one before. The "Breaking" section of `CHANGELOG.md` must be empty for the kit in both releases. See [Kit compatibility](../policy/kit-compat.md). No v0.x release exists yet, so no minor has passed.
- [x] **All documented commands are tested.** `go test ./internal/cli -run TestEveryCommandIsDocumented` fails when a visible command has no page in `docs/commands/`, or when a page has no runnable block. `make doctest` runs the blocks.
- [x] **A published support policy.** [Support policy](../policy/support.md) and [Behavior versions](../policy/behavior-version.md) are pages of the docs site. The repository is public, and the Pages workflow publishes the site.
- [ ] **A dogfood app upgraded across two or more minors.** Create the app with v0.1 or v0.2, and add local edits. Run `spinup upgrade` to v0.3 and then to the newest minor. Run `spinup doctor`, the app tests, and `spinup deploy` to a test VM. The dogfood app `todo` exists, but no real minor release exists to upgrade across.

## Maintainer to-do list

A maintainer must do these steps. They need accounts, secrets, or decisions that only the owner has.

- [ ] **Tag v0.2 and v0.3.** `git tag -l 'v0.*'` and `git tag -l 'kit/v0.*'` list both tags, and `scripts/release/check.sh <version>` passes. See [Release spinup](release.md). Today only the pre-release tag `kit/v0.1.0-alpha.418` and the tag `go-freeze-r0` exist.
- [ ] **Freeze the corpus.** `testdata/corpus/<version>/` holds the real output of each release. The upgrade matrix runs it. Release corpora start at v0.3.
- [ ] **Create the signing key.** Run `go run ./scripts/release/keygen`. Store the private key in the secret `SPINUP_TEMPLATES_SIGNING_KEY`, and add the public key to `store.TrustedKeys`. Today `store.TrustedKeys` is empty.
- [ ] **Add a LICENSE file.** The file `LICENSE` must exist at the repository root. Today it does not exist. The owner must choose the license.
- [ ] **Turn on private vulnerability reporting.** The repository is public and the Pages workflow publishes the docs. In the repository settings, turn on private vulnerability reporting. Today it is off.
- [ ] **Create the Homebrew tap.** The repository `stopyellingatme/homebrew-tap` exists, and the secret `HOMEBREW_TAP_TOKEN` has write access.
- [ ] **Name a second owner.** `MAINTAINERS.md` lists two owners and the release rota. Today it lists one owner.
- [ ] **Run the end-to-end suites.** `make e2e`, `make dind`, `make app-gate`, and `make vuln` pass on the release commit. They need Docker and network access.

## Already met

- [x] **The behavior version reaches the app.** `go test ./internal/devloop ./internal/deploy ./internal/project`. See [Behavior versions](../policy/behavior-version.md).
- [x] **A deprecation window of one minor.** `TestKitAPI` and `TestKitDeprecations` in `internal/apicompat` enforce it.
- [x] **The upgrade matrix passes.** `go test -race ./internal/upgrade/ -run 'Matrix|LockSchema'` passes for the synthetic corpus. See [Upgrade matrix](upgrade-matrix.md).
- [x] **`pnpm test` passes in a new app with no test files.** The web `vite.config.js` sets `passWithNoTests: true`.

## Order of work

1. Add `LICENSE`, the second owner, and the signing key. Commit them.
2. Create the tap repository. Turn on private vulnerability reporting.
3. Tag v0.2. Freeze its corpus. Install it on the dogfood app.
4. Tag v0.3 a month later. Upgrade the dogfood app from v0.2 to v0.3 with local edits.
5. Check that the kit has no breaking change across both minors.
6. Run the end-to-end suites. Tag v1.0.

## See also

- [Release spinup](release.md)
- [Upgrade matrix](upgrade-matrix.md)
- [Support policy](../policy/support.md)
- [Kit compatibility](../policy/kit-compat.md)
