#!/usr/bin/env bash
# Fail when a tool that lanes or the milestone gate need is missing or too old.
# Minimum versions; the main loop raises them when stackversions.yaml changes.
set -euo pipefail

fail=0
need() { # name, minimum version, command that prints the version
  local name=$1 min=$2 have
  if ! have=$(eval "$3" 2>/dev/null | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1) || [[ -z $have ]]; then
    echo "MISSING  $name (need >= $min)"; fail=1; return
  fi
  if [[ $(printf '%s\n%s\n' "$min" "$have" | sort -V | head -1) != "$min" ]]; then
    echo "TOO OLD  $name $have (need >= $min)"; fail=1; return
  fi
  echo "ok       $name $have"
}

need go             1.27.2  "go env GOVERSION"
need git            2.50    "git --version"
need golangci-lint  2.14.0  "golangci-lint version"
need goreleaser     2.18.3  "goreleaser --version"
need actionlint     1.7.12  "actionlint --version"
need syft           1.54.1  "syft version"
need cosign         3.1.3   "cosign version"
need promtool       3.15.0  "promtool --version"
need sops           3.13.3  "sops --version"
need age            1.3.2   "age --version"
need node           24.0.0  "node --version"
need docker         27.0.0  "docker version --format '{{.Server.Version}}'"

if ! git merge-file -h 2>&1 | grep -q -- '--diff-algorithm'; then
  echo "MISSING  git merge-file --diff-algorithm"; fail=1
fi

exit $fail
