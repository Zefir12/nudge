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
| GET | `/api/v1/audit` | session | the audit log, newest first, paged and filtered; see Audit log |
| GET | `/api/v1/registries` | session | registry credentials for pulls, by name; see below |
| POST | `/api/v1/registries` | session | add registry credentials; `201` |
| GET | `/api/v1/registries/{id}` | session | one registry, without its password |
| PUT | `/api/v1/registries/{id}` | session | change a registry; a missing password keeps the stored one |
| DELETE | `/api/v1/registries/{id}` | session | delete a registry's credentials; `204` |
| POST | `/api/v1/environments/{env}/containers` | session | create (and start) a container; `201`; see Container create and recreate |
| GET | `/api/v1/environments/{env}/containers/{id}/spec` | session | a container's settings in the create shape |
| POST | `/api/v1/environments/{env}/containers/{id}/recreate` | session | replace a container, optionally pulling first or with changed settings |
| POST | `/api/v1/environments/{env}/containers/{id}/update` | session | pull the container's image and recreate it if that brought a newer one; see Image updates |
| GET | `/api/v1/environments/{env}/containers/{id}/logs` | session | WebSocket: a container's logs, one JSON frame per line; see Container logs |
| GET | `/api/v1/environments/{env}/containers/{id}/stats` | session | WebSocket: a running container's usage, a sample about every second; see Stats |
| GET | `/api/v1/environments/{env}/containers/{id}/exec` | session | WebSocket: a command with a terminal in a running container (audited); see Container exec |
| GET | `/api/v1/environments/{env}/stats` | session | one usage sample of every running container, with totals; see Stats |
| GET | `/api/v1/environments/{env}/events` | session | WebSocket: the environment's Docker events as they happen; see Events |
| POST | `/api/v1/environments/{env}/images/pull` | session | pull `{"image": "nginx:1.29"}`, progress as JSON lines; see Image actions |
| POST | `/api/v1/environments/{env}/images/prune` | session | remove untagged images, or with `?all=true` every unused one |
| DELETE | `/api/v1/environments/{env}/images/{id}` | session | remove an image or tag (`?force=true`); `204` |
| POST | `/api/v1/environments/{env}/volumes` | session | create a volume; `201`; see Volume and network actions |
| POST | `/api/v1/environments/{env}/volumes/prune` | session | remove unused anonymous volumes, or with `?all=true` named ones too |
| DELETE | `/api/v1/environments/{env}/volumes/{name}` | session | remove a volume (`?force=true`); `204` |
| POST | `/api/v1/environments/{env}/networks` | session | create a network; `201` |
| POST | `/api/v1/environments/{env}/networks/prune` | session | remove custom networks no container uses |
| DELETE | `/api/v1/environments/{env}/networks/{id}` | session | remove a network by ID or name; `204` |
| POST | `/api/v1/stacks/check` | session | compose's errors for stack files `{"compose", "env"}` (writes nothing); see Stacks |
| GET | `/api/v1/environments/{env}/stacks` | session | the environment's compose stacks with container counts; see Stacks |
| POST | `/api/v1/environments/{env}/stacks` | session | create a stack `{"name", "compose", "env"}` (not deployed); `201` |
| GET | `/api/v1/environments/{env}/stacks/{name}` | session | a stack's files, container counts and containers by service |
| PUT | `/api/v1/environments/{env}/stacks/{name}` | session | replace a stack's files `{"compose", "env"}`; `204` |
| DELETE | `/api/v1/environments/{env}/stacks/{name}` | session | delete a stack that is down; `204` |
| POST | `/api/v1/environments/{env}/stacks/{name}/up` | session | deploy `{"pull": bool}`, compose output as JSON lines |
| POST | `/api/v1/environments/{env}/stacks/{name}/down` | session | stop and remove the containers `{"volumes": bool}`, output as JSON lines |
| POST | `/api/v1/environments/{env}/stacks/{name}/update` | session | pull the images and deploy, output as JSON lines; recorded in the update history |
| GET | `/api/v1/environments/{env}/updates` | session | the last image update check; see Image updates |
| POST | `/api/v1/environments/{env}/updates/check` | session | check the environment's images now |
| GET | `/api/v1/environments/{env}/updates/history` | session | updates made through nudge, with the image each replaced |

