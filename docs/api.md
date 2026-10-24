# API

Base path `/api/v1`. JSON in and out; errors are `{"error": "message"}` with a matching
status code. Unknown paths and wrong methods under `/api/` return a JSON 404 to a
logged-in client and a JSON 401 otherwise.
Streams (logs, exec, events, agent tunnel) use WebSocket.

Auth: the `nudge_session` cookie set by login or setup, or `Authorization: Bearer <token>`
with an API token (see Tokens). When an `Authorization` header is present it alone
decides; the cookie is ignored. Routes marked *public* need neither; every other route
answers `401 {"error":"authentication required"}` without a valid session or token.

Cross-origin writes are refused with `403`: a `POST`/`PUT`/`PATCH`/`DELETE` must carry
`Sec-Fetch-Site: same-origin`, or an `Origin` whose host matches `Host`, or neither header
(non-browser clients). Requests with an `Authorization` header skip this check.

## Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/api/v1/health` | public | `{"status":"ok","version":"v1.2.3"}` |
| GET | `/api/v1/setup` | public | `{"needed":true}` while no user exists |
| POST | `/api/v1/setup` | public | create the admin and log in; see below |
| POST | `/api/v1/auth/login` | public | log in; see below |
| POST | `/api/v1/auth/logout` | public | end the session and clear the cookie; `204` (a no-op for API tokens) |
| GET | `/api/v1/auth/me` | session | the logged-in user |
| GET | `/api/v1/tokens` | session | the user's API tokens, oldest first; see below |
| POST | `/api/v1/tokens` | session | create an API token `{"name": "ci"}`; `201` |
| DELETE | `/api/v1/tokens/{id}` | session | revoke one of the user's tokens; `204`, or `404` |

"session" means a session cookie or an API token.

Add a row (and a section below, for anything non-trivial) with every new endpoint.

### Setup and login
`POST /api/v1/setup` and `POST /api/v1/auth/login` take
`{"username": "admin", "password": "..."}` and answer with the user
`{"id": 1, "username": "admin", "created_at": "2026-10-24T12:00:00Z"}` (`201` for setup,
`200` for login) plus the session cookie: `HttpOnly`, `SameSite=Strict`, `Path=/`,
`Secure` when the request came over HTTPS (directly or with `X-Forwarded-Proto: https`),
valid for 7 days. `GET /api/v1/auth/me` returns the same user object.

- Setup works only while no user exists, else `409`. The username is 1 to 64 bytes
  without control characters or surrounding spaces; the password at least 8 characters
  (at most 1024 bytes). Invalid input is `400` with the reason.
- Usernames are case-insensitive. A wrong password and an unknown user both get
  `401 {"error":"invalid username or password"}`.
- Failed logins are limited to 20 per client IP (per /64 for IPv6) and 10 per existing
  username in 15 minutes; then login answers `429` with `Retry-After` (seconds), even
  with the right password. So ten wrong guesses lock that user out for up to 15 minutes;
  sessions already open keep working. Behind a reverse proxy all clients share the
  proxy's IP.
- When too many logins wait for a password check, login answers `503` with
  `Retry-After: 1`.

### Tokens
`POST /api/v1/tokens` with `{"name": "ci"}` answers `201` with
`{"id": 1, "name": "ci", "prefix": "nudge_AB12", "created_at": "2026-10-24T12:00:00Z",
"last_used_at": null, "token": "nudge_AB12..."}`. `token` appears only in this response;
nudge stores its SHA-256 hash. `GET /api/v1/tokens` lists the same objects without
`token`.

- A token acts as the user who created it, on every route; there are no scopes yet. That
  includes creating more tokens, so after a leak revoke every token created since.
- Names are 1 to 64 bytes without control characters or surrounding spaces, unique per
  user (`409` otherwise). A user can hold at most 100 tokens (`409` beyond that).
- `last_used_at` is updated at most once a minute.
- Deleting a user deletes their tokens.

### Environments
An environment is a Docker host; `{env}` in paths is its numeric `id`.
`GET /api/v1/environments` answers
`[{"id": 1, "name": "local", "kind": "local", "online": true, "created_at": "...", "last_seen_at": "..."}]`.
`kind` is `local` (the server's own Docker socket) or `agent` (a remote host running
`nudge agent`). Agent environments also carry `"agent": {"enrolled": true, "version":
"..."}`: whether an agent holds credentials, and the version it reported. `online` comes
from pinging each host (3 s timeout) during the request; `last_seen_at` is the last time
the host answered (updated at most once a minute, `null` if it never did).

`GET /api/v1/environments/{env}` adds the host's details under `docker`:
`{"name": "host1", "version": "28.5.1", "api_version": "1.51", "min_api_version": "1.24",
"os": "Debian GNU/Linux 13", "os_type": "linux", "kernel_version": "6.8.0",
"architecture": "x86_64", "ncpu": 8, "mem_total": 17179869184, "containers": 5,
"containers_running": 3, "containers_paused": 0, "containers_stopped": 2, "images": 7,
"storage_driver": "overlay2", "docker_root_dir": "/var/lib/docker"}`
(`mem_total` in bytes). Only these fields are passed on; Docker's proxy and registry
settings are not.

