---
title: CI workflows and their cost
parent: Contributing
nav_order: 14
---

# CI workflows and their cost

This page tells you which GitHub Actions workflows run, when they run, and how they keep runner minutes low.
spinup is a public repository, so its standard runners are free.
The same rules apply to the workflows that spinup generates for apps, and those apps are usually private, where GitHub bills each minute.

## Price of a minute

GitHub rounds each job up to a whole minute.
On a private repository, a Linux minute costs $0.006, a Windows minute $0.010, and a macOS minute $0.062.
A macOS minute therefore costs about 10 times a Linux minute.
Before you add a job, a matrix entry, or a schedule, count its minutes.

## The CI workflow

`.github/workflows/ci.yml` runs on each pull request, on each push to `main`, and by hand.

| Job | Runner | What it does |
| --- | --- | --- |
| `gate` | Linux | On a push, it looks for an earlier green run of the same git tree. |
| `lint` | Linux | Runs `golangci-lint` on the CLI and the kit. |
| `actionlint` | Linux | Checks the workflow files. |
| `test` | Linux | Runs `go test -race ./...` and the kit tests. This includes the golden trees and the CLI scripts. |
| `windows` | Windows | Vets every package, builds the CLI, and tests the portable packages. |
| `upgrade` | Linux | Runs the [upgrade matrix](upgrade-matrix.md), the pack digest check, and the coverage floor. |
| `test-macos` | macOS | Runs the `test` job on macOS. It starts only after every other job passes. |

### Skip a tree that already passed

On a push, the `gate` job runs `scripts/ci/tree-gate.sh`.
The script compares the git tree of the pushed commit with the trees of the successful CI runs of the last 14 days.
If a tree matches, every other job skips, and the run summary names the run that passed.
If the script cannot decide, it fails open and every job runs.

A pull request run tests the head commit of the branch, not the merge commit.
Thus, when you push the tree of a green pull request to `main`, CI does not test it again.
To force a full run, start the workflow by hand:

```sh
gh workflow run ci.yml --ref main
```

### macOS runs last

The `test-macos` job waits for `lint`, `actionlint`, `test`, `windows`, and `upgrade`.
If one of them fails, the macOS job does not start.
Fix the Linux or Windows failure first, and the next run shows the macOS result.

## Other workflows

| Workflow | When it runs |
| --- | --- |
| `dind.yml` | Each push to `main`, and each Monday. A newer push cancels a run that is in progress. See [Docker-in-Docker deploy suite](dind.md). |
| `monitoring.yml` | A pull request or push that changes the monitoring paths, and each Monday. See [Monitoring stack boot test](monitoring-boot.md). |
| `nightly.yml` | Each night. It downloads from the network, so it finds drift in npm, crates.io, PyPI, and the browsers. See [Browser end-to-end suite](e2e.md). |
| `kit-compat.yml` | A pull request that changes `kit/`. |
| `docs.yml` | A push to `main` that changes `docs/`. |
| `canary-*.yml`, `bump.yml`, `shas.yml`, `dr.yml` | Once a week. |
| `release.yml` | A version tag. See [Release spinup](release.md). |

Every job has `timeout-minutes`.
Without it, GitHub stops a stuck job only after 6 hours.

## Measure the minutes

The run timing API no longer reports billable time, so read the jobs instead.
This command adds up the minutes of each job in the last 30 days, rounded up as GitHub bills them:

```sh
since=$(date -u -v-30d +%F 2>/dev/null || date -u -d '-30 days' +%F)
gh api --paginate "repos/stopyellingatme/spinup/actions/runs?created=>=$since&per_page=100" \
  --jq '.workflow_runs[].id' |
  while read -r id; do
    gh api "repos/stopyellingatme/spinup/actions/runs/$id/jobs?filter=all" \
      --jq '.jobs[] | select(.completed_at != null)
            | [.name, (.labels | join(",")),
               (((.completed_at | fromdate) - (.started_at | fromdate)) / 60 | ceil)] | @tsv'
  done |
  awk -F'\t' '{ minutes[$2] += $3 } END { for (k in minutes) print minutes[k], k }' | sort -rn
```

## See also

- [Upgrade matrix](upgrade-matrix.md)
- [Docker-in-Docker deploy suite](dind.md)
- [Browser end-to-end suite](e2e.md)
- [GitHub Actions billing](https://docs.github.com/en/billing/concepts/product-billing/github-actions)
