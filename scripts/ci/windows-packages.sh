#!/usr/bin/env bash
# Print the import paths that the Windows CI job tests, one for each line.
# It lists ./internal/... and removes the packages in windows-excluded.txt.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
module="github.com/stopyellingatme/spinup"

excluded=()
while IFS= read -r line; do
	line="${line%%#*}"
	line="$(echo "$line" | tr -d '[:space:]')"
	if [ -n "$line" ]; then
		excluded+=("$module/internal/$line")
	fi
done <"$here/windows-excluded.txt"

all="$(go list ./internal/... | tr -d '\r')"

# An excluded name that is not a package is a mistake. Fail so the list stays correct.
for name in "${excluded[@]}"; do
	if ! grep -qxF "$name" <<<"$all"; then
		echo "windows-excluded.txt names an unknown package: $name" >&2
		echo "Try: remove the line or correct the package path." >&2
		exit 1
	fi
done

while IFS= read -r pkg; do
	skip=0
	for name in "${excluded[@]}"; do
		if [ "$pkg" = "$name" ]; then
			skip=1
		fi
	done
	if [ "$skip" -eq 0 ]; then
		echo "$pkg"
	fi
done <<<"$all"