"session" means a session cookie or an API token.

Add a row (and a section below, for anything non-trivial) with every new endpoint.

### Setup and login
`POST /api/v1/setup` and `POST /api/v1/auth/login` take
`{"username": "admin", "password": "..."}` and answer with the user
`{"id": 1, "username": "admin", "created_at": "2026-10-24T12:00:00Z"}` (`201` for setup,
`200` for login) plus the session cookie: `HttpOnly`, `SameSite=Strict`, `Path=/`,
`Secure` when nudge serves TLS or `NUDGE_PUBLIC_URL` is `https://`, else when the request
came over HTTPS (with `X-Forwarded-Proto: https`),
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
  `docker_run` starts the agent on the remote host; its `NUDGE_SERVER_URL` is
  `NUDGE_PUBLIC_URL`, or when that is unset the request's host (`https://` over TLS or
  with `X-Forwarded-Proto: https`). Names follow the token
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
- `GET /api/v1/agents/connect` (WebSocket), public: the agent authenticates with HTTP
  Basic auth, its agent ID as the user name and its secret as the password, checked
  before the upgrade (missing or wrong credentials answer `401`). Then the tunnel
  handshake runs (docs/architecture.md) and the request lasts as long as the tunnel. One
  tunnel per agent: a new connection closes the previous one, and revoking the
  credentials or enrolling again closes it too. While the tunnel is up the environment is
  `"online": true` and every environment endpoint works as for `local`; without one,
  Docker endpoints answer `503` `{"error": "the environment is offline"}`. The agent's
  version (`agent.version`) and `last_seen_at` are recorded at connect, `last_seen_at`
  again at disconnect.

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

### Container actions
Each answers `204` with no body once Docker has done it (a request whose client goes away
still completes), `404` for an unknown container
(as above), and `409` with the reason when the container's state doesn't allow it.

- `POST /api/v1/environments/{env}/containers/{id}/start`.
- `POST .../containers/{id}/stop` and `.../restart`: `?timeout=` (0 to 600 seconds) is how
  long Docker waits before killing the container; without it the container's own stop
  timeout applies (10 s unless it sets another). The request waits up to the timeout plus
  10 s, or 2 minutes without one, then answers `504` (Docker carries on).
- `POST .../containers/{id}/kill`: sends `?signal=` (a name such as `SIGHUP` or `HUP`, or
  a number; default `SIGKILL`), `400` for a signal Docker doesn't know. `409` if the
  container isn't running.
- `DELETE /api/v1/environments/{env}/containers/{id}`: `?force=true` kills a running
  container first (without it a running container answers `409`), `?volumes=true` also
  removes its anonymous volumes. Invalid `timeout`, `signal`, `force` or `volumes` values
  answer `400`.

### Container create and recreate
A container spec is nudge's own subset of Docker's settings:

```json
{"image": "nginx:1.29", "name": "web", "command": ["nginx", "-g", "daemon off;"],
 "env": ["MODE=prod"],
 "ports": [{"host_ip": "", "host_port": 8080, "container_port": 80, "protocol": "tcp"}],
 "mounts": [{"type": "volume", "source": "data", "target": "/data", "read_only": false},
            {"type": "bind", "source": "/srv/conf", "target": "/etc/app", "read_only": true}],
 "networks": ["front", "back"], "restart_policy": "unless-stopped", "labels": {"app": "web"}}
```

Only `image` (a reference or an image ID) is required. An empty `command` uses the image's;
`host_ip` empty publishes on every address and `host_port` 0 picks a free port; `protocol`
defaults to `tcp`; a volume mount without `source` gets a new anonymous volume; empty
`networks` means Docker's default bridge (or, on a recreate, the container's own networks);
`restart_policy` is `no`, `always`, `unless-stopped` or `on-failure`. Invalid settings
answer `400` with a message per field: `{"error": "invalid container settings", "fields":
{"ports.0.host_port": "must be 0 (any free port) to 65535"}}`.

