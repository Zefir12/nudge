# Code map

Where things are, so a session can open the right lines instead of whole files. Keep it
short: one entry per package, its entry points, and the recipes that come up again.
Update it when a package, entry point or recipe changes.

User docs: README.md has setup and flags/env. Each config topic has its own page in
docs/config/; grep there, not in README. The API reference is docs/api.md; the design
(environments, agent tunnel, auth) is docs/architecture.md.

## Request path
`main.realMain` picks the subcommand → `config.LoadServer` → `serve` runs
`prepareDataDir` and `store.Open` (migrations), `secretbox.LoadKey` + `registries.New`, `auth.New` (+ `EnsureAdmin`,
`RunCleanup`), `audit.New` (+ `RunCleanup`), `environments.New` (+ `EnsureLocal`), `notify.New` + `NewWatcher` (both `Run`, sharing one `events.Hub` with the API), `deployhooks.New` (+ `FailStale`, `Close` deferred), then builds
`api.New(api.Options{Auth: …, Envs: …, Web: web.Handler(web.Dist())})`
→ root `http.ServeMux` (wrapped in `proxies.handler`): `/api/` goes through `server.protect` (with an `Authorization`
header an API token, else the cross-origin check and a session; `server.authenticate`
lets patterns in `publicRoutes` through without either credential and refuses tokens on
`sessionOnlyRoutes`; `POST /api/v1/hooks/{id}` checks its webhook secret itself) to the API mux
(`/api/v1/...` handlers on `api.server`, `/api/` catch-all `notFound`); everything else
`web.Handler` (static file or index.html for SPA routes).

