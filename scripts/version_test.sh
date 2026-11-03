#!/bin/sh
# Tests version.sh against a throwaway repository: version bumps, PR merges into develop
# and releases into main, as the Forgejo merge button makes them (merge commits).
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
expect_error() {
	if (cd "$1" && sh "$script" >/dev/null 2>&1); then
		echo "FAIL $2: want an error"
		fail=1
	else
		echo "ok   $2 fails"
	fi
}
commit() { git commit -q --allow-empty -m "$1"; }
merge() { git merge -q --no-ff -m "Merge $1" "$1"; }
bump() {
	printf '%s\n' "$1" >version.txt
	git add version.txt
	commit "Bump to $1"
}
# A PR with one commit, merged into the current branch.
pr() {
	git switch -q -c "$1"
	commit "$1 work"
	git switch -q -
	merge "$1"
}

got=$(cd "$dir" && sh "$script")
[ "$got" = dev ] || { echo "FAIL outside a repository: got $got, want dev"; fail=1; }

git init -q -b develop "$dir/repo"
cd "$dir/repo"
commit initial
git branch main
expect_error "$dir/repo" "no version.txt"
bump x
expect_error "$dir/repo" "version.txt without a number"
bump 4
expect v4.0.0 "bump starts at MAJOR.0.0"

commit chore1
expect v4.0.0 "direct commits don't count"
git switch -q -c feat/1
commit f1
commit f2
expect v4.0.0 "a feature branch's commits don't count"
git switch -q develop
merge feat/1
expect v4.0.1 "PR into develop bumps patch"
pr feat/2
expect v4.0.2 "second PR into develop"

git switch -q main
merge develop
expect v4.1.0 "release on main bumps minor, resets patch"
git switch -q develop
expect v4.1.0 "develop at the released commit"
commit chore2
expect v4.1.0 "direct commit after a release"
pr feat/3
pr feat/4
expect v4.1.2 "two PRs since the release"

git switch -q main
merge develop
expect v4.2.0 "second release"
git switch -q develop
commit chore3
git merge -q --no-ff -m "Merge main into develop" main
expect v4.2.0 "merging main back into develop is not a PR"
pr feat/5
expect v4.2.1 "PR after the back-merge"

printf '9\n' >version.txt
expect v4.2.1 "an uncommitted version.txt is ignored"

git switch -q -c feat/old
commit old
git switch -q develop
bump 5
expect v5.0.0 "bump on develop resets minor and patch"
merge feat/old
expect v5.0.1 "PR branched before the bump counts"

git switch -q -c feat/bump
bump 6
expect v6.0.0 "bump on its branch"
git switch -q develop
merge feat/bump
expect v6.0.0 "the PR carrying the bump doesn't count"
pr feat/6
expect v6.0.1 "PR after the bump"

git switch -q main
merge develop
expect v6.1.0 "first release after a bump"
git switch -q develop
pr feat/7
expect v6.1.1 "PR after that release"

git clone -q --depth 1 "file://$dir/repo" "$dir/shallow"
expect_error "$dir/shallow" "shallow clone"

exit $fail