When Docker refuses a create or a start, the answer is `400` (`409` for a name in use) with
Docker's message, such as "port is already allocated" or "No such image", with the
container's environment values masked.

- `POST /api/v1/environments/{env}/containers` with a spec plus `"start": true` to start
  it. Answers `201` with `{"id": "…", "warnings": []}`. A container that doesn't start is
  removed again.
- `GET /api/v1/environments/{env}/containers/{id}/spec`: the container's settings as a
  spec, to edit and send to recreate. What the image sets (its environment, command,
  labels) is left out, so the spec shows the container's own settings.
- `POST /api/v1/environments/{env}/containers/{id}/recreate` with `{"pull": true, "spec":
  {...}}`, both optional. It starts from the container's full Docker settings, so the ones
  a spec doesn't cover (capabilities, devices, resource limits, …) are kept; a `spec`
  replaces the ones it covers, and mounts it keeps keep their options (such as `:z`).
  What the old image set (environment, command, labels, volumes, ports, healthcheck) is
  left to the image, so a pulled newer image brings its own. `pull` pulls the image first with its registry's stored
  credentials (errors as for an image pull; `400` if the image is an ID). Then the old
  container is stopped, renamed to `<name>-nudge-old`, the new one created with the
  original name (or the spec's), started if the old one ran, and the old one removed.
  Its anonymous volumes are mounted in the new container, so their data stays. If the
  create or start fails, the new container is removed and the old one gets its name back
  and is started again (the answer says if that failed too). Answers `200` with the new
  container's `{"id", "warnings"}`; `409` while another recreate of the container runs or
  when a `<name>-nudge-old` container is left from an earlier one.

### Container logs
`GET /api/v1/environments/{env}/containers/{id}/logs` upgrades to a WebSocket (the browser
sends the session cookie; a script sends `Authorization: Bearer <token>`). A session
upgrade is refused (`403`) unless its `Origin` matches the `Host`. Query options:
`follow=true` keeps the stream open for new output, `tail=N` starts with the last N lines
(default `all`), `timestamps=true` adds the daemon's time to each line.

Errors before the upgrade are ordinary JSON errors: `400` invalid option, `404` unknown
environment or container, `503`/`504`/`502` as for other Docker calls. Then each line is a
text frame `{"stream": "stdout"|"stderr", "time": "2026-10-01T12:00:00.5Z", "line": "..."}`
(`time` only with `timestamps`; a container with a TTY has no stderr, all its output is
`stdout`; lines over 16 KiB are split). The last frame is `{"done": true}` when the stream
ended (the container stopped, or without `follow` every line was sent) or `{"error":
"..."}`, then the server closes the socket normally. Closing the socket stops the stream
on the Docker host. The server pings every 30 s and ends a stream whose client doesn't
answer.

### Container exec
`GET /api/v1/environments/{env}/containers/{id}/exec` runs a command with a TTY in a
running container (`docker exec -it`) and upgrades to a WebSocket, authenticated and
origin-checked like the log stream. Query options: `cmd` is the command and its arguments
split on whitespace, without shell quoting (default `/bin/sh`, at most 1024 characters);
`cols` and `rows` (both or neither, 1 to 1000) are the terminal's initial size. The
process gets `TERM=xterm-256color`.

Errors before the upgrade are JSON errors: `400` invalid option, `404` unknown
environment or container, `409` container not running, `503`/`504`/`502` as for other
Docker calls. Every session is recorded in the audit log when it opens (status `101`), or
with the status it was refused with.

Then binary frames carry the terminal: the client's are its input (at most 32 KiB each),
the server's its output. A text frame `{"type": "resize", "cols": 120, "rows": 40}` from
the client resizes the terminal; other text frames are ignored. When the process exits the
server sends a text frame `{"done": true, "exit_code": 0}` (`exit_code` when the host
reports it) or `{"error": "..."}`, then closes the socket normally. Closing the socket
closes the process's terminal, which ends a shell. The server pings every 30 s and ends a
session whose client doesn't answer.

### Stats
Usage is computed on the server from the daemon's counters. A sample:
`{"time": "...", "cpu_percent": 12.5, "memory_usage": 268435456, "memory_limit": 1073741824,
"memory_percent": 25, "network_rx": 1024, "network_tx": 256, "block_read": 8192,
"block_write": 512, "pids": 7}`. `cpu_percent` is a percentage of one CPU (like `docker
stats`, it can pass 100 on several CPUs); memory leaves out the inactive page cache, and
the limit is the host's memory when the container has none (Windows containers report no
limit). Network and block I/O are byte totals since the container started; rates come
from two samples.