## Backend packages (backend/)
| Package | What | Entry points |
|---|---|---|
| `cmd/nudge` | subcommands, wiring, graceful shutdown | `realMain`, `serve`, `runAgent` |
| `internal/api` | HTTP API under /api/v1; resource lists/details in containers.go (create, spec and recreate in container_create.go), images.go (also pull stream, remove, prune), volumes.go and networks.go (also create, remove, prune); logs.go (container log WebSocket, `lineWriter`, `endStream`, `keepAlive`); stats.go (stats WebSocket, environment overview, `toStatsSample`); exec.go (exec terminal WebSocket, audited at upgrade with `auditNow`); events.go (events WebSocket on `events.Hub`); stacks.go (stack files, containers by service, up/down streamed as JSON lines, one change per stack via `lockStack`, `checkStack` runs `Options.StackChecker`); registries.go; registry_browse.go (repositories, tags, manifest and tag delete through `Options.RegistryClient`, a `RegistryBrowser`; `browseError` maps client errors; `requireAdmin` with `Options.Admin`); webhooks.go (notification targets, test delivery); updates.go (update check results and check now, container/stack update, update history; updates.go (update check results and check now, container/stack update, update history; recreate and update share `beginReplace`/`pullForReplace`/`finishReplace`, HTTP wrappers of `startReplace`/`pullWithAuth`/`completeReplace` mapped by `replaceError`; `updateContainerImage` and `pullAndDeploy` are the updates without HTTP, which deploy webhooks reuse; stacks are claimed and synced by `startStack`); deployments.go (deployment management; `deployTokenRoutes`/`tokenRoute`, the mux `protect` sends deploy tokens to; `deployDeployment` sets the run's variables through `startStackEdit` and restores .env on failure); deployhooks.go (deploy webhook management, the public trigger `triggerDeployHook`, `deployHook` the update a run makes, `deployError` its safe messages); audit.go (`serveAudited`, the one place writes are recorded, and `GET /audit`); proxy.go (`proxies.handler` wraps the root mux and resolves the client once per request from the peer or, behind `Options.TrustedProxies`, X-Forwarded-For/-Proto; `clientIP`, `isTLS`) | `New`, `Options`, `publicRoutes`, `server.protect`, `userFrom`, `server.decodeJSON`, `server.writeJSON`, `server.writeError`, `server.internalError`, `server.dockerCall`, `server.dockerAction` (204/409), `server.pathID`, `server.queryBool`/`queryInt`/`queryTime`, `auditTarget` (name a create's target for the audit log), `validCreate`/`createError` (volume and network create) |
| `internal/events` | Docker events fan-out: one Docker stream per watched environment, bounded buffer per subscriber, slow ones dropped; one reserved subscription per environment outside the limit (the notify watcher) | `New`, `Options`, `Hub.Subscribe`/`SubscribeReserved`, `Subscription.C`/`Err`/`Close`, `Event` |
| `internal/stacks` | compose stacks: files in `<dir>/<name>/` (`Dir`), `Runner` interface, `Local` runs the docker-compose binary with fixed arguments (`ExecFunc` fakes it in tests); `Checker` validates drafts with `docker-compose config` (check.go); credentials.go: registry logins for `up` (`Credential`, `CredentialFunc` as `LocalOptions.Credentials` or `Remote.WithCredentials`, temporary `DOCKER_CONFIG` under `LocalOptions.ConfigDir`, swept at start by `CleanConfigDir`, output masking); control.go: the agent's control service (`NewControl`, HTTP on control streams) and `Remote`, the runner that calls it | `ValidName`, `NewDir`, `Dir.Create`/`Update`/`Write`/`Read`/`Remove`, `Runner`, `NewLocal`, `LocalOptions`, `NewChecker`, `ErrNoCompose`, `NewControl`, `NewRemote`, `ErrBusy`, `ErrOldAgent`, `Credential`, `CleanConfigDir`; binds.go: `LocalOptions.Mounts` makes `Up` refuse binds the daemon can't mount from the same host path (`BindError`, 409 in the API); self.go: `SelfID` (nudge's container ID), `ContainerMounts` |
| `internal/updates` | image update checks: images containers run by tag, registry digest via the daemon's `DistributionInspect` vs. `RepoDigests`; bounded concurrency, per-registry gap, results in `image_updates`; `Run` is the scheduled check; history.go keeps the updates made (`update_history`, bounded per target) | `New`, `Options`, `Service.Check`/`CheckAll`/`List`/`Run`/`Record`/`History`, `DigestFor`, `ErrRunning`, `Result`, `Status`, `Change` |
| `internal/notify` | webhook notifications: targets in `webhooks` (URL and header value sealed with secretbox, the `events` each receives; webhooks.go), one delivery worker posting to `MaxDeliveries` targets at once with retries, dedup keys with an expiry in `notify_sent`, `Recover` sends a recovery only if its problem key is live and ends it (notify.go); `Watcher` (watch.go) reads each online environment's events through a reserved `events.Hub` subscription (exits, unhealthy/healthy), pings the environments `MaxPings` at once for `agent.offline`/`agent.online`; `UpdatesChecked` is `updates.Options.Checked` | `New`, `Options`, `Service.Send`/`Recover`/`Test`/`Run`/`UpdatesChecked`, `Events`, `DefaultEvents`, `Service.List`/`Create`/`Update`/`Delete`, `Message`, `DeliveryError`, `NewWatcher`, `WatchOptions`, `Watcher.Run` |
| `internal/deployhooks` | deploy webhooks (`deploy_hooks`, secret sealed with secretbox): create/list/delete/rotate with per-user ownership (`Actor`), `Authenticate` (bearer or HMAC signature headers, constant time), runs.go: one run per webhook plus one merged follow-up, per-webhook trigger limit, runs in `deploy_hook_runs` (bounded), `Wait` with the injected `Clock`, `Skipped` (tag filter for package events); the deploy is a `DeployFunc` the API passes | `New`, `Options`, `Service.Create`/`List`/`Get`/`Delete`/`Rotate`/`Authenticate`/`Trigger`/`Skip`/`Wait`/`Run`/`Runs`/`FailStale`/`Close`, `Skipped`, `Hook`, `Run`, `Outcome` |
| `internal/deployments` | deployments (`deployments`, `deployment_members`, `deployment_tokens`, `deployment_runs`): a stack CI deploys through; roles and actions (`Role.Allows`) with the one check `Authorize`; members with the last-owner rule; deploy tokens (tokens.go, `nudge_dt_`, scopes, `AuthenticateToken` reports the creator's role); runs.go: one run per deployment plus one queued, a newer request supersedes the queued one and takes its variables, `Wait`, `FailStale`, `Close`; env.go: `SetVariables` edits a stack's .env | `New`, `Options`, `Service.Create`/`Authorize`/`List`/`Change`/`Delete`/`Members`/`SetMember`/`RemoveMember`/`CreateToken`/`Tokens`/`DeleteToken`/`AuthenticateToken`/`Deploy`/`Wait`/`Run`/`Runs`, `SetVariables`, `Role`, `Action`, `Scope`, `Run`, `Outcome` |
| `internal/audit` | audit log entries (`audit_log`), list with cursor + filters, retention prune | `New`, `Options`, `Service.Record`, `List`, `Query`, `Prune`, `RunCleanup` |
| `internal/auth` | users, argon2id, sessions, login rate limit (`Attempt` lets other credential checks share the per-IP limit), API tokens (tokens.go) | `New`, `Options`, `Service.Login`, `SessionUser`, `TokenUser`, `CreateToken`, `CreateFirstUser`, `EnsureAdmin`, `Attempt`, `RunCleanup` |
| `internal/docker` | Docker client over a dialer per kind; `Client` interface handlers use; `dockertest.Fake` | `New`, `LocalDialer`, `Client`, `Unreachable` |
| `internal/environments` | environments table, Docker client per environment (cached), online checks, auto-created `local`; agent enrollment in agents.go (join tokens, agent ID + secret); agent tunnels in sessions.go (one per environment, Docker client dials a stream, `ControlDialer` a control stream) | `New`, `Options.Connect`, `LocalConnect`, `EnsureLocal`, `Service.Client`, `Online`, `CreateAgent`, `RegenerateJoinToken`, `Enroll`, `AuthenticateAgent`, `Revoke`, `ServeAgent` |
| `internal/tunnel` | WebSocket ↔ yamux tunnel: JSON hello (protocol version; the server also takes protocol 1 agents), server = yamux client, keepalives; protocol 2 streams start with a `Kind` byte (Docker or control) | `Accept` (server), `Connect` (agent), `Session.Open`/`OpenControl`/`Accept`/`Protocol`/`Done`/`Close`, `ReadKind`, `ProtocolVersion`, `ErrNoControl`; `tunneltest.Connect`/`ConnectConfig` fake agent serving an `http.Handler` on every stream (`Config.Protocol` acts as an older agent) |
| `internal/agent` | `nudge agent`: enroll with the join token, `agent.json` (credentials.go), dial the tunnel with backoff, pipe each Docker stream to the Docker socket, control streams to `Options.Control` | `New`, `Options`, `Agent.Run`, `FatalError`, `Clock` |
| `internal/certs` | TLS certificate from PEM files, reloaded on file change (polled) or SIGHUP | `Load`, `Loader.GetCertificate`, `Reload`, `Watch` |
| `internal/config` | flags with NUDGE_* env fallbacks | `LoadServer`, `Server` (`TLS()`, `PublicURL`, `TrustedProxies`), `LoadAgent`, `Agent` |
| `internal/registries` | registry credentials for pulls (password sealed with secretbox), image host → credentials; `kind` (generic/forgejo), Forgejo `owners`, `plain_http` for browsing | `New`, `Options`, `Service.AuthFor`, `Service.AuthsFor` (a stack's registries), `Service.Credentials`, `HostOf`, `NormalizeAddress`, `ValidOwner`, `Kind` |
| `internal/registryclient` | browses registries over HTTP (stdlib): v2 token/Basic auth with a bounded token cache (auth.go), realm and redirect rules, capped bodies, bounded paging (client.go); catalog, Forgejo packages listing, tags, manifest detail, shared tags, delete (ops.go); name grammar (names.go) | `New`, `Options`, `Registry`, `Client.Repositories`/`Tags`/`Manifest`/`Digest`/`SharedTags`/`Delete`, `ValidRepository`/`ValidTag`/`ValidDigest`, `Err*` sentinels; tests: `newFakeRegistry` (fake_test.go: v2 + token server + Forgejo packages API over httptest) |
| `internal/secretbox` | AES-256-GCM for secrets nudge must read back; key in `<data>/secret.key` | `LoadKey`, `New`, `Box.Seal`/`Open` |
| `internal/store` | SQLite (`<data>/nudge.db`), embedded migrations | `Open`, `Store.DB`, `migrations/NNNN_name.sql` |
| `internal/web` | embedded frontend, SPA fallback | `Dist`, `Handler` |
| `internal/version` | build version via ldflags | `Version` |

## Frontend (frontend/src)
| Path | What |
|---|---|
| `main.ts`, `App.vue` | app setup, layout (top bar with `AppBreadcrumbs`, `AppSidebar`, `RouterView`; public routes render without it); look and layout in docs/design.md |
| `router.ts` | routes (add one per view; `meta.public` for no-login pages), `createAppRouter` with the auth guard, `redirectOnUnauthorized` |
| `session.ts` | `currentUser`, `loadUser`, `safeRedirect` |
| `environments.ts` | `environments`, `loadEnvironments`, last used environment (localStorage), `defaultEnvironment` |
| `composables/` | `useLoad(source, fetch)` (data/error/loading/reload, refetch on route change), `useParams()` (`:env`, `:id`), `useEvents(env, types, onEvent)` (event stream with reconnect backoff), `useLiveReload(env, types, reload)` (debounced reload on events; the list views use it), `useUpdates(env)` (last update check, `updateFor(container)`, `check()`) |
| `format.ts` | `formatDate`, `formatBytes`, `shortId`, `formatPort`, `matches` (search), `parsePairs` (key=value lines for labels and options), `timeKey` (sortable Docker timestamps) |
| `compose.ts` | `readCompose` (YAML syntax errors and service names, with the `yaml` package) |
| `api/client.ts` | `apiFetch<T>` (cookies, 401 → `setUnauthorizedHandler`), `ApiError` (with `fields` for per-field 400s), one typed function per endpoint; `pullImage` reads the JSON-lines stream, `streamLogs`/`streamStats`/`streamEvents` open the log, stats and events WebSockets (`openStream`), `openExec` the exec terminal (binary frames) |
| `test-utils.ts` | `stubApi({'GET /path': {status, body}})` fetch stub (`raw` for a non-JSON body such as a pull stream), `admin` user, `stubSockets` (`FakeWebSocket` for WebSocket streams) |
| `views/` | one component per route (`ContainerFormView` serves both new and edit/recreate; new stacks are at `/env/:env/new-stack`, as `new` is a valid stack name; a registry's repository is `/registries/:id/repository?name=`, as names contain `/`) |
| `components/` | reusable pieces: `AppSidebar` (global part: account, `EnvSwitcher`, global pages; per-environment part for the open or last used environment), `AppBreadcrumbs` (Dashboard / environment / section), `EnvironmentTile` (one environment on the global dashboard, Docker info when online), `BrandLogo` (inline nudge lockup, colours from the theme tokens, size via `--logo-height`; source SVGs in docs/brand/), `LoadState` (loading/empty/error + retry), `KeyValues`, `StateBadge`, `EnvSwitcher`, `EnvStatus` (online/offline/waiting badge + last seen), `ContainerActions` (start/stop/restart/kill/remove buttons with confirms), `ContainerLogs` (log viewer: tail, timestamps, search, pause, download), `ContainerStats` (live usage with `Sparkline` SVG charts), `ContainerTerminal` (xterm.js exec terminal; its test mocks `@xterm/xterm`), `EnvironmentUsage` (dashboard overview, refreshed every 10 s), `ImagePull` (pull form + per-layer progress), `ContainerForm` (container spec form with per-field errors), `VolumeCreate`/`NetworkCreate` (create forms), `StackEditor` (compose.yaml + .env with YAML and compose errors, `validate()`), `StackLogs` (one log stream per container, merged by time), `JoinTokenCard` (one-time agent command and compose file, each with copy), `UpdateBadge` (update available, digests in the title), `UpdateHistory` (a container's or stack's updates with the image before and after; `reload()` exposed), `WebhooksCard` (a container's or stack's deploy webhooks: create, rotate, delete, one-time secret with Forgejo snippets, runs on demand) |
| `style.css` | design tokens (CSS variables, dark only), self-hosted fonts (`assets/fonts/`), base styles and shared classes (`.card`, `.lamp`, `.chip`, `.eyebrow`); see docs/design.md |

## Recipes
- **New API endpoint:** handler method on `api.server` in a file per area
  (`internal/api/containers.go`), route in `api.New`, table test next to it with
  `httptest`, row in docs/api.md, typed function in `frontend/src/api/client.ts`.
- **New view:** `frontend/src/views/FooView.vue`, route in `router.ts` (environment pages
  under `/env/:env/...`, loading with `useLoad` + `LoadState`), nav link in `AppSidebar.vue` (per-environment `sections` or `globalItems`) and a crumb in `AppBreadcrumbs.vue`, `FooView.test.ts` stubbing the API with `stubApi`; views that use the
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
  httptest server, `connect`/`waitOnline`; `newAgentTestOpts` with API options) and `fakeDockerAPI`;
  api/agent_stacks_test.go has `newAgentStackTest` (stacks deployed through the tunnel to a
  fake agent's control service); environments/sessions_test.go
  has `newSession` (server side of a tunnel) for `ServeAgent` tests.
- **End-to-end test:** internal/e2e wires the server like `serve` (`newServer`, httptest)
  and runs a real `agent.Agent` against a fake Docker API on a unix socket (`fakeDocker`);
  `conns.drop` cuts the agent's connections, `instantClock` skips the backoff.
- **Registry browsing tests:** handlers use `newBrowseTest` in api/registry_browse_test.go
  (`fakeBrowser` as `Options.RegistryClient`, `admin` sets `Options.Admin`); the client
  is tested against `newFakeRegistry` in registryclient/fake_test.go.
- **Deployment tests:** deployments/deployments_test.go has `newDepTest` (alice, bob, an
  admin, one environment, `fakeClock`) and `deployer` (a `DeployFunc` stepped through
  `started`/`release`); api/deployments_test.go has `newDeploymentTest` (stack web with
  a .env, deployment shop) and `deployToken`.
- **Deploy webhook tests:** deployhooks/deployhooks_test.go has `newHookTest` (two users,
  one environment, `fakeClock` whose `After` channel the test fires) and `deployer` (a
  `DeployFunc` the test steps through `started`/`release`); api/deployhooks_test.go has
  `newDeployHookTest` (container web with a newer image to pull), `createHook`, `trigger`
  and `signBody`.
- **Test helpers:** `api.newTestHandler(t)` (api_test.go: empty database, returns the
  `auth.Service`; create a user with `CreateFirstUser`, get a cookie from a login via
  `do`/`sessionCookieOf` in auth_test.go); `auth.newTestService(t)` with `fakeClock`; `fstest.MapFS` for web
  handler tests; `realMain` with a fake `getenv` and buffers for CLI tests;
  `store.Open(ctx, t.TempDir())` for a migrated database; `dockertest.Fake` as a Docker
  client and `api.newTestHandlerWith(t, connect)` to hand it to environments (see
  api/environments_test.go); main tests pass `-docker-host noDocker(t)` so serve never
  reaches the host's daemon.
- **New Docker call:** add the method to `docker.Client` and `dockertest.Fake` (resource
  fixtures live in `dockertest.Resources`; container, image, volume and network changes are recorded in
  `Fake.Actions` and fail with `ActionErr`, or one step with `FailAction`); a handler that answers with one call uses
  `s.dockerCall` (environment lookup, timeout, 404 on `docker.NotFound`, `s.dockerError`), one that changes an object `s.dockerAction` (204, 409 on a conflict);
  validate path IDs with `s.pathID`. Tests: `newResourceTest` in api/resources_test.go; WebSocket handlers: `newLogsTest` in api/logs_test.go (httptest server, `dial` with the session cookie). `Fake.Logs` holds raw log streams; a followed one blocks until cancelled and reports it on `LogsCancelled`. `Fake.Stats` and `StatsCancelled` do the same for stats; `newStatsTest` in api/stats_test.go. `Fake.Distribution` holds registry digests by reference (`DistributionCalls` records the auth sent). `Fake.EventsResult` is the event stream (`newEventsTest` in api/events_test.go). `Fake.ContainerList` filters by `label`. Stack handlers: `newStackTest` in api/stacks_test.go (`fakeRunner` with `onRun` to change `stackTest.fake` mid-run, `fakeChecker`; options via `newEnvTestOpts`/`newTestHandlerOpts`). `Fake.Execs` hands each attached exec's container end (`dockertest.Exec.Conn`, a pipe) to the test; `newExecTest` in api/exec_test.go. Tests of views that use `useLiveReload` call `stubSockets()`.
