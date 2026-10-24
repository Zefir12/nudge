# Code map

Where things are, so a session can open the right lines instead of whole files. Keep it
short: one entry per package, its entry points, and the recipes that come up again.
Update it when a package, entry point or recipe changes.

User docs: README.md has setup and flags/env. Each config topic has its own page in
docs/config/; grep there, not in README. The API reference is docs/api.md; the design
(environments, agent tunnel, auth) is docs/architecture.md.

## Request path
`main.realMain` picks the subcommand → `config.LoadServer` → `serve` runs
`prepareDataDir` and `store.Open` (migrations), `auth.New` (+ `EnsureAdmin`,
`RunCleanup`), `environments.New` (+ `EnsureLocal`), then builds
`api.New(api.Options{Auth: …, Envs: …, Web: web.Handler(web.Dist())})`
→ root `http.ServeMux`: `/api/` goes through `server.protect` (with an `Authorization`
header an API token, else the cross-origin check and a session; `server.authenticate`
lets patterns in `publicRoutes` through without either credential) to the API mux
(`/api/v1/...` handlers on `api.server`, `/api/` catch-all `notFound`); everything else
`web.Handler` (static file or index.html for SPA routes).

## Backend packages (backend/)
| Package | What | Entry points |
|---|---|---|
| `cmd/nudge` | subcommands, wiring, graceful shutdown | `realMain`, `serve`, `runAgent` |
| `internal/api` | HTTP API under /api/v1; resource lists/details in containers.go, images.go, volumes.go, networks.go | `New`, `Options`, `publicRoutes`, `server.protect`, `userFrom`, `server.decodeJSON`, `server.writeJSON`, `server.writeError`, `server.internalError`, `server.dockerCall`, `server.pathID` |
| `internal/auth` | users, argon2id, sessions, login rate limit, API tokens (tokens.go) | `New`, `Options`, `Service.Login`, `SessionUser`, `TokenUser`, `CreateToken`, `CreateFirstUser`, `EnsureAdmin`, `RunCleanup` |
| `internal/docker` | Docker client over a dialer per kind; `Client` interface handlers use; `dockertest.Fake` | `New`, `LocalDialer`, `Client`, `Unreachable` |
| `internal/environments` | environments table, Docker client per environment (cached), online checks, auto-created `local`; agent enrollment in agents.go (join tokens, agent ID + secret); agent tunnels in sessions.go (one per environment, Docker client dials a stream) | `New`, `Options.Connect`, `LocalConnect`, `EnsureLocal`, `Service.Client`, `Online`, `CreateAgent`, `RegenerateJoinToken`, `Enroll`, `AuthenticateAgent`, `Revoke`, `ServeAgent` |
| `internal/tunnel` | WebSocket ↔ yamux tunnel: JSON hello (protocol version), server = yamux client, keepalives | `Accept` (server), `Connect` (agent), `Session.Open`/`Accept`/`Done`/`Close`, `ProtocolVersion`; `tunneltest.Connect` fake agent serving an `http.Handler` |
| `internal/agent` | `nudge agent`: enroll with the join token, `agent.json` (credentials.go), dial the tunnel with backoff, pipe each stream to the Docker socket | `New`, `Options`, `Agent.Run`, `FatalError`, `Clock` |
| `internal/config` | flags with NUDGE_* env fallbacks | `LoadServer`, `Server`, `LoadAgent`, `Agent` |
| `internal/store` | SQLite (`<data>/nudge.db`), embedded migrations | `Open`, `Store.DB`, `migrations/NNNN_name.sql` |
| `internal/web` | embedded frontend, SPA fallback | `Dist`, `Handler` |
| `internal/version` | build version via ldflags | `Version` |

