#!/usr/bin/env bash
# Decide whether a push can skip the heavy jobs of a workflow.
#
# The heavy jobs can skip when an earlier successful run of the same workflow tested a
# commit with the same git tree as GITHUB_SHA. A typical case: the ci/green pull request
# passed, and the same tree then lands on main. The script writes skip=true or skip=false
# to GITHUB_OUTPUT. It fails open: on any error it writes skip=false, so the jobs run.
#
# Environment:
#   GH_TOKEN            a token with actions: read and contents: read
#   GITHUB_REPOSITORY   owner/name
#   GITHUB_SHA          the commit of this run
#   GITHUB_RUN_ID       this run, which the search ignores
#   WORKFLOW            the workflow file name, for example ci.yml
#   MAX_AGE_DAYS        how far back to look (default 14)
#
# A run that skipped through this gate also ends in success. That is correct, because the
# tree that it skipped had passed before it.
set -uo pipefail

out="${GITHUB_OUTPUT:-/dev/stdout}"
summary="${GITHUB_STEP_SUMMARY:-/dev/null}"
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}"
sha="${GITHUB_SHA:?GITHUB_SHA is not set}"
workflow="${WORKFLOW:?WORKFLOW is not set}"
max_age="${MAX_AGE_DAYS:-14}"
run_id="${GITHUB_RUN_ID:-0}"

run_all() {
	echo "skip=false" >>"$out"
	echo "Full run: $1." >>"$summary"
	echo "tree-gate: full run: $1" >&2
	exit 0
}

tree="$(gh api "repos/$repo/git/commits/$sha" --jq .tree.sha)" || run_all "cannot read the tree of $sha"
[ -n "$tree" ] || run_all "the tree of $sha is empty"

# GNU date on the Linux runner, BSD date on a Mac.
since="$(date -u -d "-$max_age days" +%F 2>/dev/null || date -u -v-"$max_age"d +%F)" ||
	run_all "cannot compute the start date"

# The newest 50 successful runs, newest first. Keep the first run of each commit.
# Do not use unique_by: it sorts by SHA and loses the newest-first order.
candidates="$(gh api -X GET "repos/$repo/actions/workflows/$workflow/runs" \
	-f status=success -f "created=>=$since" -f per_page=50 \
	--jq "[.workflow_runs[] | select(.id != $run_id) | {sha: .head_sha, id: .id}]
	      | reduce .[] as \$r ([]; if any(.[]; .sha == \$r.sha) then . else . + [\$r] end)
	      | .[] | \"\(.sha) \(.id)\"")" ||
	run_all "cannot list the successful runs of $workflow"

while read -r candidate_sha candidate_run; do
	[ -n "$candidate_sha" ] || continue
	if [ "$candidate_sha" = "$sha" ]; then
		candidate_tree="$tree"
	else
		candidate_tree="$(gh api "repos/$repo/git/commits/$candidate_sha" --jq .tree.sha 2>/dev/null)" || continue
	fi
	if [ "$candidate_tree" = "$tree" ]; then
		echo "skip=true" >>"$out"
		message="Skipped: tree $tree passed in run $candidate_run (commit $candidate_sha). Run the workflow by hand to force a full run."
		echo "$message" >>"$summary"
		echo "tree-gate: $message" >&2
		exit 0
	fi
done <<<"$candidates"

run_all "no successful run of $workflow since $since tested tree $tree"
