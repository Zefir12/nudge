#!/bin/sh
# Tests version.sh against a throwaway repository: commits, PR merges into develop and
# releases into main, as the Forgejo merge button makes them (merge commits).
set -eu

script=$(cd "$(dirname "$0")" && pwd)/version.sh
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export VERSION_MAIN_REF=main

fail=0
expect() {
	got=$(cd "$dir/repo" && sh "$script")
	if [ "$got" = "$1" ]; then
		echo "ok   $1  $2"
	else
		echo "FAIL $2: got $got, want $1"
		fail=1
	fi
}
commit() { git commit -q --allow-empty -m "$1"; }
merge() { git merge -q --no-ff -m "Merge $1" "$1"; }

got=$(cd "$dir" && sh "$script")
[ "$got" = dev ] || { echo "FAIL outside a repository: got $got, want dev"; fail=1; }

git init -q -b develop "$dir/repo"
cd "$dir/repo"
commit initial
git branch main
expect v0.0.1 "first commit"

git switch -q -c feat/1
commit f1
commit f2
expect v0.0.3 "feature branch counts its commits"
git switch -q develop
merge feat/1
expect v0.1.0 "PR merge bumps minor, resets patch"
commit chore1
commit chore2
expect v0.1.2 "direct commits bump patch"

git switch -q main
merge develop
expect v1.0.0 "release on main"
git switch -q develop
expect v1.0.0 "develop at the released commit"
commit chore3
expect v1.0.1 "commit after a release"
git switch -q -c feat/2
commit f3
git switch -q develop
merge feat/2
git switch -q -c feat/3
commit f4
git switch -q develop
merge feat/3
expect v1.2.0 "two PRs since the release"

git switch -q main
merge develop
expect v2.0.0 "second release"
git switch -q develop
commit chore4
expect v2.0.1 "commit after the second release"
git merge -q --no-ff -m "Merge main into develop" main
expect v2.0.1 "merging main back into develop is not a PR"
git switch -q -c feat/4
commit f5
git switch -q develop
merge feat/4
expect v2.1.0 "PR after the back-merge"

git clone -q --depth 1 "file://$dir/repo" "$dir/shallow"
if (cd "$dir/shallow" && sh "$script" >/dev/null 2>&1); then
	echo "FAIL shallow clone: want an error"
	fail=1
else
	echo "ok   shallow clone fails"
fi

exit $fail