### Agent environments
- `POST /api/v1/environments` `{"name": "nas", "kind": "agent"}` creates an agent
  environment (`kind` must be `agent`: the local one is created automatically) and answers
  `201` with the environment plus its join token:
  `"join_token": {"token": "nudge_join_…", "expires_at": "...", "docker_run": "docker run -d … nudge:latest agent"}`.
  `docker_run` starts the agent on the remote host; its `NUDGE_SERVER_URL` comes from the
  request's host until a public URL can be configured (#12). Names follow the token
  rules (1 to 64 bytes, no control characters or surrounding spaces); `409` if taken.
- `POST /api/v1/environments/{env}/join-tokens` replaces the environment's join token
  (`201`, same object as `join_token`), for an agent that never enrolled or lost its
  credentials. Existing credentials keep working until a new agent enrolls.
- `DELETE /api/v1/environments/{env}/credentials` revokes the agent: deletes its
  credentials and any pending join token (`204`). The environment stays.
  Both answer `409` for a non-agent environment.
- `POST /api/v1/agents/enroll` `{"join_token": "nudge_join_…"}`, public (the join token is
  the credential), answers `201` `{"environment_id": 2, "agent_id": "agt_…", "agent_secret":
  "nudge_agent_…"}` and replaces any credentials the environment had. The secret is shown
  only this once. Unknown, used and expired tokens all answer `401`.

Join tokens and agent secrets are 32 random bytes; nudge stores their SHA-256. A join
token works once, within 24 h.

### Containers, images, volumes and networks
Read-only lists and details of an environment's resources. Responses are nudge's own
types, stable across Docker versions; times are RFC 3339 in UTC, or `null` when Docker
doesn't know them (a container that never started has `"started_at": null`). Lists and
maps are `[]` and `{}`, never `null`.

`{id}` is an ID or a name; image references may contain `/`, encoded as `%2F`
(`/images/ghcr.io%2Forg%2Fapp:1.2`). An `{id}` with other characters, or an object that
doesn't exist, answers `404` `{"error": "container not found"}` (or `image`, `volume`,
`network`).

- `GET /api/v1/environments/{env}/containers` lists every container, running or not;
  `?state=running` (or `created`, `paused`, `restarting`, `removing`, `exited`, `dead`)
  filters by state, other values answer `400`. Each item:
  `{"id": "abc123…", "name": "web", "image": "nginx:1.29", "image_id": "sha256:…",
  "command": "nginx -g 'daemon off;'", "created_at": "...", "state": "running",
  "status": "Up 2 hours", "health": "healthy", "ports": [{"private_port": 80,
  "protocol": "tcp", "host_ip": "0.0.0.0", "public_port": 8080}], "labels": {},
  "networks": ["front"], "mounts": [{"type": "volume", "name": "data", "source": "...",
  "destination": "/data", "rw": true}]}`. `health` is `""` without a health check; an
  unpublished port has `"host_ip": ""` and `"public_port": 0`.
- `GET /api/v1/environments/{env}/containers/{id}`:
  `{"id", "name", "image", "image_id", "created_at", "state": {"status", "running",
  "paused", "restarting", "oom_killed", "dead", "pid", "exit_code", "error", "started_at",
  "finished_at", "health"}, "restart_count", "path", "args", "config": {"hostname",
  "user", "env", "cmd", "entrypoint", "working_dir", "exposed_ports", "labels", "tty"},
  "restart_policy": {"name", "maximum_retry_count"}, "network_mode", "privileged",
  "ports", "mounts", "networks": [{"name", "network_id", "ip_address", "gateway",
  "mac_address", "aliases"}]}`. `config.env`, `path`, `args`, `config.cmd` and labels are
  returned as Docker reports them, values included: anyone with API access sees secrets
  passed that way (as they could through the Docker socket).
- `GET /api/v1/environments/{env}/images`: `[{"id": "sha256:…", "tags": ["nginx:1.29"],
  "digests": ["nginx@sha256:…"], "created_at": "...", "size": 1000, "labels": {},
  "dangling": false}]`; `dangling` images have no tag.
- `GET /api/v1/environments/{env}/images/{id}`: `{"id", "tags", "digests", "created_at",
  "size", "author", "comment", "architecture", "variant", "os", "config": {"user", "env",
  "cmd", "entrypoint", "working_dir", "exposed_ports", "volumes", "labels"}, "layers"}`.
- `GET /api/v1/environments/{env}/volumes` and `/volumes/{name}`, sorted by name:
  `{"name": "data", "driver": "local", "mountpoint": "/var/lib/docker/volumes/data/_data",
  "scope": "local", "created_at": "...", "labels": {}}`. Driver options are left out:
  CIFS and NFS volumes keep their mount credentials there.
- `GET /api/v1/environments/{env}/networks`, sorted by name: `{"id", "name", "driver",
  "scope", "created_at", "internal", "attachable", "ingress", "ipam": [{"subnet":
  "172.18.0.0/16", "ip_range": "", "gateway": "172.18.0.1"}], "labels"}`.
  `/networks/{id}` adds `"options": {}` (driver options such as the bridge name or
  macvlan parent; unlike volume options they hold no credentials) and `"containers": [{"id",
  "name", "ipv4_address", "ipv6_address", "mac_address"}]` (addresses with prefix length).

Docker errors (every environment route):
- `404` unknown environment.
- `503` the environment is offline: its Docker host can't be reached.
- `502` the daemon answered with an error. Its message is logged, never returned, since
  it can contain secrets.
- `504` the host did not answer within 10 s.
