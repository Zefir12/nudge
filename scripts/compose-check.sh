#!/bin/sh
# Checks the docker-compose binary the Dockerfile copies from docker/compose-bin, which
# govulncheck can't see. Fails when Docker Hub has a newer vX.Y.Z tag of compose-bin, or
# when OSV lists an advisory against the pinned version (Go module
# github.com/docker/compose/v<major>). Run nightly (nightly.yaml, job compose).
# COMPOSE_CHECK_DOCKERFILE, COMPOSE_CHECK_TAGS and COMPOSE_CHECK_OSV name files that
# replace the Dockerfile, Docker Hub's tag list and OSV's answer (tests).
# Needs only sh, curl, grep, sed and sort -V, so it runs in the Go image.
set -eu

die() {
	echo "compose-check.sh: $*" >&2
	exit 2
}

dockerfile=${COMPOSE_CHECK_DOCKERFILE:-$(dirname "$0")/../Dockerfile}
pinned=$(sed -n 's|^FROM docker/compose-bin:\(v[0-9]*\.[0-9]*\.[0-9]*\)@.*|\1|p' "$dockerfile")
case $pinned in
v*.*.*) ;;
*) die "no single 'FROM docker/compose-bin:vX.Y.Z@sha256:...' line in $dockerfile" ;;
esac
case $pinned in
*"
"*) die "more than one compose-bin stage in $dockerfile" ;;
esac
version=${pinned#v}
module=github.com/docker/compose/v${version%%.*}

if [ -n "${COMPOSE_CHECK_TAGS:-}" ]; then
	tags=$(cat "$COMPOSE_CHECK_TAGS")
else
	tags=$(curl -fsS --retry 3 \
		'https://hub.docker.com/v2/repositories/docker/compose-bin/tags?page_size=100&ordering=last_updated&name=v') ||
		die "can't list compose-bin tags on Docker Hub"
fi
# Release tags only: v5.0.0-rc.1, edge and latest don't match.
latest=$(printf '%s\n' "$tags" | grep -o '"name": *"v[0-9]*\.[0-9]*\.[0-9]*"' |
	sed 's/.*"\(v[^"]*\)"$/\1/' | sort -uV | tail -n 1) || true
[ -n "$latest" ] || die "no vX.Y.Z tags in Docker Hub's answer"

if [ -n "${COMPOSE_CHECK_OSV:-}" ]; then
	osv=$(cat "$COMPOSE_CHECK_OSV")
else
	osv=$(curl -fsS --retry 3 -H 'Content-Type: application/json' \
		-d "{\"package\":{\"name\":\"$module\",\"ecosystem\":\"Go\"},\"version\":\"$version\"}" \
		https://api.osv.dev/v1/query) || die "can't query OSV"
fi
# OSV answers {} when nothing is known, else {"vulns":[{"id":...},...]}.
advisories=
case $osv in
*'"vulns"'*)
	advisories=$(printf '%s\n' "$osv" | grep -o '"id": *"[^"]*"' |
		sed 's/.*"\([^"]*\)"$/\1/' | sort -u | tr '\n' ' ') || true
	[ -n "$advisories" ] || die "OSV lists vulns for $module $version but no ids could be read"
	;;
*'{'*) ;;
*) die "unreadable answer from OSV" ;;
esac

status=0
newest=$(printf '%s\n%s\n' "$pinned" "$latest" | sort -V | tail -n 1)
if [ "$newest" != "$pinned" ]; then
	echo "NEWER  docker/compose-bin $latest is out, the Dockerfile pins $pinned: bump it (check https://github.com/docker/compose/releases first)"
	status=1
else
	echo "ok     docker/compose-bin $pinned is the newest release"
fi
if [ -n "$advisories" ]; then
	echo "VULN   advisories against $module $version: ${advisories% }"
	status=1
else
	echo "ok     no advisories against $module $version"
fi
exit $status