`GET /api/v1/environments/{env}/containers/{id}/stats` upgrades to a WebSocket like the
log stream (same authentication, `Origin` check, pings and close behaviour). Before the
upgrade: `404` unknown environment or container, `409` container is not running, and the
usual Docker errors. Then each sample is a text frame (the daemon's first reading, which
has no previous one to compute CPU from, is skipped); the last frame is `{"done": true}`
when the container stopped or `{"error": "..."}`.

`GET /api/v1/environments/{env}/stats` answers `{"containers": [{"id", "name", ...sample}],
"skipped": 0, "total": {"cpu_percent", "memory_usage", "network_rx", "network_tx", "block_read",
"block_write"}}`, sorted by name. It takes about a second (the daemon waits for a second
reading) and samples 8 containers at a time, for at most a minute; a container that stops
meanwhile, or isn't sampled in time, is left out and counted in `skipped`.

### Events
`GET /api/v1/environments/{env}/events` upgrades to a WebSocket like the log stream (same
authentication, `Origin` check, pings and close behaviour). `type=container,image` (or
repeated `type` params) keeps only those object types: `container`, `image`, `volume`,
`network`, `daemon`, `plugin`; without it every event is sent. Before the upgrade: `400`
unknown type, `404` unknown environment, `503` when 100 streams are already open for the
environment. Only live events are sent, none from before the connection.

Each event is a text frame `{"time": "2026-10-24T12:00:00.123456789Z", "type":
"container", "action": "start", "id": "<container ID>", "name": "web", "attributes":
{...}}`: `id` is a container or image ID or reference, or a volume or network name;
`name` is there when the event has one; `attributes` are Docker's (labels, image, exit
code, ...). Exec events carry only the action (`exec_start`), not the command. All clients
of an environment share one Docker event stream. The stream has no natural end: it ends
with `{"error": "..."}` when the Docker host ends it (unreachable, agent disconnected) or
when the client falls more than 64 events behind; clients reconnect.

### Image actions
- `POST /api/v1/environments/{env}/images/pull` with `{"image": "ghcr.io/org/app:1.2"}`
  (a reference without a tag pulls `latest`). If a registry under `/registries` has the
  image's host as its address (Docker Hub for images without a host), its credentials are
  sent to the daemon. Answers `200` with `Content-Type: application/x-ndjson`, one JSON
  object per line as Docker reports progress: `{"id": "a1b2c3d4e5f6", "status":
  "Downloading", "current": 1048576, "total": 3145728}` (`id` is a layer, or the tag on
  the first line; lines about the whole image have none). The last line is `{"done":
  true}`, or `{"error": "..."}` if the pull failed; a stream without either was cut off.
  Errors before the pull starts are ordinary JSON errors: `400` invalid reference, `404`
  unknown image or no access, `403` the registry refused the credentials, `502` other
  daemon errors. Unlike other routes these show Docker's message (the only explanation of
  a failed pull), with the registry password masked. The pull is cancelled when the
  client disconnects and after one hour.
