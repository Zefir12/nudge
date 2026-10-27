# Architecture

## Goals
- `docker run` with the socket mounted and a volume gives a working UI; the first visit
  creates the admin. No config file needed.
- Remote Docker hosts from the start, through an agent that needs no inbound port.
- One public API that the web UI and scripts share.

## Components

```
browser ──HTTPS──▶ nudge serve ──unix socket──▶ local dockerd
                       ▲
                       │ WebSocket (agent dials out), yamux streams
                       │
                  nudge agent ──unix socket──▶ remote dockerd
```

- **Server** (`nudge serve`): API under `/api/v1`, embedded web UI, SQLite store in
  `/data/nudge.db`, the registry of connected agents.
- **Agent** (`nudge agent`): same binary and image, on each remote host with that host's
  socket mounted. It dials the server; it has no API of its own and stores only its
  credential in `/data`.
- **Environment**: one Docker host the server manages. Kinds: `local` (a socket on the
  server's host, created on first start if `/var/run/docker.sock` is reachable) and
  `agent`. Every Docker route is scoped to one: `/api/v1/environments/{env}/...`.

## Docker access
`internal/docker` builds one `*client.Client` (github.com/moby/moby/client) per
environment. The only difference between kinds is the dialer:
- local: dial the unix socket;
- agent: open a new yamux stream on that agent's session.

So every Docker feature (lists, pull with progress, logs, attach/exec hijacking, events)
works the same for remote hosts without a per-feature agent protocol. Handlers use a
small interface over the client, so tests run against fakes.

## Agent tunnel (`internal/tunnel`, `internal/agent`)
1. Admin creates an agent environment in the UI. The server stores a **join token**
   (random 32 bytes, stored as a SHA-256 hash, single use, expires after 24 h) and shows the
   `docker run` command for the agent.
2. The agent sends the join token to `POST /api/v1/agents/enroll` (plain HTTPS, before
   any WebSocket). The server answers with an agent ID and a long-lived **agent secret**
   (hashed on the server); the agent saves both to `/data/agent.json` (0600) and forgets
   the join token. Then it connects to `wss://<server>/api/v1/agents/connect`.
3. Every connect authenticates with ID + secret (HTTP Basic auth on the upgrade request).
   A second connection for the same agent replaces the first. Revoking the environment
   (or enrolling it again) deletes the old secret and drops the session. The sessions
   live in `environments.Service` (`ServeAgent`), one per environment; the agent
   environment's Docker client dials a new yamux stream on the current session per
   connection, so a reconnect needs no new client.
4. After the handshake the WebSocket is wrapped in a `net.Conn` and a yamux session where
   the **server is the yamux client**: it opens streams, and the agent accepts each one and
   pipes it to its local Docker socket. A small JSON hello at connect carries the agent's
   version and host name; liveness uses yamux keepalives.
   Wire format (`internal/tunnel`): the agent sends one text message
   `{"protocol": 2, "version": "...", "hostname": "..."}`, the server answers
   `{"protocol": 2, "version": "..."}` (in the agent's protocol for an agent on 1) or
   `{"protocol": 2, "error": "..."}` and closes (policy violation) on an unsupported
   protocol or invalid fields; after that every message is binary yamux data. On protocol
   2 every stream starts with a kind byte: `0x00` Docker, `0x01` control. Keepalive pings every 15 s, the session closes when one isn't
   answered within 10 s. Streams opened by the agent are refused.
5. The agent reconnects with exponential backoff and jitter (1 s → 60 s). The server marks
   the environment offline when the session closes and records `last_seen`.

Trust: the server has full Docker access to every agent's host (that is the point); an
agent can reach nothing on the server except its own tunnel endpoint. The server must
use TLS for agents on untrusted networks (`NUDGE_TLS_*` or a reverse proxy); the agent
refuses `ws://` unless `NUDGE_INSECURE=1`.

## Auth
- Users with argon2id password hashes. First start with no users: the API only allows
  `GET /health`, `GET /setup` and `POST /setup` (create the admin), unless
  `NUDGE_ADMIN_PASSWORD` creates it at startup.
- Browser: session cookie (`HttpOnly`, `Secure` behind TLS, `SameSite=Strict`), sessions
  in SQLite with expiry; writes additionally require a matching `Origin`.
- Scripts: API tokens (`Authorization: Bearer nudge_...`), stored hashed, shown once.
- Decision (#33): routes that mint a credential which outlives the token's revocation
  (creating API tokens) are **session-only**, not scoped: a token request gets `403`,
  audited under the token's name. They are listed in `sessionOnlyRoutes` in
  `internal/api` and enforced in the auth middleware; a new route of that kind goes on
  the list. Scopes stay a later option.
- Login attempts are rate-limited per IP and username, with bounded memory.
- Roles (#111): every user is an `admin` or a `user` (`users.role`; users created before
  roles are admins, setup and `NUDGE_ADMIN_PASSWORD` create admins). One place decides:
  `userRoutes` in `internal/api` lists the routes open to users, and the auth middleware
  answers every other authenticated route with `403 admins only` (audited) for a user's
  session or API token, so a new route is admin-only until it is listed. The routes open
  to users check the object themselves (deployments: `deployments.Authorize` with the
  user's role on the deployment). `TestRoutesForUsers` walks every registered route. An
  API token has its owner's role; a deploy token or deploy webhook acts for its creator
  and stops while they are disabled. A disabled user's sessions end and their
  credentials stop working; the last enabled admin can't be demoted, disabled or
  deleted (checked in the writing statement). SSO users get their role from the
  provider on each login.
- Ownership (#112): a user sees and touches only what is theirs, so a user can't read
  an admin's logs or exec into another tenant's database. Two checks, both in
  `internal/api`: `server.environment` answers `404` for an environment the user wasn't
  given (`user_environments`, set by an admin), and `scope` (ownership.go) decides which
  objects are theirs: those labelled `net.zefirlabs.nudge.owner=<owner key>`. The owner
  key is random per user (`users.owner_key`) because SQLite reuses the newest deleted
  user's id. Lists are filtered by the label in the Docker call and checked again;
  `{id}` routes inspect first and answer someone else's object as `404` (the daemon is
  then called with the inspected ID, not the name). A container also needs an image
  without the owner label, since Docker merges image labels into the container's. The
  event stream checks each container once (bounded cache per stream) and drops events
  that can't be checked. Admins skip the checks (no extra Docker calls). The label
  namespace is reserved: requests can't set it, a recreate keeps it. Creating objects as
  a user (#114, #115) uses `server.ownerLabels`.
- Tenant builder (#115, api/tenant.go): a user's container is built by the server from a
  short strict spec (`tenantSpec`, unknown JSON fields refused), never from Docker's own
  options, so privileged mode, binds, the host's namespaces, devices or the socket can't
  be asked for at all. `buildTenant` is a pure function that adds the fixed hardening
  (`NET_RAW` dropped, `no-new-privileges`, pids limit, log limits, init, private IPC) and
  the owner label last. Volumes and networks must carry the user's owner label; images
  are pulled without stored credentials, and an image with an uncovered `VOLUME` or a
  reserved label is refused. `replace` (recreate, image update, deploy webhooks) rebuilds
  any user's container through the builder from its settings read back as a spec
  (`toTenantSpec`), so a setting the builder doesn't make can't survive a replace; the
  admin spec is refused on a user's container.
- Single sign-on (#110, internal/oidc): one OpenID Connect provider, nudge a confidential
  client (authorization code + PKCE S256, `client_secret_basic`). Discovery, JWKS and ID
  token checks are go-oidc's (RS256 only, issuer, audience, expiry against the injected
  clock, one JWKS refetch on an unknown kid); nudge checks the nonce, `azp`, `iat`, the
  admin or user role in `roles` and, optionally, `mfa` in `amr`. Discovery is lazy (nudge starts
  while the provider is down) and retried at most every 30 s. Started logins live
  server-side in a bounded map (10 minutes, keyed by state) and are bound to the browser
  by a `SameSite=Lax` cookie; each state is used once. Every provider call has a timeout
  and a capped body. Users are matched by issuer + `sub`, never by name or email; they
  have no password, and their sessions are nudge sessions capped by
  `NUDGE_OIDC_SESSION_TTL`. With SSO, setup is off; `NUDGE_PASSWORD_LOGIN=false` keeps
  password login only for the local break-glass `admin`.
- Deploy webhooks (`POST /api/v1/hooks/{id}`, internal/deployhooks): a public route whose
  credential is the webhook's own secret, as a bearer token or as the key of an
  HMAC-SHA256 body signature (Forgejo, Gitea, GitHub headers), compared in constant time.
  The secret is sealed with the secretbox key (not hashed: a signature check needs its
  value). A webhook can do one thing, update its one target, and runs as its creator;
  failed checks count against the login limit per IP, and each webhook takes 10 triggers
  a minute. This is the credential to put in CI instead of an API token, which is its user
  on every route.
- Deployments (internal/deployments) have their own access model, so it can grow (teams,
  environment roles) in one place: members with a role (viewer < deployer < maintainer <
  owner, admins above all), and one check, `Service.Authorize(actor, name, action)`,
  that every deployment handler calls; `Role.Allows` maps roles to actions. Deploy
  tokens (`nudge_dt_`, hashed, scoped `deploy` / `runs:read`) never reach the API mux:
  `server.protect` sends any request carrying one to a mux with only the deployment's
  `deploys` routes, which check the token against the deployment in the path, its
  scopes and its creator's current role.

## Stacks (phase 5)
Decision (approved by zefir for #25): **compose runs next to the daemon it deploys to**: in
the server for the local environment, in the agent for agent environments, with the same
code. The server keeps the authoritative files and sends them to the agent on every
deploy; the agent gets a small control service on its tunnel, the one exception to "the
agent is a dumb pipe".

### Where compose can run
| | A: on the server, through the tunnel | B: on the agent, via a control endpoint |
|---|---|---|
| Relative bind mounts (`./data:/data`) | resolved on the server, then mounted by the remote daemon from a path that doesn't exist there: broken | resolved in the agent's copy of the project, on the host that mounts it: works (with the same-path mount below) |
| `env_file`, `.env`, interpolation | read on the server: works, but only files the server holds | read from the agent's copy: works |
| `configs`/`secrets` with `file:` | bind-mounted outside swarm, so broken like relative binds | works |
| Build contexts | the context is tarred on the server and sent through the tunnel: works, but every build crosses the tunnel | the files are synced first (size limit), the build runs against the local daemon; git URLs work in both |
| Agent | stays a dumb pipe | gets a compose binary and a control service; needs the protocol bump below |
| Implementations | one | one, run in two places |

A keeps the agent simple but breaks the most common compose layouts (relative data and
config mounts), and nothing can fix that short of copying files to the remote host,
which is B. So B.

The same problem exists locally: nudge runs in a container, so `./data` in
`/data/stacks/local/web/` is a path inside nudge's container, and the daemon mounts that
path from the host. Whatever runs compose (server or agent) keeps stacks in a directory
**mounted at the same path on the host and in the container** (`NUDGE_STACKS_DIR`,
default `/data/stacks`; the deploy docs show `-v /opt/nudge/stacks:/opt/nudge/stacks
-e NUDGE_STACKS_DIR=/opt/nudge/stacks`). Without that mount relative binds are refused
at deploy with a clear error rather than mounting an empty host directory.

### Running compose
The image is distroless with no Docker CLI. Options (zefir approved 1):
1. Ship the static `docker-compose` binary (compose v2 standalone, ~60 MB) in the image
   and run it with fixed arguments: `docker-compose -p <name> --project-directory <dir>
   -f compose.yaml up -d --remove-orphans [--pull always]`, `down [--volumes]`,
   `config --quiet` (validate). `DOCKER_HOST` is the local socket.
2. Import `github.com/docker/compose/v2` as a library: no subprocess, but a very large
   dependency tree (buildkit, containerd) in every build.

Recommended: 1. It's isolated, its version can move independently, and its output is
what users know from the CLI. nudge parses only the exit code; output lines are streamed
to the UI as they are.

### Files
- Server: `<stacks dir>/<env>/<name>/` holds `compose.yaml`, `.env` (variables set in the
  UI) and any extra files the user adds. It is the source of truth: the UI edits it,
  and the audit log records every change.
- Agent: `<stacks dir>/<name>/`, a copy replaced on each deploy. Names follow compose's
  project name rules (`[a-z0-9][a-z0-9_-]*`).
- Status (services, containers, health) is read through the Docker API by the
  `com.docker.compose.project` label, the same for both kinds; logs use the existing
  container log stream. No control endpoint is needed for reads.

### Agent control protocol
- Tunnel protocol 2: every stream the server opens starts with one byte, `0x00` Docker
  socket or `0x01` control. The hello carries the protocol; with an agent on protocol 1
  the server opens plain Docker streams and stacks on that environment answer `409
  "update the agent to use stacks"`. Protocol 1 agents keep working for everything else.
- Control streams carry HTTP/1.1: the agent serves a `net/http` handler on a listener fed
  by control streams, and the server uses an `http.Client` whose dialer opens one. Only
  the server can reach it (the tunnel is already authenticated); there is no listening
  port.
- Endpoints:
  - `PUT /v1/stacks/{name}`: body is `{"compose": "...", "env": "..."}` (at most 2 MB),
    each file written to a temporary file and swapped in with a rename. (A tar of extra
    project files can come later, when the UI edits them.)
  - `POST /v1/stacks/{name}/up` `{"pull": bool, "credentials": [{"address", "username",
    "password"}]}`: `credentials` (left out when there are none, so older agents still
    deploy public images) are the registry logins the server picked for the stack; runs
    compose, answers NDJSON
    `{"stream": "stdout"|"stderr", "line": "..."}` frames, then `{"exit_code": N}`.
    Cancelling the request kills the process.
  - `POST /v1/stacks/{name}/down` `{"volumes": bool}`: the same output format.
  - `up` also takes `"wait_seconds"` (left out when zero), compose's `--wait
    --wait-timeout`. An agent from before it refuses the field with `400`, and the server
    deploys again without waiting and warns.
  - `POST /v1/stacks/{name}/probe` `{"container", "port", "path", "status", "timeout"}`
    (timeout in nanoseconds): a deployment's HTTP check. The agent starts a helper
    container from its own image with `network_mode: container:<id>` running `nudge
    probe` against `127.0.0.1`, and answers `{"failed": "..."}` (empty when it passed);
    `409` when the agent doesn't run in a container of that daemon. An agent from before
    it answers `404`, and the server skips the check with a warning. The local
    environment runs the same helper through the server's own container.
  - `DELETE /v1/stacks/{name}`: removes the files after `down`.
  - Not built yet: `GET /v1/stacks` with names and a SHA-256 of each project's files, so
    the server can show a stack that differs from what it last sent.
- One deploy per stack at a time (the server serializes per environment and name; the
  agent refuses a second one with `409`).
- Security: no new privilege (the server can already do anything on the remote daemon),
  but the agent runs only the compose binary with fixed arguments, never a shell, and
  validates names and tar entries before writing.

In code: a `stacks.Runner` interface (`Sync`, `Up`, `Down`, `Remove`) with a local
implementation (`stacks.Local`: writes the files, runs compose) used by the server for the
local environment and by the agent's control service (`stacks.NewControl`), and
`stacks.Remote`, which calls the control endpoints over `environments.ControlDialer`.

Registry credentials: compose otherwise runs with only `DOCKER_HOST`, `HOME` and `PATH`,
so for each `up` the runner that runs compose writes the credentials of the registries the stack
may need to a `config.json` (0600) in a new private temporary directory (0700) under
`<data>/compose-config` (0700), sets `DOCKER_CONFIG` to it and removes it when compose
exits; `serve` and `agent` remove the directories a killed run left there at start (and
log how many). On an agent environment the server picks the credentials and sends them
with the `up` request; the agent writes the same temporary config. An agent from before
this answers such an `up` with `400`, which the server reports as "the agent is too old
for registry credentials; update it" (`stacks.ErrOldAgent`).
- Which credentials (`registries.Service.AuthsFor`): Docker Hub's on every `up` when
  they are stored (images without a host use them), and those of each registry whose
  host compose.yaml or `.env` names. The match is a text scan of both files, comments
  included, not a parse of the images. So anyone who can edit a stack can make nudge
  hand any stored registry's credentials to that environment's compose (and, on an agent
  environment, to its agent). That is no more than deploy access already gives: such a
  user can already pull any image with the stored credentials, and image pulls send
  them through the tunnel (`X-Registry-Auth`) anyway.
- Passwords and their base64 `auth` are masked (`********`) in compose's output,
  longest secret first, so a short password can't break up an `auth` and leave part of
  it visible. Masking works line by line: a password containing a newline isn't
  masked. `stacks.Credential` never logs or prints its password.
- Credential helpers are not supported.

Out of scope for phase 5: swarm stacks, compose `build` with large contexts (git URLs
are fine), and editing a project's extra files beyond compose.yaml and `.env` in the UI.

## Storage
SQLite (`modernc.org/sqlite`, pure Go, so the image stays static) with numbered SQL
migrations embedded in the binary and applied at start. One writer connection; WAL mode.
