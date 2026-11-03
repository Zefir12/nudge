# Versioning

Builds are versioned `vMAJOR.MINOR.PATCH` by `scripts/version.sh`. MAJOR is the number in
`version.txt` in the repo root; MINOR and PATCH are counted from git history.

| Part  | Comes from | Resets |
|-------|------------|--------|
| MAJOR | `version.txt`, bumped by hand | never |
| MINOR | PRs merged into `main` (releases) since the commit that last changed `version.txt` | on a bump |
| PATCH | PRs merged into `develop` since the last release or bump | on a release or a bump |

To bump MAJOR, change `version.txt`, on `develop` or in a PR. The commit gets
`vMAJOR.0.0`, the next PR into `develop` `vMAJOR.0.1`, and the next release `vMAJOR.1.0`.

Only PR merges move the numbers:
- The counts follow the first-parent line, so a PR's own commits don't count, and neither
  do commits made directly on `develop`. Those share the previous commit's version, so use
  the image tag's sha to tell such builds apart.
- Merging `main` back into `develop` (a merge whose second parent is on `main`'s
  first-parent line) is not a PR and bumps nothing.
- The PR that carries a bump doesn't count: the version is `vMAJOR.0.0` after it.
- A feature branch has the version of the `develop` commit it started from, unless the
  branch changes `version.txt`.

A commit keeps its version, with one exception: a commit that gets released takes the
release's version. A build of `develop`'s tip made before the release says `v4.0.2`, and
the same commit built after it says `v4.1.0`.

Where it's used:
- `make` sets `VERSION` from the script (`make build`, `make run`); `VERSION=...`
  overrides it.
- The image workflow checks out the full history (`fetch-depth: 0`, `origin/main`
  included) and passes the version as the `VERSION` build arg. Image tags stay
  `<branch>-<sha>` and `develop`.
- The binaries carry it (`internal/version`), `GET /api/v1/health` returns it, and the web
  UI shows it at the bottom of the sidebar.

The script reads `version.txt` from the commit, not the working tree, and fails if it is
missing or doesn't hold a single number. It also fails on a shallow clone (the counts would
be wrong) and prints `dev` outside a git repository. `scripts/version_test.sh` (part of
`make test` and CI) checks it against a throwaway repository.