## Frontend (frontend/src)
| Path | What |
|---|---|
| `main.ts`, `App.vue` | app setup, layout (sidebar with `EnvSwitcher`, per-environment nav, user + logout, `RouterView`; public routes render without it) |
| `router.ts` | routes (add one per view; `meta.public` for no-login pages), `createAppRouter` with the auth guard, `redirectOnUnauthorized` |
| `session.ts` | `currentUser`, `loadUser`, `safeRedirect` |
| `environments.ts` | `environments`, `loadEnvironments`, last used environment (localStorage), `defaultEnvironment` |
| `composables/` | `useLoad(source, fetch)` (data/error/loading/reload, refetch on route change), `useParams()` (`:env`, `:id`) |
| `format.ts` | `formatDate`, `formatBytes`, `shortId`, `formatPort`, `matches` (search) |
| `api/client.ts` | `apiFetch<T>` (cookies, 401 → `setUnauthorizedHandler`), `ApiError`, one typed function per endpoint |
| `test-utils.ts` | `stubApi({'GET /path': {status, body}})` fetch stub, `admin` user |
| `views/` | one component per route |
| `components/` | reusable pieces: `LoadState` (loading/empty/error + retry), `KeyValues`, `StateBadge`, `EnvSwitcher` |
| `style.css` | design tokens (CSS variables, light/dark) and base styles |

## Recipes
- **New API endpoint:** handler method on `api.server` in a file per area
  (`internal/api/containers.go`), route in `api.New`, table test next to it with
  `httptest`, row in docs/api.md, typed function in `frontend/src/api/client.ts`.
- **New view:** `frontend/src/views/FooView.vue`, route in `router.ts` (environment pages
  under `/env/:env/...`, loading with `useLoad` + `LoadState`), nav link in `App.vue`, `FooView.test.ts` stubbing the API with `stubApi`; views that use the
  router mount with `createAppRouter(createMemoryHistory())` as a plugin.
- **New config option:** field + flag in `internal/config`, test row in
  `config_test.go`, README table, docs/config/<topic>.md.
- **Schema change:** new `internal/store/migrations/NNNN_name.sql` with the next number
  (never edit an applied one); it runs in one transaction at start.
- **Tunnel tests:** `newWSPair` (httptest + WebSocket) and `newPipePair` (`net.Pipe`) in
  tunnel_test.go, `echo` as a fake agent, `checkLeaks` (non-parallel tests only).
- **Agent command tests:** agent/agent_test.go has `newFakeServer` (enroll + tunnel
  endpoint over httptest, sessions on a channel), `fakeDocker`, `start` (runs an agent
  with a `fakeClock` that records backoff delays) and `get` (HTTP over a stream).
- **Agent environment tests:** `tunneltest.Connect` dials a server and serves a fake
  Docker HTTP API on each stream; api/connect_test.go has `newAgentTest` (enrolled agent,
  httptest server, `connect`/`waitOnline`) and `fakeDockerAPI`; environments/sessions_test.go
  has `newSession` (server side of a tunnel) for `ServeAgent` tests.
- **Test helpers:** `api.newTestHandler(t)` (api_test.go: empty database, returns the
  `auth.Service`; create a user with `CreateFirstUser`, get a cookie from a login via
  `do`/`sessionCookieOf` in auth_test.go); `auth.newTestService(t)` with `fakeClock`; `fstest.MapFS` for web
  handler tests; `realMain` with a fake `getenv` and buffers for CLI tests;
  `store.Open(ctx, t.TempDir())` for a migrated database; `dockertest.Fake` as a Docker
  client and `api.newTestHandlerWith(t, connect)` to hand it to environments (see
  api/environments_test.go); main tests pass `-docker-host noDocker(t)` so serve never
  reaches the host's daemon.
- **New Docker call:** add the method to `docker.Client` and `dockertest.Fake` (resource
  fixtures live in `dockertest.Resources`); a handler that answers with one call uses
  `s.dockerCall` (environment lookup, timeout, 404 on `docker.NotFound`, `s.dockerError`);
  validate path IDs with `s.pathID`. Tests: `newResourceTest` in api/resources_test.go.
