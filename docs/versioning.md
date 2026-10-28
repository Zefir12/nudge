# Versioning

Builds are versioned `vMAJOR.MINOR.PATCH`, counted from git history by
`scripts/version.sh`. Nothing is committed for a version bump and there is no version
file: the same commit always gets the same version.

| Part  | Counts | Resets |
|-------|--------|--------|
| MAJOR | merges into `main` (releases) that the commit contains | never |
| MINOR | PRs merged into `develop` since the last release | on a release |
| PATCH | commits on `develop` since the last PR merge | on a PR merge or a release |

The counts follow the first-parent line, so a PR's own commits don't count once it is
merged: the merge commit bumps MINOR. On a feature branch, PATCH is develop's patch plus
the branch's commits. Release #88 is the first merge into `main`, so the versions start
at v1.

Where it's used:
- `make` sets `VERSION` from the script (`make build`, `make run`); `VERSION=...`
  overrides it.
- The image workflow checks out the full history (`fetch-depth: 0`, `origin/main`
  included) and passes the version as the `VERSION` build arg. Image tags stay
  `<branch>-<sha>` and `develop`.
- The binaries carry it (`internal/version`), `GET /api/v1/health` returns it, and the web
  UI shows it at the bottom of the sidebar.

The script fails on a shallow clone (the counts would be wrong) and prints `dev` outside a
git repository. `scripts/version_test.sh` (part of `make test` and CI) checks it against a
throwaway repository.