- `POST /api/v1/environments/{env}/images/prune`: removes untagged (dangling) images, or
  with `?all=true` every image no container uses. Answers `{"deleted": ["sha256:…"],
  "untagged": ["old:1"], "space_reclaimed": 1234}`: `deleted` holds images and their
  layers. `409` while another prune runs.
- `DELETE /api/v1/environments/{env}/images/{id}`: `{id}` is an image ID or reference;
  removing a reference of an image with several tags only untags it. `409` when
  containers use the image or it has several tags and is removed by ID; `?force=true`
  removes it anyway, except while a running container uses it.

### Volume and network actions
- `POST /api/v1/environments/{env}/volumes` with `{"name": "media", "driver": "local",
  "options": {"type": "nfs", "o": "addr=10.0.0.5,rw", "device": ":/export"}, "labels":
  {"app": "web"}}`, every field optional (no name: Docker picks one; no driver: `local`).
  Answers `201` with the volume as in the list (without its options, which may hold mount
  credentials). An existing volume with the same name and driver is answered as is, like
  `docker volume create`; `409` when it exists with another driver. `400` for an invalid
  name or driver, empty option or label names, a driver the host doesn't have, or options
  the driver refuses (the daemon's message isn't shown, since it can quote the options).
- `POST /api/v1/environments/{env}/volumes/prune`: removes the anonymous volumes no
  container uses, or with `?all=true` named ones too. Answers `{"deleted": ["name"],
  "space_reclaimed": 1234}`; `409` while another prune runs.
- `DELETE /api/v1/environments/{env}/volumes/{name}`: `409` while a container (running
  or not) uses it; `?force=true` removes it even if its driver fails to.
- `POST /api/v1/environments/{env}/networks` with `{"name": "apps", "driver": "bridge",
  "internal": false, "attachable": false, "ipv6": false, "subnet": "172.30.0.0/16",
  "gateway": "172.30.0.1", "ip_range": "172.30.5.0/24", "options": {}, "labels": {}}`;
  only `name` is required (no driver: `bridge`; no subnet: Docker picks one from its
  pools, and then `gateway` and `ip_range` must be empty). `gateway` must be in the
  subnet and `ip_range` inside it. Answers `201` with `{"id": "…", "warnings": []}`;
  `409` when a network has the name; `400` for invalid fields, a driver the host doesn't
  have, or settings the daemon refuses (such as an overlapping subnet).
- `POST /api/v1/environments/{env}/networks/prune`: removes custom networks no container
  uses; answers `{"deleted": ["name"]}`.
- `DELETE /api/v1/environments/{env}/networks/{id}`: `409` while containers are attached
  and for Docker's predefined networks (`bridge`, `host`, `none`).

