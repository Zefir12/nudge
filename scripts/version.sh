#!/bin/sh
# Prints HEAD's version, vMAJOR.MINOR.PATCH, counted from git history (no version file):
#   MAJOR  releases: merges into main that HEAD contains (the merge, or the commit it merged)
#   MINOR  PRs merged into develop (merges on HEAD's first-parent line) since the last release
#   PATCH  commits on HEAD's first-parent line since the last of those merges
# main is VERSION_MAIN_REF, else origin/main, else main. Prints "dev" outside a git
# repository, and fails on a shallow clone, whose counts would be wrong.
set -eu

if ! git rev-parse --git-dir >/dev/null 2>&1; then
	echo dev
	exit 0
fi
if [ "$(git rev-parse --is-shallow-repository)" = true ]; then
	echo "version.sh: shallow clone, fetch the full history (fetch-depth: 0)" >&2
	exit 1
fi

main=
for ref in "${VERSION_MAIN_REF:-}" origin/main main; do
	if [ -n "$ref" ] && git rev-parse -q --verify "$ref^{commit}" >/dev/null; then
		main=$ref
		break
	fi
done

# Releases, newest first: the first one HEAD contains is the base the counts start from.
major=0
base=
if [ -n "$main" ]; then
	for m in $(git rev-list --first-parent --merges "$main"); do
		if git merge-base --is-ancestor "$m" HEAD; then
			r=$m
		elif git merge-base --is-ancestor "$m^2" HEAD; then
			r=$(git rev-parse "$m^2")
		else
			continue
		fi
		major=$((major + 1))
		if [ -z "$base" ]; then base=$r; fi
	done
fi

range=HEAD
if [ -n "$base" ]; then range=$base..HEAD; fi
minor=$(git rev-list --first-parent --merges --count "$range")
last=$(git rev-list --first-parent --merges -n 1 "$range")
from=${last:-$base}
if [ -n "$from" ]; then range=$from..HEAD; fi
patch=$(git rev-list --first-parent --no-merges --count "$range")

echo "v$major.$minor.$patch"
