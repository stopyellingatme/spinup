#!/usr/bin/env bash
# Compare the repository rulesets on GitHub with the files in .github/rulesets/, and apply them.
#
# Usage: scripts/github/apply-rulesets.sh [--apply] [--repo owner/name]
#
# Needs gh (signed in, with admin access to the repository) and jq.
#
# Repository: the default is the constant stopyellingatme/spinup. The script does not call
# gh repo view, so a fork checkout never changes the target by accident. Use --repo to
# choose another repository.
#
# Default is a dry run. For each .github/rulesets/*.json the script finds the live ruleset
# with the same name, then prints a unified diff of the normalized live ruleset against the
# normalized file. The dry run only reads from GitHub.
#
# Normalization keeps name, target, enforcement, conditions, bypass_actors (sorted), and rules
# (sorted by type, required_status_checks sorted by context). The server adds default values
# to rule parameters. To make "in sync" reliable, the script projects the live parameters of
# each rule onto the keys that the file sets. A key that the file does not set never causes a
# difference. A live rule type that the file does not list does cause a difference.
#
# --apply: POST a missing ruleset, PUT a ruleset that differs. It never deletes. A live ruleset
# with no file is reported as "unmanaged". It never prompts.
#
# Exit status: 0 all in sync, 1 drift or a missing ruleset, 2 usage error, missing tool, or a
# failed GitHub API call.
set -euo pipefail

repo="stopyellingatme/spinup"
apply=false
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

usage_error() {
	echo "apply-rulesets: $1" >&2
	echo "Try: $2" >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
	--apply) apply=true ;;
	--repo)
		[ $# -ge 2 ] || usage_error "--repo needs a value." "run with --repo owner/name."
		repo="$2"
		shift
		;;
	-h | --help)
		sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d'
		exit 0
		;;
	*) usage_error "unknown argument $1." "run with --help to see the usage." ;;
	esac
	shift
done

for tool in gh jq diff; do
	command -v "$tool" >/dev/null 2>&1 ||
		usage_error "the tool $tool is not on PATH, so the script cannot run." "install $tool and run the script again."
done
case "$repo" in */*) ;; *) usage_error "the repository name $repo has no owner." "use the form owner/name." ;; esac

# Normalize a ruleset. $spec is the committed file, which decides the projected parameter keys.
# With the argument "{}" the filter only sorts, which is correct for the committed file itself.
# shellcheck disable=SC2016 # the jq program uses $ on purpose
normalize='
def proj($s):
  if (type == "object") and ($s | type) == "object"
  then . as $o | reduce ($s | keys_unsorted[]) as $k ({}; if $o | has($k) then .[$k] = ($o[$k] | proj($s[$k])) else . end)
  else . end;
def rule($spec):
  . as $r
  | ([$spec.rules[]? | select(.type == $r.type)][0] // null) as $m
  | if $m != null and ($r | has("parameters")) and ($m | has("parameters"))
    then .parameters |= proj($m.parameters) else . end
  | if has("parameters") and (.parameters.required_status_checks? != null)
    then .parameters.required_status_checks |= (map({context, integration_id}) | sort_by(.context)) else . end
  | if has("parameters") and (.parameters.allowed_merge_methods? != null)
    then .parameters.allowed_merge_methods |= sort else . end;
{
  name, target, enforcement, conditions,
  bypass_actors: ((.bypass_actors // []) | map({actor_id, actor_type, bypass_mode}) | sort_by(.actor_type, .actor_id)),
  rules: ((.rules // []) | map(rule($spec) | del(.ruleset_source_type, .ruleset_source, .ruleset_id)) | sort_by(.type))
}
'

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Call the GitHub API and leave the response body in $out. On failure, print what failed and
# a Try: line, then exit with 2. Call it in the current shell, not in a command substitution.
out=""
gh_or_die() { # usage: gh_or_die <what> <gh api arguments...>
	local what="$1"
	shift
	if ! out="$(gh api "$@" 2>"$tmp/err")"; then
		echo "apply-rulesets: cannot $what in $repo: $(tr '\n' ' ' <"$tmp/err") $out" >&2
		echo "Try: run gh auth status, and check that you have admin access to $repo." >&2
		exit 2
	fi
}

gh_or_die "list the rulesets" "repos/$repo/rulesets?includes_parents=false&per_page=100"
live_list="$out"

status=0
managed_names=()
shopt -s nullglob
files=("$root"/.github/rulesets/*.json)
[ ${#files[@]} -gt 0 ] || usage_error "no ruleset file in $root/.github/rulesets." "restore the files from git."

for file in "${files[@]}"; do
	jq -e . "$file" >/dev/null 2>&1 || usage_error "$file is not valid JSON." "fix the file and run the script again."
	name="$(jq -r .name "$file")"
	managed_names+=("$name")
	rel=".github/rulesets/$(basename "$file")"
	id="$(jq -r --arg n "$name" '[.[] | select(.name == $n)][0].id // empty' <<<"$live_list")"
	jq --argjson spec "$(cat "$file")" "$normalize" "$file" >"$tmp/want.json"

	if [ -z "$id" ]; then
		echo "missing: $name ($rel) does not exist in $repo"
		status=1
		if $apply; then
			gh_or_die "create the ruleset $name" -X POST "repos/$repo/rulesets" --input "$file"
			echo "created: $name"
		fi
		continue
	fi

	gh_or_die "read the ruleset $name" "repos/$repo/rulesets/$id"
	jq --argjson spec "$(cat "$file")" "$normalize" <<<"$out" >"$tmp/have.json"
	if diff -u --label "live/$name" --label "file/$rel" "$tmp/have.json" "$tmp/want.json" >"$tmp/diff"; then
		echo "in sync: $name"
	else
		echo "drift: $name ($rel)"
		cat "$tmp/diff"
		status=1
		if $apply; then
			gh_or_die "update the ruleset $name" -X PUT "repos/$repo/rulesets/$id" --input "$file"
			echo "updated: $name"
		fi
	fi
done

while IFS= read -r live_name; do
	known=false
	for n in "${managed_names[@]}"; do [ "$n" = "$live_name" ] && known=true; done
	$known || echo "unmanaged: $live_name exists in $repo and has no file (the script never deletes)"
done < <(jq -r '.[].name' <<<"$live_list")

if [ "$status" -ne 0 ] && $apply; then
	echo "Applied. Run the script again without --apply to confirm that everything is in sync."
fi
exit "$status"