### Stacks
A stack is a compose project: `compose.yaml` and an optional `.env` (the variables compose
interpolates), kept by the server in `<stacks dir>/<env ID>/<name>/`
([config](config/server.md#stacks)). Names are compose project names: lowercase letters,
digits, `-` and `_`, starting with a letter or digit, at most 63 characters. Every write
is in the audit log with the stack's name as its target.
- `GET .../stacks` answers `[{"name": "web", "containers": 2, "running": 1}]`, sorted by
  name; `containers` counts the project's containers in any state (by the
  `com.docker.compose.project` label), `running` those running. `GET .../stacks/{name}`
  adds `"compose"` and `"env"` (`env` often holds secrets and is answered as stored) and
  `"services": [{"name": "app", "containers": [...]}]`, the project's containers grouped
  by the `com.docker.compose.service` label, sorted by service and container name, each as
  in the container list. A service shows up once it has a container.
- `POST .../stacks` `{"name": "web", "compose": "services: ...", "env": "TAG=1\n"}` writes
  the files and answers `201` with the stack; `400` invalid name or empty compose, `409` a
  stack of that name exists. `PUT .../stacks/{name}` `{"compose", "env"}` replaces them
  (an empty `env` deletes `.env`); the running stack changes at its next `up`. The YAML
  isn't checked until a deploy.
- `POST /api/v1/stacks/check` `{"compose", "env"}` runs `docker-compose config` on the files
  without saving them or reaching a daemon and answers `{"errors": []}` if compose accepts
  them, else compose's messages (at most 20), such as `"yaml: line 3: mapping values are
  not allowed in this context"`. Files the compose file names relative to the stack
  (`env_file`, bind sources) aren't checked. `400` empty compose, `429` four checks are
  already running, `503` the image has no `docker-compose`. Not in the audit log.
- `POST .../stacks/{name}/up` runs `docker-compose up --detach --remove-orphans`, with
  `{"pull": true}` also `--pull always` (an update); the body may be left out.
  `POST .../stacks/{name}/down` runs `docker-compose down --remove-orphans`, with
  `{"volumes": true}` also `--volumes`. Both answer `200` with `Content-Type:
  application/x-ndjson`, one line per line compose prints: `{"stream": "stderr", "line":
  " Container web-app-1  Started"}` (compose prints its progress on stderr). The last
  line is `{"exit_code": 0}` (non-zero: compose failed, recorded as `502` in the audit log)
  or `{"error": "..."}` if the run was cut short; a stream without either was cut off.
  Errors before compose starts are ordinary JSON errors: `404` no such stack, `409` the
  stack is being changed or deployed (one change per stack at a time) or the agent is too
  old for stacks ("update the agent to use stacks", tunnel protocol 1), `503` no
  `docker-compose` (in the server's image, or the agent's for agent environments) or the
  agent is offline. On an agent environment the server first sends the stack's files to
  the agent, and compose runs there, next to that host's daemon.
  The run is killed when the client disconnects and after one hour.
- `DELETE .../stacks/{name}` deletes the files; `409` while the project has containers
  (take it down first). On an agent environment the agent's copy is deleted too, so the
  agent must be online (`503` otherwise); an agent too old for stacks never had one.

### Registries
`POST /api/v1/registries` with `{"name": "GitHub", "address": "ghcr.io", "username":
"bot", "password": "ghp_..."}` answers `201` with `{"id": 1, "name": "GitHub", "address":
"ghcr.io", "username": "bot", "created_at": "...", "updated_at": "..."}`; the password is
never returned. `PUT /api/v1/registries/{id}` takes the same body; without `password` (or
with `null`) the stored one is kept.

- `address` is the registry host as image references name it, with an optional port. A
  pasted URL is reduced to its host (`https://ghcr.io/` → `ghcr.io`), and Docker Hub's
  aliases (`index.docker.io`, `registry-1.docker.io`) become `docker.io`.
- Names (1 to 64 bytes) and addresses are unique (`409` otherwise). Usernames are 1 to
  255 bytes, passwords 1 to 4096.
- Passwords are encrypted (AES-256-GCM) with the key in `<data>/secret.key`; see
  docs/config/server.md. A pull sends the matching password to the environment's Docker
  daemon, on agent environments through the agent's tunnel to the remote host.
- A pull reference may contain `@` only before a digest (`app@sha256:…`), so the
  credentials always match the host the daemon pulls from.

### Image updates
nudge checks the images that containers run by tag (an implicit `latest` included) for a
newer version: it asks the environment's daemon for the registry's digest of the tag
(with the stored registry credentials, as a pull would) and compares it with the digests
the local image was pulled as. Images pinned to a digest, named by ID or built locally
aren't updatable by tag. The check runs every `NUDGE_UPDATE_CHECK_INTERVAL` over every
environment (see docs/config/server.md) and on request.

- `GET /api/v1/environments/{env}/updates` answers the last check of the environment,
  `[]` before the first:
  `[{"image": "nginx:1.29", "image_id": "sha256:…", "status": "available",
  "local_digest": "sha256:…", "remote_digest": "sha256:…", "error": "", "checked_at":
  "..."}]`, by image. `status` is `available` (the registry has another digest for the
  tag), `current` or `unknown`; for `unknown`, `error` says why (no such tag, access
  refused, built locally, ...) without the daemon's message. A container has an update
  when its `image` and `image_id` match a row with `available`.
- `POST /api/v1/environments/{env}/updates/check` checks the environment now and answers
  the same list; it replaces the environment's results. One check runs at a time (`409`
  while another one, scheduled or not, runs); `503` when the environment is offline,
  `504` when its daemon doesn't answer. The check goes on when the client disconnects and
  stops after five minutes or at shutdown.
