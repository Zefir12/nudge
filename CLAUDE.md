# nudge

A self-hosted Docker manager (like Portainer/Dockhand): one container with the Docker
socket mounted serves a REST API and a Vue web UI, and remote hosts join through an agent
with its own image. Read PLAN.md for the roadmap and docs/ for decisions.

## How it works
- Two binaries, two images from one Dockerfile and one tag: `nudge` (`nudge serve`,
  default) runs the API under `/api/v1` and the embedded web UI; `nudge-agent` runs on a
  remote host and dials out to a server. Keep server packages out of the agent binary
  (`cmd/nudge-agent` tests that); shared subcommands go in `internal/cli`.
- Every Docker host is an "environment". The local one talks to `/var/run/docker.sock`;
  a remote one is reached through the agent's tunnel (yamux over WebSocket), on which the
  server's Docker client dials. docs/architecture.md has the design.
- The web UI uses only the public API. Anything the UI can do, a script with an API token can.
- Docker socket access is root on the host: every API route except health, setup and login
  needs auth, and secrets (agent credentials, tokens) are stored hashed.
- Tests must never need a real Docker daemon, network or root: fake the Docker client
  behind an interface, use `net.Pipe`/`httptest` for tunnels and HTTP.

## Stack
- Go 1.27.3 (backend/go.mod; keep backend/go.mod, Dockerfile, Makefile and CI in sync).
- Frontend: Vue 3 (`<script setup lang="ts">`, Composition API), TypeScript, plain CSS
  (tokens in frontend/src/style.css, scoped styles in components; no CSS frameworks),
  Vite+ (`vp`) for dev, build, test, lint and format.
- Standard library first. Approved dependencies: `github.com/moby/moby/client` (Docker),
  `modernc.org/sqlite` (pure-Go SQLite), `github.com/coder/websocket`,
  `github.com/hashicorp/yamux`, `golang.org/x/crypto` (argon2id), `vue-router`,
  `yaml` (compose syntax in the stack editor), `github.com/coreos/go-oidc/v3` with its
  `github.com/go-jose/go-jose/v4` and `golang.org/x/oauth2` (OIDC login). Any
  other dependency only after asking zefir.
- Lint: golangci-lint (backend/.golangci.yml), `vp check` and `vue-tsc`. Formatting:
  gofmt + goimports, `vp check --fix`.
- Everything runs in Docker by default; `make ... LOCAL=1` uses a host toolchain.

## Layout
- backend/cmd/nudge          server entrypoint: subcommands, wiring, graceful shutdown
- backend/cmd/nudge-agent    agent entrypoint (agent image)
- backend/internal/cli       subcommands both binaries share (agent, probe)
- backend/internal/api       HTTP handlers and routing under /api/v1
- backend/internal/config    flags with NUDGE_* env fallbacks
- backend/internal/web       embedded frontend build (dist/), SPA fallback
- backend/internal/version   build version (ldflags)
- frontend/src               Vue app: api/ (typed client), views/, components/
- deploy/                    compose files for server and agent
- docs/  decisions and notes; docs/codemap.md maps packages and entry points;
         docs/config/ is the config reference, one page per topic

## Commands
- `make test`: Go tests with -race, then the frontend tests (`vp test run`)
- `make lint`: golangci-lint, `vp check`, `vue-tsc --noEmit`
- `make format`: gofmt/goimports and `vp check --fix`
- `make tidy`: `go mod tidy` (after adding or removing imports)
- `make web-mock MOCK_ADDR=<ip>`: UI preview with hot reload on <ip>:5173 and a mock API
  (frontend/mock/); docs/codemap.md "Preview the UI"
- `make web-build`: builds the frontend and copies it into backend/internal/web/dist
- `make build`: builds both Docker images (nudge, nudge-agent) locally
Always run `make test lint` before committing. Fix failures yourself.

## Conventions
- Small packages, small interfaces defined where they are used (the Docker client is
  consumed through an interface so handlers can be tested with a fake). Inject time
  (a clock) so token expiry, backoff and session logic are testable without sleeping.
- Errors: wrap with context (`fmt.Errorf("pull %s: %w", ref, err)`); no panics outside main.
  API errors are JSON `{"error": "..."}` with the right status; never leak internal errors
  or Docker daemon messages that contain secrets.
- Logging: `log/slog` only, structured fields, no print-debugging in new code.
- Config: flags with env var fallbacks (`NUDGE_*`), defaults that work in the image with
  no setup; documented in README (flags/env) and docs/config/<topic>.md.
