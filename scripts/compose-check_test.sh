#!/bin/sh
# Tests compose-check.sh with fixture files for the Dockerfile, Docker Hub's tag list and
# OSV's answer: no network.
set -eu

script=$(cd "$(dirname "$0")" && pwd)/compose-check.sh
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

pin() {
	printf 'FROM scratch AS web\nFROM docker/compose-bin:%s@sha256:%s AS compose\n' \
		"$1" 0000000000000000000000000000000000000000000000000000000000000000 >"$dir/Dockerfile"
}
tags() {
	# The shape Docker Hub sends: compact JSON, one result per tag.
	printf '{"count":%d,"results":[' $#
	sep=
	for t in "$@"; do
		printf '%s{"images":[{"digest":"sha256:x","os":"linux"}],"name":"%s","tag_status":"active"}' "$sep" "$t"
		sep=,
	done
	printf ']}\n'
}
clean='{}'
vulnerable='{"vulns":[{"id":"GHSA-gv8h-7v7w-r22q","aliases":["CVE-2025-62725","GO-2025-4077"],"affected":[{"package":{"name":"github.com/docker/compose/v2","ecosystem":"Go"}}]},{"id":"GO-2025-4077","aliases":["CVE-2025-62725"]}]}'

export COMPOSE_CHECK_DOCKERFILE="$dir/Dockerfile" COMPOSE_CHECK_TAGS="$dir/tags" COMPOSE_CHECK_OSV="$dir/osv"

fail=0
# expect <exit code> <text the output must contain> <name>
expect() {
	code=0
	out=$(sh "$script" 2>&1) || code=$?
	case $out in
	*"$2"*) found=1 ;;
	*) found=0 ;;
	esac
	if [ "$code" = "$1" ] && [ $found = 1 ]; then
		echo "ok   $3"
	else
		echo "FAIL $3: exit $code (want $1), output:"
		printf '%s\n' "$out" | sed 's/^/     /'
		fail=1
	fi
}

pin v5.8.0
tags edge latest v5.8.0 v5.7.2 v5.0.0-rc.1 v2.40.3 >"$dir/tags"
printf '%s\n' "$clean" >"$dir/osv"
expect 0 "v5.8.0 is the newest release" "up to date, no advisories"
expect 0 "no advisories against github.com/docker/compose/v5 5.8.0" "module path follows the major version"

tags v5.10.0 v5.9.1 v5.8.0 >"$dir/tags"
expect 1 "v5.10.0 is out, the Dockerfile pins v5.8.0" "newer release, compared as versions not strings"

tags v6.0.0-rc.1 v5.8.0 >"$dir/tags"
expect 0 "newest release" "release candidates don't count"

pin v2.40.1
tags v5.8.0 v2.40.3 >"$dir/tags"
printf '%s\n' "$vulnerable" >"$dir/osv"
expect 1 "github.com/docker/compose/v2 2.40.1: GHSA-gv8h-7v7w-r22q GO-2025-4077" "advisories listed by id"

pin v5.8.0
tags v5.8.0 >"$dir/tags"
expect 1 "VULN" "advisory alone fails"

printf '{"vulns":[]}\n' >"$dir/osv"
expect 2 "no ids could be read" "vulns without ids is an error"
printf 'Bad Gateway\n' >"$dir/osv"
expect 2 "unreadable answer from OSV" "non-JSON OSV answer is an error"

printf '%s\n' "$clean" >"$dir/osv"
printf '{"results":[]}\n' >"$dir/tags"
expect 2 "no vX.Y.Z tags" "empty tag list is an error"

tags v5.8.0 >"$dir/tags"
printf 'FROM docker/compose-bin:latest AS compose\n' >"$dir/Dockerfile"
expect 2 "no single" "unpinned stage is an error"
pin v5.8.0
cat "$dir/Dockerfile" "$dir/Dockerfile" >"$dir/Dockerfile2" && mv "$dir/Dockerfile2" "$dir/Dockerfile"
expect 2 "more than one" "two compose-bin stages is an error"

# The real Dockerfile must parse: a clean answer for whatever it pins.
pinned=$(sed -n 's|^FROM docker/compose-bin:\(v[0-9.]*\)@.*|\1|p' "$(dirname "$script")/../Dockerfile")
tags "$pinned" >"$dir/tags"
COMPOSE_CHECK_DOCKERFILE="$(dirname "$script")/../Dockerfile" expect 0 "$pinned is the newest release" "repo Dockerfile parses"

exit $fail