- Lookups are bounded: four at a time, at most one request per registry every 500 ms,
  one per image reference and environment per check, at most 500 images per
  environment. Each environment's daemon does its own lookups, with its own mirrors and
  proxy.

Updates (one click in the UI) are writes, so each is in the audit log under its own
action. Both keep the image that ran before in the update history, for a rollback by
hand: the previous image stays on the host (untagged) until it is pruned.

- `POST /api/v1/environments/{env}/containers/{id}/update` (no body) pulls the image
  reference the container was created from, with the registry's stored credentials. If
  the pulled image is the one the container runs, it answers `{"updated": false, "id":
  "<container ID>", "previous_image_id": "sha256:…", "image_id": "sha256:…"}` and leaves
  the container alone; else it recreates it as `recreate` does (same settings, restored
  if the create or start fails) and answers `{"updated": true, "id": "<new container
  ID>", ...}`. `400` the container runs an image by ID or digest; otherwise the errors of
  a recreate with `"pull": true`.
- `POST /api/v1/environments/{env}/stacks/{name}/update` (no body) is `up` with
  `--pull always` and answers the same stream. When compose ends (also when it failed or
  was cut short, as some services may have been replaced), each service whose
  containers run another image ID than before is recorded.
- `GET /api/v1/environments/{env}/updates/history` lists the environment's updates,
  newest first (at most 500); `?kind=container|stack&target=<name>` narrows it to one
  container or stack (by name, which survives a recreate): `[{"id": 1, "kind":
  "container", "target": "web", "service": "", "image": "nginx:1.29", "old_image_id":
  "sha256:…", "old_digest": "sha256:…", "new_image_id": "sha256:…", "new_digest":
  "sha256:…", "user": "admin", "time": "..."}]`. `service` is the compose service of a
  stack's row; a digest is empty when the image has none from that repository. The last
  20 updates per container or stack (a stack's with a row per service) and 5000 rows per
  environment are kept. `400` an unknown kind.

### Audit log
Every `POST`, `PUT`, `PATCH` and `DELETE` that reaches a route is recorded after it is
answered, by one middleware: time, user, the API token's name (empty for a session), the
environment ID from the path, the route pattern as `action`
(`POST /api/v1/environments/{env}/containers/{id}/start`), the target (the path's `{id}`,
or for creates the name from the body) and the HTTP status. Logins and logouts, reads,
unknown routes and requests refused for lack of a session are not recorded; unauthenticated
writes (setup, agent enrollment) only when they succeed.

`GET /api/v1/audit` answers `{"entries": [{"id": 7, "time": "2026-10-24T12:00:00Z",
"user": "admin", "token": "ci", "environment": "1", "action": "POST /api/v1/...",
"target": "web", "status": 204}], "next": 6}`. Query parameters, all optional:

- `limit`: page size, default 50, at most 200. `next` is `null` on the last page; pass it
  as `before` to get the next one.
- `user` (case-insensitive), `environment` (ID): exact matches.
- `q`: substring of the action or target, case-insensitive.
- `failed=true` only failed writes (status 400 and up), `failed=false` only the others.
- `since`, `until`: RFC 3339 times, inclusive and exclusive.

Entries are deleted after `NUDGE_AUDIT_RETENTION_DAYS` (default 90); see
docs/config/server.md, and beyond the newest million. A pull that fails after its stream
started (answered `200`) is recorded as `502`. Entries carry the time the request
started.

Docker errors (every environment route):
- `404` unknown environment.
- `503` the environment is offline: its Docker host can't be reached.
- `502` the daemon answered with an error. Its message is logged, never returned, since
  it can contain secrets.
- `504` the host did not answer within 10 s.
