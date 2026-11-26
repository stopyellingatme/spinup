---
title: Repository rules
parent: Contributing
nav_order: 15
---

# Repository rules

This page describes the rules that GitHub enforces on the spinup repository. It also shows how to audit and change them.
The decision and its reasons are in ADR 0040.
The rules are code. They live in `.github/rulesets/`.

## The three rulesets

| Ruleset | What it protects | Bypass |
| --- | --- | --- |
| `main-history` | `main` cannot be deleted or force-pushed. | None. |
| `main-merge` | A change reaches `main` through a pull request with six passing checks. | The admin role. |
| `release-tags` | The tags `v*`, `kit/v*`, and `plugin-sdk/v*` cannot move or be deleted. | None. |

### For a contributor

1. Open a pull request. Nobody can push to `main` directly.
2. Wait for the six required checks. They are `lint`, `actionlint`, `test`, `windows`, `upgrade`, and `test-macos`.
3. A maintainer must approve the CI run of a pull request from a fork. See [CI workflows and their cost](ci.md).
4. The pull request needs no review approval, because the project has one owner.

### For the maintainer

1. The admin role can bypass `main-merge`. Use this only to push a local merge commit.
2. Merge a pull request with a merge commit or a squash. Rebase merges are off.
3. A tag cannot move or be deleted, so a bad tag needs a new version. See [Release spinup](release.md).

## Apply the rulesets

The script `scripts/github/apply-rulesets.sh` compares the live rulesets with the files.
It needs `gh` with admin access, and `jq`.
It is a dry run by default. It prints a diff for each ruleset and exits with 1 on drift.
The option `--apply` creates or updates a ruleset by name. The script never deletes a ruleset.

```sh
scripts/github/apply-rulesets.sh
scripts/github/apply-rulesets.sh --apply
```

## Audit the settings

Each command only reads from GitHub.

| Group | What to check | Command |
| --- | --- | --- |
| Rulesets | The three rulesets exist and are active. | `gh api repos/stopyellingatme/spinup/rulesets` |
| Merge | The merge methods, auto-merge, and branch deletion. | `gh api repos/stopyellingatme/spinup` |
| Security | Dependabot security updates and secret scanning. | `gh api repos/stopyellingatme/spinup --jq .security_and_analysis` |
| Alerts | Dependabot alerts are on. Status 204 means on, and status 404 means off. | `gh api -i repos/stopyellingatme/spinup/vulnerability-alerts` |
| Reporting | Private vulnerability reporting is on. | `gh api repos/stopyellingatme/spinup/private-vulnerability-reporting` |
| Actions | SHA pinning and the allowed actions. | `gh api repos/stopyellingatme/spinup/actions/permissions` |
| Forks | Approval for fork pull request runs. | `gh api repos/stopyellingatme/spinup/actions/permissions/fork-pr-contributor-approval` |

```sh
gh api repos/stopyellingatme/spinup/rulesets --jq '.[] | [.name, .enforcement] | @tsv'
gh api repos/stopyellingatme/spinup --jq '{allow_merge_commit, allow_squash_merge, allow_rebase_merge, allow_auto_merge, delete_branch_on_merge, allow_update_branch, has_wiki, has_discussions, homepage}'
gh api repos/stopyellingatme/spinup/private-vulnerability-reporting
gh api -i repos/stopyellingatme/spinup/vulnerability-alerts
gh api repos/stopyellingatme/spinup/actions/permissions
gh api repos/stopyellingatme/spinup/actions/permissions/fork-pr-contributor-approval
```

## Change a required check

The names of the six checks are in two places. They are the job names in `.github/workflows/ci.yml` and the contexts in `.github/rulesets/main-merge.json`.
`TestRulesetsMatchWorkflows` fails when the two differ.

1. Edit `ci.yml` and `main-merge.json` in one pull request.
2. Merge the pull request after the checks pass.
3. Run the apply script with `--apply`.

Do not rename the six jobs. Do not add a filter to `on.pull_request`.
A path filter leaves a required check "expected" for ever. Then the pull request cannot merge.

## See also

- [CI workflows and their cost](ci.md)
- [Release spinup](release.md)
- [ADR 0040](../adr/0040-open-source.md)
