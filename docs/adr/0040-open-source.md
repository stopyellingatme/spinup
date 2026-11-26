# ADR 0040: Open source terms and repository rules

Status: accepted (owner, 2026-11-25)
Date: 2026-11-25
Extends: ADR 0012 (release assets), ADR 0036 (quality gates)

## Context

The repository is public, but it had no license, no contribution terms, and no rules on the `main` branch.
Without a license, nobody may legally use, copy, or change the code.
If the repository has no rules, a mistaken push can delete `main` or move a release tag.
Go also needs a stable tag history, because the Go module proxy caches each version for ever.

## Decision

### 1. License

1. The license is MIT. The copyright line is `Copyright (c) 2026 Taylor King and the spinup authors`.
2. One `LICENSE` file at the repository root covers all three Go modules. The modules are the root, `kit/`, and `plugin-sdk/`.
3. Reason: Go puts the root `LICENSE` in the zip of a nested module that has no `LICENSE` of its own. A copy in each module would drift.

### 2. Generated code

1. The output of the template packs and the generators belongs to the user. The user has no duty to add a notice.
2. The statement is in `README.md` under "License", in the section "Generated code". It is not in `LICENSE`, so license detection keeps working.
3. The statement does not cover third-party files that a user copies into an app. It does not cover the kit, which stays under MIT.
4. Contributors accept this statement in `CONTRIBUTING.md`, in the section "License of your contribution".

### 3. Third-party notices

1. `THIRD_PARTY_NOTICES.md` lists each third-party file that spinup ships, with its license and its license text.
2. Rule: spinup vendors a file only when the license of the file is known.
3. Example: the Grafana dashboard 14282 had no license. spinup replaced it with a dashboard that spinup wrote (the "Containers" dashboard).

### 4. Contribution terms and conduct

1. Contributions come under the project license only. There is no DCO and no CLA.
2. Reason: one maintainer cannot run a CLA process. The license of the project already grants the rights that the project needs.
3. The code of conduct is Contributor Covenant 2.1. The contact is the private advisory form `https://github.com/stopyellingatme/spinup/security/advisories/new`.

### 5. Rulesets as code

The directory `.github/rulesets/` holds three rulesets as JSON.

| Ruleset | Target | Rules | Bypass |
| --- | --- | --- | --- |
| `main-history` | The default branch | No deletion. No force push. | None. |
| `main-merge` | The default branch | A pull request with zero approvals, and the six required checks. | The admin role. |
| `release-tags` | `v*`, `kit/v*`, `plugin-sdk/v*` | A tag cannot move or be deleted. | None. |

1. The six required checks are `lint`, `actionlint`, `test`, `windows`, `upgrade`, and `test-macos`.
2. Zero approvals: one maintainer cannot approve their own pull request. When a second owner exists, raise the count to one.
3. The admin bypass lets the owner push a local merge commit. The pull request rule still applies to everyone else.
4. All six checks are required because GitHub counts a skipped check as a pass. A skipped `test-macos` therefore does not block a merge.
5. A check with a path filter is not required. If a check does not run on a pull request, it stays "expected" for ever. Then the check blocks the merge.
6. Rule for `ci.yml`: do not rename the six jobs. Do not add a filter to `on.pull_request`. If you must rename a job or add a filter, change the ruleset file in the same pull request.
7. `TestRulesetsMatchWorkflows` in `internal/ci` guards this rule.
8. Release tags have no bypass. A bad tag needs a new version.

### 6. The apply script

1. `scripts/github/apply-rulesets.sh` compares the live rulesets with the files.
2. It is a dry run by default. The option `--apply` creates or updates a ruleset by name.
3. It never deletes a ruleset. The script reports a live ruleset with no file as "unmanaged".
4. Reason: a script that can delete rules is a risk. The owner can delete a ruleset in the GitHub settings.

### 7. Repository settings

| Setting | Value | Reason |
| --- | --- | --- |
| Private vulnerability reporting | On | Reporters need a private channel. `SECURITY.md` names it. |
| Dependabot alerts | On | Alerts cost nothing and show known flaws. |
| Dependabot security updates | Off | They would edit shared lockfiles, which only the main loop writes. |
| Homepage | The docs site | One place for users to start. |
| Wiki | Off | The docs live in `docs/`. |
| Discussions | Off | One maintainer cannot watch a second channel. |
| Delete branch on merge | On | It keeps the branch list short. |
| Allow update branch | On | A contributor can update a branch in the web page. |
| Auto-merge | On | The maintainer can queue a merge. |
| Merge commit and squash | Allowed | The maintainer merges with either. |
| Rebase merge | Off | It rewrites commit ids. |
| `sha_pinning_required` | `true` | A workflow must pin each action to a commit. |
| Fork pull request runs | Approval for all outside contributors | A fork cannot run code in CI without a maintainer. |

### 8. Workflow hardening

1. `docs.yml` has a top-level `permissions: contents: read`.
2. Every checkout step sets `persist-credentials: false`, except the checkout in `release.yml`. That job needs the token to push the kit tag. `TestCheckoutsDropCredentials` guards this rule.
3. `scripts/ci/tree-gate.sh` ignores runs from forks. A run from a fork does not prove that a tree passed.
4. `TestRepositoryWorkflowsPassVerify` runs the YAML checks of `ci.Verify` on the real workflow files. It finds zero findings. The test does not run actionlint. The `actionlint` job does.

### 9. Root markdown files

`TestDocsRootFiles` guards the root markdown files (`README.md`, `CONTRIBUTING.md`, `SECURITY.md`, and the others). It checks that they exist and that their links and anchors resolve.

## Not decided here

- The first release, and its version.
- A second owner.
- A license notice inside generated apps.

## Consequences

- A contributor sends a change as a pull request, and the six checks must pass.
- The maintainer can still push a local merge commit as the admin.
- A change to a required check needs one pull request that edits `ci.yml` and `main-merge.json`. After the merge, run the apply script.
- See [Repository rules](../contributing/repository-rules.md).
