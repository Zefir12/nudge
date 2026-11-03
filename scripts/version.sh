#!/bin/sh
# Prints HEAD's version, vMAJOR.MINOR.PATCH (docs/versioning.md):
#   MAJOR  the number in version.txt, bumped by hand
#   MINOR  PRs merged into main that HEAD contains, since the commit that last changed
#          version.txt
#   PATCH  PRs merged into develop (merges on HEAD's first-parent line) since the last of
#          those or the bump; merging main back into develop and the PR carrying the bump
#          don't count
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

# The committed file, not the working tree: a version describes a commit.
if ! major=$(git show HEAD:version.txt 2>/dev/null); then
	echo "version.sh: version.txt is not committed" >&2
	exit 1
fi
major=$(printf '%s' "$major" | tr -d '[:space:]')
case $major in
'' | *[!0-9]*)
	echo "version.sh: version.txt must hold a single number" >&2
	exit 1
	;;
esac
bump=$(git log -1 --format=%H HEAD -- version.txt)

main=
for ref in "${VERSION_MAIN_REF:-}" origin/main main; do
	if [ -n "$ref" ] && git rev-parse -q --verify "$ref^{commit}" >/dev/null; then
		main=$ref
		break
	fi
done

# Releases, newest first: the first one HEAD contains is where PATCH starts counting.
# A release counts through the merge itself (on main) or the commit it merged (on develop).
minor=0
base=$bump
mainline=
if [ -n "$main" ]; then
	found=
	for m in $(git rev-list --first-parent --merges "$main"); do
		if git merge-base --is-ancestor "$m" HEAD; then
			r=$m
		elif git merge-base --is-ancestor "$m^2" HEAD; then
			r=$(git rev-parse "$m^2")
		else
			continue
		fi
		if ! git merge-base --is-ancestor "$bump" "$r"; then
			break
		fi
		minor=$((minor + 1))
		if [ -z "$found" ]; then
			found=1
			base=$r
		fi
	done
	mainline=$(git rev-list --first-parent "$main")
fi

patch=0
for m in $(git rev-list --first-parent --merges "$base..HEAD"); do
	# The second parent is on main's first-parent line: main merged back into develop.
	if [ -n "$mainline" ] && printf '%s\n' "$mainline" | grep -qx "$(git rev-parse "$m^2")"; then
		continue
	fi
	# This merge brought the bump in: it's the PR carrying it, the start of the count.
	if git merge-base --is-ancestor "$bump" "$m^2" && ! git merge-base --is-ancestor "$bump" "$m^1"; then
		continue
	fi
	patch=$((patch + 1))
done

echo "v$major.$minor.$patch"