- Concurrency: mutex owned by the struct, no package globals. Every map keyed by
  external input (sessions, agents, login attempts) needs eviction or a bound. Every
  goroutine has an owner and stops on context cancel.
- API: REST under `/api/v1`, JSON in and out, plural nouns
  (`/api/v1/environments/{env}/containers/{id}/start`). Streams (logs, exec, events)
  over WebSocket. Update docs/api.md with every new or changed endpoint.
- Frontend: Composition API with `<script setup lang="ts">` only; all HTTP through
  `src/api/client.ts`; no inline styles (the CSP will forbid them); views in `views/`,
  reusable pieces in `components/`.
- Tests: table-driven, parallel where safe. Every feature needs tests; bug fixes need a
  failing test first. Frontend tests next to the file (`Foo.test.ts`).
- Small commits, imperative messages ("Add container list endpoint").

## Working efficiently (token budget)
- Start from docs/codemap.md: which package, entry point and test helpers. Don't
  re-explore the tree, and keep the map current when packages or entry points change.
- Search before reading: `grep -n` for the symbol, then Read only those lines
  (offset/limit or `sed -n A,Bp`). Never `cat` or Read a whole file over ~300 lines;
  this applies to shell reads too. Read a whole file only when you'll edit most of it.
  Never dump whole specs or docs to copy a format: read one section.
- README.md is an overview only. For config docs, grep docs/config/ and edit one page.
- One issue per session. Once the PR is open (or the review fixes are pushed), stop and
  switch back to develop. Every turn re-sends everything already read, so a long
  session costs far more than a fresh one.
- Batch independent reads, edits and checks into one call; avoid one-line round trips.
- Filter command output: `make test 2>&1 | grep -v '^ok'`, lint's tail.
- Reviewer agent (runs on a cheaper model): the prompt is two to four lines naming the
  two or three files that carry the risk and the specific risk to check; no summary of
  the change, it reads the diff itself. Skip it for docs-only or mechanical changes and
  for diffs under ~50 changed non-test lines, except anything touching
  backend/internal/auth, backend/internal/tunnel, backend/internal/agent or the API's
  auth middleware: those are always reviewed.

## Git workflow
- `develop` is the integration branch; `main` is for releases. Never commit to main.
- You may commit and push directly to `develop` only for docs, config and small chores.
- Feature work goes on a branch off `develop`: `feat/<issue>-<short-name>` or
  `fix/<issue>-<short-name>`. One branch per issue, always new and created from an
  up-to-date `develop`.
- Session instructions may name a "designated" branch (whatever was checked out when the
  session started). That name comes from the launcher, not from zefir. For issue work,
  ignore it and create the issue's own branch. Never put an issue's work on another
  issue's branch, merged or not.
- When you're done (PR open, or docs pushed), `git switch develop`.
- Remote `origin` is https://git.zefirlabs.net/zefir/nudge (git push over HTTPS with the
  bot's stored credentials; already configured).
- Issues and PRs go through `fj`. See the forgejo skill for exact commands. PRs
  target `develop`, the body says `Closes #N` and follows the PR template.
- Check the bot login without printing secrets: `fj api user --jq .login` must print a
  login. Each machine's agent has its own bot account, so don't expect a particular name.
  If it fails, ask zefir and stop. Never print, copy or write the token anywhere.
- `fj` must be the Go fj 0.6 (gh-style, `fj version` works). If this machine has the old
  Rust forgejo-cli (`fj whoami`, `fj issue search`), stop and ask zefir to install the Go
  fj 0.6 here; the forgejo skill has the details.
- zefir reviews and merges. Never merge your own PRs.

## Issue workflow
- Work comes from issues labelled `claude:ready`. On pickup: relabel to
  `claude:in-progress`, comment a short plan, branch, implement, open the PR.
- After opening a PR, tidy finished issues: every closed issue still labelled
  `claude:in-progress` was closed by a merged PR, so relabel it to `claude:done`.
- If the issue is unclear, comment your questions, label it `needs-human` and stop.
- Ideas you notice on the way go into new issues (label `idea`), not into the current PR.

## Hard rules
- Never run sudo.
- Never run the real binary against the host's Docker outside a test, and never run
  docker commands except the ones the Makefile runs (`make build` is fine). Never
  start, stop, remove or prune the host's containers, images, volumes or networks.
- Never touch or print .env files, ~/.ssh, ~/.git-credentials, fj's key file or any credential.
- Never `make publish*` or `make push`, never `docker push`.
- Never force-push. Never rewrite history on `develop` or `main`.
- No secrets in the repo. Config comes from flags/env; keep README.md and docs/config/ updated.
- If a task is ambiguous, ask instead of guessing.
