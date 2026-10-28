# API

Base path `/api/v1`. JSON in and out; errors are `{"error": "message"}` with a matching
status code. Unknown paths and wrong methods under `/api/` return a JSON 404 to a
logged-in client and a JSON 401 otherwise.
Streams (logs, exec, events, agent tunnel) use WebSocket.

Auth: the `nudge_session` cookie set by login or setup, or `Authorization: Bearer <token>`
with an API token (see Tokens). When an `Authorization` header is present it alone
decides; the cookie is ignored. Routes marked *public* need neither; every other route
answers `401 {"error":"authentication required"}` without a valid session or token.
Users have a role, `admin` or `user` (see Users). Every authenticated route is for admins
only and answers `403 {"error":"admins only"}` to a user's session or API token, except
the routes marked *user* below: `GET /auth/me`, logout, the user's own API tokens, the
deployments (whose handlers check the user's role on each deployment) and, in the
environments an admin gave them, their own containers, volumes and networks (see
Ownership). An API token has its owner's role.

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
| POST | `/api/v1/auth/logout` | public | end the session and clear the cookie; `204` (a no-op for API tokens), or `200 {"logout_url": "..."}` for a single sign-on user: the provider's sign-out page to visit next |
| GET | `/api/v1/auth/me` | user | the logged-in user |
| GET | `/api/v1/auth/methods` | public | the login page's ways to sign in; see Single sign-on |
| GET | `/api/v1/auth/oidc/login` | public | start a single sign-on login (a browser navigation); see Single sign-on |
| GET | `/api/v1/auth/oidc/callback` | public | the provider's redirect back; starts the session |
| GET | `/api/v1/tokens` | user | the user's API tokens, oldest first; see below |
| POST | `/api/v1/tokens` | user, session only | create an API token `{"name": "ci"}`; `201` |
| DELETE | `/api/v1/tokens/{id}` | user | revoke one of the user's tokens; `204`, or `404` |
| GET | `/api/v1/users` | admin | every user by name; see Users |
| POST | `/api/v1/users` | admin, session only | create a local user `{"username", "password", "role"}`; `201` |
| PATCH | `/api/v1/users/{id}` | admin, session only | change `{"role", "disabled", "password"}` (each optional); `200` with the user |
| DELETE | `/api/v1/users/{id}` | admin | delete a user with their sessions, tokens and webhooks; `204` |
| GET | `/api/v1/users/{id}/environments` | admin | the environments a user may use `{"environments": [1, 3]}`; see Ownership |
| PUT | `/api/v1/users/{id}/environments` | admin | replace them `{"environments": [1, 3]}`; `200` with the stored list |
| GET | `/api/v1/audit` | session | the audit log, newest first, paged and filtered; see Audit log |
| POST | `/api/v1/backup` | admin, session only | download an encrypted backup of the server's data `{"passphrase"}`; see Backup |
| GET | `/api/v1/registries` | session | registry credentials for pulls, by name; see below |
| POST | `/api/v1/registries` | session | add registry credentials; `201` |
| GET | `/api/v1/registries/{id}` | session | one registry, without its password |
| PUT | `/api/v1/registries/{id}` | session | change a registry; a missing password keeps the stored one |
| DELETE | `/api/v1/registries/{id}` | session | delete a registry's credentials; `204` |
| GET | `/api/v1/registries/{id}/repositories` | session | the registry's repositories, paged (`?q=`, `?cursor=`); see Registry browsing |
| GET | `/api/v1/registries/{id}/tags` | session | a repository's tags (`?repository=`), sorted |
| GET | `/api/v1/registries/{id}/manifest` | session | a tag's digest, size, platforms and the tags sharing its digest (`?repository=&tag=`) |
| DELETE | `/api/v1/registries/{id}/tags` | admin | delete a tag (`?repository=&tag=&digest=`); `204` |
| GET | `/api/v1/webhooks` | session | notification webhooks, by name; see Notifications |
| POST | `/api/v1/webhooks` | session | add a webhook; `201` |
| GET | `/api/v1/webhooks/{id}` | session | one webhook, without its URL path or header value |
| PUT | `/api/v1/webhooks/{id}` | session | change a webhook; a missing `url` or `header_value` keeps the stored one |
| DELETE | `/api/v1/webhooks/{id}` | session | delete a webhook; `204` |
| POST | `/api/v1/webhooks/{id}/test` | session | send a test notification now; `{"ok": true}` or `{"ok": false, "error": "..."}` |
| POST | `/api/v1/environments/{env}/containers` | session | create (and start) a container; `201`; see Container create and recreate |
| GET | `/api/v1/environments/{env}/containers/{id}/spec` | session | a container's settings in the create shape |
| POST | `/api/v1/environments/{env}/containers/{id}/recreate` | session | replace a container, optionally pulling first or with changed settings |
| POST | `/api/v1/environments/{env}/user-containers` | user | create (and start) a container from a tenant spec; `201`; see User containers |
| GET | `/api/v1/environments/{env}/user-containers/{id}/spec` | user | an owned container's settings as a tenant spec |
| POST | `/api/v1/environments/{env}/user-containers/{id}/recreate` | user | pull the spec's image and replace an owned container with one built from the spec |
| POST | `/api/v1/environments/{env}/containers/{id}/update` | user | pull the container's image and recreate it if that brought a newer one; see Image updates |
| GET | `/api/v1/environments/{env}/containers/{id}/logs` | user | WebSocket: a container's logs, one JSON frame per line; see Container logs |
| GET | `/api/v1/environments/{env}/containers/{id}/stats` | user | WebSocket: a running container's usage, a sample about every second; see Stats |
| GET | `/api/v1/environments/{env}/containers/{id}/exec` | user | WebSocket: a command with a terminal in a running container (audited); see Container exec |
| GET | `/api/v1/environments/{env}/stats` | user | one usage sample of every running container, with totals; see Stats |
| GET | `/api/v1/environments/{env}/stats/summary` | user | the environment's CPU and memory totals, sampled at most every 30 s; see Stats |
| GET | `/api/v1/environments/{env}/allowance` | user | a user's allowance with what they use and what is left (admin: `?user=`); see Allowances |
| PUT | `/api/v1/environments/{env}/allowance?user={id}` | session | set a user's allowance in the environment; see Allowances |
| DELETE | `/api/v1/environments/{env}/allowance?user={id}` | session | remove it; `204`, `404` if there was none |
| GET | `/api/v1/environments/{env}/events` | user | WebSocket: the environment's Docker events as they happen; see Events |
| POST | `/api/v1/environments/{env}/images/pull` | session | pull `{"image": "nginx:1.29"}`, progress as JSON lines; see Image actions |
| POST | `/api/v1/environments/{env}/images/prune` | session | remove untagged images, or with `?all=true` every unused one |
| DELETE | `/api/v1/environments/{env}/images/{id}` | session | remove an image or tag (`?force=true`); `204` |
| POST | `/api/v1/environments/{env}/volumes` | user | create a volume; `201`; see Volume and network actions (users: Ownership) |
| POST | `/api/v1/environments/{env}/volumes/prune` | session | remove unused anonymous volumes, or with `?all=true` named ones too |
| POST | `/api/v1/environments/{env}/volumes/usage` | session | measure the volume sizes now; `204` once stored |
| DELETE | `/api/v1/environments/{env}/volumes/{name}` | user | remove a volume (`?force=true`); `204` |
| POST | `/api/v1/environments/{env}/networks` | user | create a network; `201` (users: Ownership) |
| POST | `/api/v1/environments/{env}/networks/prune` | session | remove custom networks no container uses |
| DELETE | `/api/v1/environments/{env}/networks/{id}` | user | remove a network by ID or name; `204` |
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
| GET | `/api/v1/environments/{env}/webhooks` | session | the deploy webhooks the user may see (`?container=` / `?stack=` name); see Webhooks |
| POST | `/api/v1/environments/{env}/webhooks` | session only | create a deploy webhook `{"name", "container" or "stack", "tag_filter"}`; `201` with its secret |
| DELETE | `/api/v1/environments/{env}/webhooks/{id}` | session | delete a deploy webhook and its runs; `204` |
| POST | `/api/v1/environments/{env}/webhooks/{id}/secret` | session only | new secret for a deploy webhook; the old one stops working at once |
| GET | `/api/v1/environments/{env}/webhooks/{id}/runs` | session | a deploy webhook's runs, newest first |
| POST | `/api/v1/hooks/{id}` | public (webhook secret) | trigger a deploy webhook: pull and redeploy its target; see Webhooks |
| GET | `/api/v1/deployments` | session | the deployments you may view (`?environment=` narrows), by name; see Deployments |
| POST | `/api/v1/deployments` | admin | create a deployment on a stack; `201`, you become its owner |
| GET | `/api/v1/deployments/{name}` | session | one deployment, with your `role` and what you `can` do |
| PATCH | `/api/v1/deployments/{name}` | session | change `description`, `variables` or `health` (maintainer) |
| DELETE | `/api/v1/deployments/{name}` | session | delete a deployment with its tokens and runs (owner); the stack stays; `204` |
| GET | `/api/v1/deployments/{name}/members` | session | members and their roles |
| POST | `/api/v1/deployments/{name}/members` | session | `{"username", "role"}` adds a member or changes their role (owner) |
| DELETE | `/api/v1/deployments/{name}/members/{user}` | session | remove a member by user id (owner); `204` |
| GET | `/api/v1/deployments/{name}/tokens` | session | deploy tokens, without their values (maintainer) |
| POST | `/api/v1/deployments/{name}/tokens` | session only | create a deploy token `{"name", "scopes", "expires_at"}`; `201` with its value, shown once |
| DELETE | `/api/v1/deployments/{name}/tokens/{id}` | session | revoke a deploy token; `204` |
| POST | `/api/v1/deployments/{name}/deploys` | session or deploy token (`deploy`) | queue a run `{"variables": {...}}`; `?wait=1` waits for it, `?stream=1` streams its progress; see Deployments |
| GET | `/api/v1/deployments/{name}/deploys` | session or deploy token (`runs:read`) | runs, newest first (the last 100) |
| GET | `/api/v1/deployments/{name}/deploys/{run}` | session or deploy token (`runs:read`) | one run |

"session" means a session cookie or an API token.

Add a row (and a section below, for anything non-trivial) with every new endpoint.

### Single sign-on
With an OpenID Connect provider configured (docs/config/server.md, Single sign-on):

- `GET /api/v1/auth/methods` answers `{"password": true, "oidc": {"name": "Authestra"}}`;
  `oidc` is `null` without single sign-on, `password` is false with
  `NUDGE_PASSWORD_LOGIN=false`.
- `GET /api/v1/auth/oidc/login?redirect=/path` sets a binding cookie `nudge_oidc`
  (`HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/api/v1/auth/oidc`, 10 minutes) and
  answers `302` to the provider. `redirect` must be a path on this site, else `/`.
- `GET /api/v1/auth/oidc/callback?state=...&code=...` needs the state (one use, 10
  minutes) and the binding cookie of the browser that started the login. Success sets the
  session cookie and answers `302` to the saved path. A failure answers `302` to
  `/login?error=sso`; a valid sign-in without the admin or user role (or without `mfa` in `amr`
  when MFA is required) answers a `403` page. Both are audited as
  `GET /api/v1/auth/oidc/callback` with the subject as target. A sign-in refused for having
  no role also ends every session and deletes every API token of that SSO user (if they
  signed in before); its audit entry names the nudge user and says how many were revoked.
  Every attempt counts against the per-IP login limit until it succeeds.
- The session cookie stays `SameSite=Strict`: the callback's redirect follows the
  provider's cross-site navigation, so the cookie isn't sent with that one request, but it
  loads only the web UI, whose own API calls are same-site.
- `POST /api/v1/setup` answers `403`, and `GET /api/v1/setup` always `{"needed":false}`.
- Without single sign-on both `oidc` routes are `404`.

### Setup and login
`POST /api/v1/setup` and `POST /api/v1/auth/login` take
`{"username": "admin", "password": "..."}` and answer with the user
`{"id": 1, "username": "admin", "created_at": "2026-10-24T12:00:00Z", "role": "admin", "admin": true}`
(`201` for setup,
`200` for login) plus the session cookie: `HttpOnly`, `SameSite=Strict`, `Path=/`,
`Secure` when nudge serves TLS or `NUDGE_PUBLIC_URL` is `https://`, else when the request
came over HTTPS (or with `X-Forwarded-Proto: https` from a trusted proxy,
`NUDGE_TRUSTED_PROXIES`),
valid for 7 days. `GET /api/v1/auth/me` returns the same user object. `admin` says
whether the user may use the admin routes (`role` is `admin`); the web UI hides
admin-only pages and actions when it is false. Setup creates an admin.

- Setup works only while no user exists, else `409`. The username is 1 to 64 bytes
  without control characters or surrounding spaces; the password at least 8 characters
  (at most 1024 bytes). Invalid input is `400` with the reason.
- Usernames are case-insensitive. A wrong password and an unknown user both get
  `401 {"error":"invalid username or password"}`.
- Failed logins are limited to 20 per client IP (per /64 for IPv6) and 10 per existing
  username in 15 minutes; then login answers `429` with `Retry-After` (seconds), even
  with the right password. So ten wrong guesses lock that user out for up to 15 minutes;
  sessions already open keep working. The client IP is the TCP peer, or behind a
  proxy listed in `NUDGE_TRUSTED_PROXIES` the client it names in `X-Forwarded-For`
  (docs/config/server.md, Trusted proxies); behind an unlisted proxy all clients share
  the proxy's IP.
- When too many logins wait for a password check, login answers `503` with
  `Retry-After: 1`.

### Tokens
`POST /api/v1/tokens` with `{"name": "ci"}` answers `201` with
`{"id": 1, "name": "ci", "prefix": "nudge_AB12", "created_at": "2026-10-24T12:00:00Z",
"last_used_at": null, "token": "nudge_AB12..."}`. `token` appears only in this response;
nudge stores its SHA-256 hash. `GET /api/v1/tokens` lists the same objects without
`token`.

- A token acts as the user who created it, on every route except the session-only ones;
  there are no scopes. Creating a token is session-only: with an API token
  `POST /api/v1/tokens` answers `403 {"error":"API tokens can't do this; sign in to the
  web UI"}` (an unknown token is still `401`), so a leaked token can't mint one that
  outlives its revocation. The refusal shows up in the audit log under the token's name.
  Listing and revoking tokens work with a token.
- Names are 1 to 64 bytes without control characters or surrounding spaces, unique per
  user (`409` otherwise). A user can hold at most 100 tokens (`409` beyond that).
- `last_used_at` is updated at most once a minute.
- Deleting a user deletes their tokens.

### Users
`GET /api/v1/users` lists
`{"id": 2, "username": "bob", "role": "user", "disabled": false, "sso": false,
"display_name": "", "email": "", "created_at": "..."}` by username.

- `POST /api/v1/users` creates a local user with a password (the rules of setup) and a
  role, `admin` or `user`; a taken name is `409`.
- `PATCH /api/v1/users/{id}` changes the given fields only. Disabling ends the user's
  sessions; their API tokens, deploy tokens and deploy webhooks stop working until they
  are enabled again, and they can't log in (`401`, as for a wrong password). A password
  reset ends their sessions too.
- SSO users get their role from the provider on every login: changing it, or setting a
  password, is `400`; they can be disabled (their next login gets a 403 page).
- With password login off (`NUDGE_PASSWORD_LOGIN=false`) the local break-glass `admin`
  can't be demoted, disabled or deleted either (`409`); its password can be reset.
- The last enabled admin can't be demoted, disabled or deleted: `409`. Concurrent changes
  can't get around this (each change checks it in the statement that writes).
- Creating a user and changing one are session-only, as a password is a credential.
  So are the other routes that mint a credential: creating an agent environment or a join
  token, creating a deploy webhook or rotating its secret, and creating a deploy token.
- `DELETE /api/v1/users/{id}` removes the user and everything they own: sessions, API
  tokens, deploy tokens, deploy webhooks and deployment memberships.
- Every change is in the audit log with the username as the target.

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

### User containers
Users create and change containers only through a tenant spec, which the server turns into
Docker's settings itself (api/tenant.go). Admins may use these routes too; the admin spec
above stays admin-only.

```json
{"image": "nginx:1.29", "name": "web", "command": [], "entrypoint": [], "env": ["MODE=prod"],
 "cpus_milli": 500, "memory_bytes": 268435456,
 "ports": [{"host_port": 0, "container_port": 80, "protocol": "tcp"}],
 "mounts": [{"volume": "u3-data", "target": "/data", "read_only": false}],
 "networks": ["u3-front"], "restart_policy": "unless-stopped",
 "healthcheck": {"command": ["/bin/check"], "interval_seconds": 30, "timeout_seconds": 5,
                 "start_period_seconds": 10, "retries": 3},
 "labels": {"app": "web"}}
```

- The JSON is decoded strictly: any other field (`privileged`, `cap_add`, `binds`,
  `network_mode`, `pid`, `devices`, `security_opt`, `runtime`, `cgroup_parent`,
  `volumes_from`, a mount's `type`, …) is `400 unknown field "…"`. Invalid values are `400`
  with a message per field, like the admin spec.
- `image` is a reference, never an image ID. `name` is required on create and gets the
  user's `u<id>-` prefix; on a recreate an empty one keeps the name. `cpus_milli` (at most
  1000000) and `memory_bytes` (6 MiB to 1 TiB) are required. At most 64 ports, mounts,
  networks and labels and 256 environment entries; labels in Docker's `com.docker.`
  namespace (compose projects) are refused like nudge's own, and so are labels starting
  with a prefix of `NUDGE_TENANT_DENIED_LABELS` (reverse proxies, updaters; see
  docs/config/server.md), also when the image sets them (400). `host_port` 0 takes the lowest free port of
  the user's range. `mounts` name the user's own volumes; `networks` the user's own
  networks by name (empty: their default network). `healthcheck` runs its command in exec
  form; null keeps the image's.
- The server sets, and nobody can change: capabilities Docker's default without
  `NET_RAW`, `no-new-privileges`, 512 processes, the `json-file` log driver with
  `max-size` 10m and `max-file` 3, an init process, a private IPC namespace, swap no larger
  than the memory limit, no binds, no devices, the default seccomp and AppArmor profiles,
  and the owner label. With the server's tenant settings (docs/config/server.md, "Hosting
  other users") also the user's cgroup parent, the sandboxed runtime and a disk cap on
  the writable layer (where the storage driver supports it).
- With a runtime configured, a daemon that doesn't list it is `409` on create, recreate
  and update. Starting or restarting a user's container is `409` when the daemon lacks
  the runtime, or the container wasn't made with the current tenant settings (its runtime,
  cgroup parent or, where the storage driver can set one, disk cap differ): recreate it.
- The image is always pulled first, without any stored registry credentials (so only
  public images), and must fit the allowance (its image patterns and disk). An image that
  sets a label in the `net.zefirlabs.nudge.` namespace is refused, and so is one with a
  `VOLUME` no mount covers (`fields.mounts` names the paths): Docker would make an
  anonymous volume nobody owns. An image from a registry outside the server's
  `NUDGE_USER_REGISTRIES` is `403` and never pulled (docs/config/server.md, "Registries
  users can pull from"); this holds for every user's pull, also updates and deploy webhooks.
- Someone else's volume or network is `400` on its field; going over the allowance or
  outside the port range is `409`.
- `POST .../user-containers` with a spec plus `"start": true` answers `201` with
  `{"id", "warnings"}`. `POST .../user-containers/{id}/recreate` with a spec pulls, then
  replaces the container like the admin recreate (stop, rename aside, create, start,
  remove; rolled back on failure) and answers `200`.
- A user's container is always rebuilt by the builder, whatever the path: an image update
  (`.../containers/{id}/update`, deploy webhooks) and an admin recreate without a spec read
  its settings back as a tenant spec and build it again. An admin recreate with the admin
  spec is `409` for a user's container. The tenant spec and recreate routes answer `409` for an admin's
  container (the builder would drop its binds, capabilities and devices), and an update
  of a user's container whose old image is gone is `409` (recreate it with its spec).

### Ownership
Users (role `user`) reach only what is theirs; admins reach everything.

- **Environments.** An admin chooses the environments each user may use
  (`PUT /api/v1/users/{id}/environments` with every id; an unknown id is `400` and
  changes nothing; the Users page has an Environments form). `GET /api/v1/environments`
  lists only those to the user, and every route under any other environment is `404
  environment not found`. Deleting an environment or the user removes the grant.
- **Owner label.** A container, volume or network a user creates carries the label
  `net.zefirlabs.nudge.owner=<owner key>`, set by the server. The owner key is random per
  user (not the user id, which SQLite can hand to a later user). Objects without it
  (created by an admin, by a stack or outside nudge) are the admins'. Labels starting
  with `net.zefirlabs.nudge.` are reserved: a create that sets one is `400` (for
  containers a field error on `labels`). The container spec leaves them out, and a
  recreate keeps the old container's.
- **What a user reaches**, marked *user* in the table: the container, volume and network
  lists, details and removal, container start/stop/restart/kill, update, logs, stats and
  exec, the environment's stats, summary and events, and the environment's details. Lists
  hold only their objects; another user's or an admin's object is `404 ... not found`,
  as if it did not exist. A container counts as theirs only if its image sets no owner
  label (Docker copies an image's labels onto its containers, so an image could otherwise
  hand a user a container an admin runs from it). Creating containers, recreating,
  images, stacks, prunes, updates checks and webhooks stay with admins for now. Their own allowance
  (see Allowances) is open to them too.
- **Networks and volumes.** Giving a user an environment creates their default bridge
  network `nudge-u-<user id>` there (best effort: an offline environment, or a network of
  that name they don't own, is logged and the access is kept). Users create more through
  the usual create routes, with limits: networks are `bridge` only, without `options`,
  `subnet`/`gateway`/`ip_range` (Docker picks the subnet), `ipv6` or `attachable`, and
  at most 8 per environment, the default one included (each takes a subnet from the
  daemon's pools; `409` past it). The server names the Linux bridge of every user's
  network `nudge` and ten hex digits (driver option `com.docker.network.bridge.name`) so
  the host's firewall can match users' traffic (docs/config/server.md, "Network");
  volumes are `local` only, without `options` (`o=bind` with `device=` would mount a host
  path). `name` is required, and the server prefixes it with `u<user id>-`, so `{"name":
  "data"}` from user 7 creates `u7-data` (the prefixed name is what the lists, details and
  removal use). A refused field is `400` before Docker is asked; a volume of that name
  someone else owns (a deleted user's leftover, user ids can come back) is `409`, never
  handed over. Removing a user removes none of their objects; an admin sees what's left.
- **Totals.** For a user, `GET /api/v1/environments/{env}` counts only their containers
  (`images` is `0`, `name` and `docker_root_dir` are empty), the stats overview and
  summary add up only their containers, and a network's detail lists only their
  containers attached to it.
- **Events.** A user's event stream has only the events of their own containers: no
  volume, network, image or daemon events (they carry no owner), and none of a container
  that was already gone when the stream first saw it.

### Allowances
An admin gives each user (role `user`) an allowance per environment: what all their
containers there may take together. Admins and their containers have none.

`PUT /api/v1/environments/{env}/allowance?user={id}` takes and answers
`{"cpus_milli": 1000, "memory_bytes": 8589934592, "disk_bytes": 21474836480,
"max_containers": 5, "port_min": 8000, "port_max": 8009, "images": []}` (the answer adds
`updated_at`). Every field is required and not negative; `port_min`..`port_max` are the
host ports the user may publish (both `0`: none). `images` are patterns (`path.Match`
syntax) of the images they may run, matched against the reference or its repository
(`nginx` allows `nginx:1.29`, `ghcr.io/me/*` everything under it); empty allows any. An
invalid allowance is `400` with `fields` (`port_max`, `images.0`), an admin as the user is
`400 admins have no allowance`.

`GET /api/v1/environments/{env}/allowance` answers a user's own (an admin passes
`?user=`; a user asking for someone else is `403`): `{"user": 2, "limits": {...} | null,
"used": {"cpus_milli", "memory_bytes", "disk_bytes", "containers"}, "left": {...} | null}`.
It asks the environment's Docker host, so an offline one answers like any Docker call.

**The check.** Before a user's container is created, recreated, updated (the API or a
deploy webhook), started or restarted, nudge adds up the CPU and memory limits of all
their containers in the environment, running or not (a stopped container can start), and
refuses with `409 over your allowance: ...` if the new total is over:
- Without an allowance a user's container can't be created or started (`409 you have no
  allowance in this environment`).
- The container needs a CPU and a memory limit; an existing one without counts as the
  whole allowance. A recreate leaves the old container out, so a bigger limit counts as
  the difference.
- The container count includes it.
- A user's container can't run an image that sets a `net.zefirlabs.nudge.` label (`400`):
  it would make the container the admins' and escape every later check.
- Host ports must be in the range; a binding without a host port gets the lowest one in
  the range that no container uses.
- Disk: the writable layers of their containers, the volumes they own or their containers
  mount (anonymous ones from an image's `VOLUME` too; sizes from the last volume
  measurement) and the images their pulls brought. Once that reaches the allowance, creates,
  recreates and pulls are refused (a start is not). An image a user's pull brings counts
  toward their disk (an image already on the host does not); if it takes them over it is
  removed again and the change refused. A user's pulls run one at a time.
  After an update, the image the container ran before is removed if their pull brought it
  and nothing else uses it. Disk is not enforced by the kernel (yet).
- Checks of one user in one environment run one at a time, through the create or start
  they allow, so two parallel creates can't both fit in what is left for one.

### Agent environments
- `POST /api/v1/environments` `{"name": "nas", "kind": "agent"}` creates an agent
  environment (`kind` must be `agent`: the local one is created automatically) and answers
  `201` with the environment plus its join token:
  `"join_token": {"token": "nudge_join_…", "expires_at": "...", "docker_run": "docker run -d … nudge-agent:latest agent", "compose": "services:\n  nudge-agent: …"}`.
  `docker_run` starts the agent on the remote host, and `compose` is the same agent as a
  compose file (deploy/agent.compose.yml with the values filled in, quoted and with `$`
  escaped). In both, `NUDGE_SERVER_URL` is
  `NUDGE_PUBLIC_URL`, or when that is unset the request's host (`https://` over TLS or
  with `X-Forwarded-Proto: https` from a trusted proxy). Names follow the token
  rules (1 to 64 bytes, no control characters or surrounding spaces); `409` if taken.
- `POST /api/v1/environments/{env}/join-tokens` replaces the environment's join token
  (`201`, same object as `join_token`), for an agent that never enrolled or lost its
  credentials. Existing credentials keep working until a new agent enrolls.
- Creating an agent environment and creating a join token are session-only: an API token
  gets `403`, since an agent enrolled with the join token keeps its credentials after the
  API token is revoked.
- `DELETE /api/v1/environments/{env}/credentials` revokes the agent: deletes its
  credentials and any pending join token (`204`). The environment stays.
  Both answer `409` for a non-agent environment.
- `DELETE /api/v1/environments/{env}`, admins only (`403` otherwise), removes an agent
  environment from nudge (`204`): its agent is disconnected and its credentials, join
  token, stack files, deploy webhooks and update results are deleted. Nothing on the
  host changes; its containers keep running. `409` for the local environment or while a
  stack change runs there.
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
  "scope": "local", "created_at": "...", "labels": {}, "size": 4096, "size_measured_at":
  "..."}`. Driver options are left out: CIFS and NFS volumes keep their mount credentials
  there. `size` (bytes) is the last background measurement (`-usage-interval`): `null`
  with a null `size_measured_at` before the first one, `null` with a time when the driver
  can't report sizes. Volumes created since are not measured yet.
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

`GET /api/v1/environments/{env}/stats/summary` answers `{"sampled_at": "...", "cpu_percent":
200, "memory_usage": 536870912, "containers": 2}`: the totals of the environment's last
overview and how many containers it sampled (running ones that stopped meanwhile are not
counted). An overview cut short, or one that could sample none of the listed containers,
is not kept. It is cheap to ask for often (the global
dashboard asks for every environment's): a summary younger than 30 s is answered as it
is, else the server samples like the overview (callers at the same time share one
sampling) and answers when it is done. Every overview refreshes the summary too.

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
- `POST /api/v1/environments/{env}/volumes/usage`: measures every volume's size now
  (Docker walks each one, so it can take minutes on a big host) and answers `204` once
  the sizes are stored. `409` while a measurement of the environment runs, `503` when
  the environment is offline. The measurement goes on if the request is cut short (by a
  proxy's timeout, say); the list shows its sizes once it ends.
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
  `{"pull": true}` also `--pull always` (an update); the body may be left out. Compose
  pulls with the credentials under `/registries` of every registry whose host the
  stack's compose.yaml or `.env` names (`ghcr.io/org/app`, not `notghcr.io` nor another
  port), and Docker Hub's (whenever stored); their passwords are masked
  (`********`) in the output. `update` pulls the same way.
  `POST .../stacks/{name}/down` runs `docker-compose down --remove-orphans`, with
  `{"volumes": true}` also `--volumes`. Both answer `200` with `Content-Type:
  application/x-ndjson`, one line per line compose prints: `{"stream": "stderr", "line":
  " Container web-app-1  Started"}` (compose prints its progress on stderr). The last
  line is `{"exit_code": 0}` (non-zero: compose failed, recorded as `502` in the audit log)
  or `{"error": "..."}` if the run was cut short; a stream without either was cut off.
  Errors before compose starts are ordinary JSON errors: `404` no such stack, `409` the
  stack is being changed or deployed (one change per stack at a time) or the agent is too
  old for stacks ("update the agent to use stacks", tunnel protocol 1), or (`up` and
  `update` on the local environment) the stack binds a path under the stacks directory that the
  daemon would take from another place on the host, as nudge has the stacks directory from
  a volume or another host path (the message names the bind, the mount and
  `NUDGE_STACKS_DIR`; docs/config/server.md, Stacks), `503` no
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
"ghcr.io", "username": "bot", "kind": "generic", "owners": [], "plain_http": false,
"created_at": "...", "updated_at": "..."}`; the password is never returned.
`PUT /api/v1/registries/{id}` takes the same body; without `password` (or with `null`) the
stored one is kept, unless the PUT changes `address`, `username` or `plain_http`: then
`password` is required (`400 {"error": "password required when address, username or
plain_http changes"}`), so the stored password never goes to a host or user it wasn't
saved for. The other fields are replaced: a PUT without `kind` makes the registry
`generic`.

- `kind` is how nudge browses the registry (Registry browsing below): `generic` (the
  default) for any Docker Registry v2 server (distribution/registry, Zot, Harbor, Docker
  Hub and GHCR by name), `forgejo` for Forgejo's container registry. For `forgejo`, the
  password must be a Forgejo access token with the `read:package` scope to browse and
  `write:package` to delete tags.
- `owners` (`forgejo` only, at most 20) are the Forgejo users and organizations whose
  images are listed; empty means the registry's username.
- `plain_http` lets nudge browse the registry over HTTP without TLS (a LAN registry). It
  only applies to browsing; pulls go through the Docker daemon, which needs the registry
  in its `insecure-registries`.
- When browsing, the registry names its token server (the `realm` of its `WWW-Authenticate:
  Bearer` challenge), and nudge sends the username and password there, as Docker does.
  The realm may be **any HTTPS host** (Docker Hub uses `auth.docker.io`), so a registry
  receives the credentials only if you trust it with them; a plain HTTP realm is only
  accepted on the registry's own host when `plain_http` is set.

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

### Registry browsing
The server talks to the registry itself over HTTPS (HTTP with `plain_http`), with the
stored credentials: the Registry v2 token flow (`WWW-Authenticate: Bearer`, tokens cached
until they expire) or Basic auth. Credentials go to a token server only over HTTPS, or to
the registry's own host when it is `plain_http`; redirects to another host (blob
storage) never carry them. Each call is bounded (30 s, capped responses, at most 20
pages of a listing).

Repository names contain `/`, so they go in the query. Names follow the distribution
grammar (`team/app`, lowercase; tags such as `v1.2`); anything else is `400`.

- `GET /api/v1/registries/{id}/repositories?q=&cursor=` answers `{"repositories":
  [{"name": "zefir/nudge"}], "next": "zefir/nudge"}`: 100 names a page, sorted, `q` a
  case-insensitive substring filter; pass `next` as `cursor` for the next page (no `next`
  on the last). `generic` registries list `/v2/_catalog`; `forgejo` registries list the
  owners' container packages (`GET /api/v1/packages/{owner}?type=container`). Registries
  without a catalog (Docker Hub, GHCR) answer `409 {"error": "registry doesn't list
  repositories; open one by name"}`: tags and manifests still work for a repository the
  user names (`nginx` on Docker Hub is `library/nginx`).
- `GET /api/v1/registries/{id}/tags?repository=zefir/nudge` answers `[{"tag":
  "latest"}]`, sorted. Forgejo's platform manifests (`sha256:…` versions) are left out.
- `GET /api/v1/registries/{id}/manifest?repository=zefir/nudge&tag=latest` answers
  `{"repository", "tag", "digest": "sha256:…", "media_type", "size", "created",
  "platforms": [{"os", "architecture", "variant", "digest", "size"}], "shared_tags":
  ["v1.2"], "shared_tags_complete": true}`. An image has `size` (config plus layers,
  compressed) and `created` (from its config); an index (multi-arch) has `platforms`
  instead, each with its size, without attestations. `shared_tags` are the repository's
  other tags on the same digest (at most 100 tags are checked; `shared_tags_complete` is
  false when not all were).
- `DELETE /api/v1/registries/{id}/tags?repository=…&tag=…&digest=sha256:…` deletes the
  tag; admins only (`403` for a user).
  `digest` must be the tag's current digest (from the manifest call), else `409
  {"error": "the tag points to another digest now"}`, so a re-push in between is never
  deleted. Audited with target `<address>/<repository>:<tag>`.
  - `generic`: deletes the manifest by digest (`DELETE /v2/<repo>/manifests/<digest>`),
    which removes **every tag on that digest** (`shared_tags`). The registry must allow
    deletes (distribution: `REGISTRY_STORAGE_DELETE_ENABLED=true`), else `409
    {"error": "registry doesn't allow deletes"}`, and frees the space only after its
    garbage collection runs.
  - `forgejo`: deletes the tag's package version through Forgejo's API (`DELETE
    /api/v1/packages/{owner}/container/{name}/{tag}`); other tags on the same digest stay.
- Errors: `404` for an unknown registry, repository or tag; `502 {"error": "registry
  unreachable"}` or `502 {"error": "registry refused the credentials"}` (also a token
  missing a scope), `502 {"error": "registry's token server isn't HTTPS"}`, and `409`
  for a `forgejo` registry without owners whose username isn't a Forgejo user. The
  registry's own error messages are never passed on.

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

### Notifications
`POST /api/v1/webhooks` with `{"name": "Chat", "url": "https://hooks.example.com/T0/abc",
"header_name": "X-Token", "header_value": "...", "enabled": true, "events":
["container.exited", "container.unhealthy", "container.healthy"]}` answers `201` with
`{"id": 1, "name": "Chat", "endpoint": "https://hooks.example.com", "header_name":
"X-Token", "enabled": true, "events": ["container.unhealthy", "container.healthy",
"container.exited"], "last_sent_at": null, "last_error": "", "created_at": "...",
"updated_at": "..."}`. Chat webhooks carry their token in the URL, so the URL and the
header value are encrypted like registry passwords and never returned: `endpoint` is the
URL's scheme and host. `PUT /api/v1/webhooks/{id}` takes the same body; without `url`,
`header_value`, `enabled` or `events` (or with `null`) the stored one is kept, and an empty
`header_name` drops the header. The stored header value is kept only while the URL's
scheme and host and the header name stay the same (`400` otherwise), so a secret never
follows the URL elsewhere. `header_name` and `header_value` are optional; a new webhook's
`enabled` defaults to `false`, its `events` to the problems (`update.available`,
`container.unhealthy`, `container.exited`, `agent.offline`), without the recoveries.

- `url` is `http` or `https`, at most 2048 bytes, without credentials (send those in the
  header). Names (1 to 64 bytes) are unique (`409`), at most 20 webhooks (`409`).
- `header_name` is any header but `Host`, `Content-Type`, `Content-Length`,
  `Transfer-Encoding`, `Connection`, `User-Agent` and `X-Nudge-Event`; values are 1 to
  4096 bytes without control characters.
- `events` are the events (table below, not `test`) the webhook receives, at least one
  (`400`); they are returned in the table's order.
- `POST /api/v1/webhooks/{id}/test` posts a `test` notification to that webhook, enabled
  or not, and answers `200` with the outcome. `last_sent_at` and `last_error` describe the
  last delivery, of a test or a notification; errors never contain the URL.
- `503` when the server runs without notifications (tests).

Every enabled webhook that receives the event gets a `POST` with `Content-Type: application/json`, `User-Agent:
nudge/<version>`, `X-Nudge-Event: <event>` and the secret header. Redirects are not
followed (the secret header would go along); any `2xx` is a delivery. A network error, a
timeout, `408`, `429` or a `5xx` is tried three times in all (5 s, then 10 s apart);
other answers aren't retried. Up to 4 webhooks are served at once; notifications go out
one after the other. The body:

```json
{"event": "container.exited", "time": "2026-10-24T12:00:00Z",
 "message": "Container web on local exited with code 1",
 "environment": {"id": 1, "name": "local"},
 "container": {"id": "…", "name": "web", "image": "nginx:1.27", "exit_code": 1}}
```

| Event | When | Extra fields | Sent again |
|---|---|---|---|
| `update.available` | an update check finds a new registry digest for an image | `images: [{image, local_digest, remote_digest}]`, all images of one check and environment | for a newer digest only |
| `container.exited` | a container stops with a non-zero exit code without a stop, kill or restart before it (5 minutes) | `container` with `exit_code` | an hour later at the earliest, per container name |
| `container.unhealthy` | a container's health check turns unhealthy | `container` | an hour later at the earliest, per container name, or at once after `container.healthy` |
| `container.healthy` | the health check of a container notified as unhealthy turns healthy (up to a day after the notice's hour) | `container` | after the next `container.unhealthy` |
| `agent.offline` | an agent environment that has connected before stays offline for 2 minutes | — | once a day while it stays offline; after it was back online, at the next outage |
| `agent.online` | an agent notified as offline is back online | — | after the next `agent.offline` |
| `test` | `POST /api/v1/webhooks/{id}/test` | — | — |

Containers are watched through each online environment's Docker events (the stream the
events WebSocket uses); environments are looked at every 30 seconds, so events while a
stream is reconnecting are missed. What was sent is kept in the database, so a restart
doesn't repeat it; a notification that no webhook got (none enabled, all failed, or more
than 100 waiting) counts as not sent, so it goes out the next time it happens. A
recovery (`container.healthy`, `agent.online`) is sent only when its problem was: a
container that was never notified as unhealthy, or an agent back within its 2 minutes,
stays quiet. It goes to the webhooks that receive the recovery event, whether or not they
receive the problem.

### Webhooks
Deploy webhooks (not to be confused with the notification webhooks above) let a CI job or
a Forgejo/Gitea/GitHub webhook make one container or stack pull its image and redeploy,
with a secret that can do nothing else: unlike an API token, which is its user on every
route (root on the host), a leaked webhook secret can only redeploy one target.

`POST /api/v1/environments/{env}/webhooks` with `{"name": "ci", "container": "web",
"tag_filter": "1.*"}` (or `"stack": "app"` instead of `"container"`) answers `201` with
`{"id": "K3…", "name": "ci", "kind": "container", "target": "web", "tag_filter": "1.*",
"created_by": "admin", "created_at": "...", "last_triggered_at": null, "last_status": "", "url":
"https://nudge.example.com/api/v1/hooks/K3…", "secret": "nudge_hook_…"}`. The secret is
shown only here and by `POST .../webhooks/{id}/secret` (rotate: same answer, new secret,
the old one is refused at once); it is stored encrypted, like registry passwords, since
signature checks need its value. `url` is built from `NUDGE_PUBLIC_URL` (else from the
request, like the agent join command).

- A container can be named by ID or name; it is stored by name, so the webhook still
  works after a recreate (whose new container has a new ID). It must exist (`404`); a
  stack must exist in nudge (`404`).
- Names are 1 to 64 bytes, unique per user (`409`); at most 100 webhooks per user (`409`).
  `tag_filter` is optional, a pattern with `*`, `?` and `[...]` (Go's `path.Match`).
- Deploy webhooks are admin-only: a user gets `403`. Every admin sees and manages all
  of them: any admin can list, rotate and delete another admin's webhooks (and, with the
  new secret, trigger them), and the runs still go as the creator, so the audit log and
  update history name the creator, not the admin who rotated the secret. Deleting a user deletes their webhooks. Creating a
  webhook and rotating its secret are session-only (an API token gets `403`), as the
  secret outlives the token's revocation; API tokens can list, delete and read the runs.
- `last_status` is the newest run's `status` (below), `""` before the first run.
- `GET .../webhooks/{id}/runs` lists the runs, newest first, at most 50 per webhook:
  `{"id": 7, "trigger": "signature", "status": "updated", "queued_at": "...",
  "started_at": "...", "finished_at": "...", "image_before": "sha256:…",
  "image_after": "sha256:…", "error": ""}`. `status` is `queued`, `running`, `updated`,
  `unchanged`, `skipped` or `failed`; `error` is nudge's own words, never the daemon's
  (the server log has those). For a stack the images are the changed services' as
  `service=ID` lists.

**Trigger:** `POST /api/v1/hooks/{id}` is public: the webhook's secret is the credential,
as either

- `Authorization: Bearer nudge_hook_…`, or
- an HMAC-SHA256 of the raw body keyed with the secret, in `X-Forgejo-Signature` or
  `X-Gitea-Signature` (hex) or `X-Hub-Signature-256` (`sha256=` and hex). A
  `Bearer nudge_hook_…` header decides alone; any other `Authorization` header (a
  proxy's Basic auth, say) is ignored and the signature counts.

Both are compared in constant time. The client IP's failed-login limit is checked first
(20 in 15 minutes, shared with logins; then `429` with `Retry-After`, before the body is
read). The body is then read, at most 1 MiB (`413`, refused up front when
`Content-Length` says more) within 30 seconds (`400` if it can't be read), and never
echoed. An unknown webhook, a missing and a wrong credential all answer
`401 {"error":"authentication required"}` and count as failed attempts, as do refused
bodies. A webhook accepts 10 triggers a minute (`429` beyond).

- The update is the one `POST .../containers/{id}/update` or `.../stacks/{name}/update`
  makes, run as the webhook's creator: it goes into the update history under their name,
  and the audit log records `POST /api/v1/hooks/{id}` with their user and the token
  `hook:<name>`. Refused triggers aren't recorded.
- It runs in the background: `202 {"run_id": 7, "status": "queued"}`. With `?wait=1` the
  answer waits for the run (up to 5 minutes, then `202` with the run still going): `200`
  with the run, or `502` with it when it failed, so `curl -f` fails the CI step.
- One run per webhook at a time. A trigger during a run queues one follow-up; later
  triggers merge into it (and a waiting caller gets that run). A shutdown stops the
  running deploy (a container's replace steps finish, so it is never left half-done) and
  fails the queued one.
- Tag filter: if the webhook has one and the call is a signed Forgejo/Gitea package event
  (`X-Forgejo-Event` or `X-Gitea-Event: package`) whose `package.version` doesn't match
  it, the answer is `200 {"run_id": 8, "status": "skipped"}` and nothing is deployed.
  Other events and bearer calls always run.

Use HTTPS: a bearer secret over plain HTTP can be read by anyone on the path. A signed
request can't be changed, but it can be replayed: the signature covers only the body, with
no timestamp or nonce, so whoever sees one signed request can send it again as often as
they like. Each replay is just another trigger of the same update, and the 10 triggers a
minute per webhook bound them.

Behind a reverse proxy every client has the proxy's IP, so failed webhook checks and
failed logins share one limit: a misconfigured CI job that keeps sending a wrong secret
can lock everyone out of logging in for 15 minutes (see docs/config/server.md).

A Forgejo (or GitHub) Actions step after the image is pushed, with the secret in the
repository's secrets as `NUDGE_HOOK_SECRET` and the URL in its variables:

```yaml
- name: Deploy
  run: curl -fsS -X POST -H "Authorization: Bearer ${{ secrets.NUDGE_HOOK_SECRET }}" "${{ vars.NUDGE_HOOK_URL }}?wait=1"
```

A native Forgejo webhook instead (repository or organisation settings, Webhooks, Add
webhook, Forgejo): target URL = the webhook's `url`, HTTP method `POST`, content type
`application/json`, secret = the webhook's secret (Forgejo signs the body with it), and
trigger on "Custom events" with "Package" to redeploy when a new image version is
published; set `tag_filter` (say `latest` or `1.*`) so other tags of the package are
skipped. A native webhook can't wait, so it gets `202`; the runs show the outcome.

### Deployments

A deployment is a stack in an environment that CI deploys through. Names are 1 to 63
lowercase letters, digits and dashes, unique across all users (a taken name answers `409`
even when you can't see that deployment); at most 1000 deployments.

- **Roles.** Each member has a role: `viewer` (reads the deployment, its runs and
  members), `deployer` (also deploys), `maintainer` (also edits it and manages deploy
  tokens), `owner` (also manages members and deletes it). Admins may do everything. The
  creator is the first owner, and a deployment always keeps one (`409` otherwise). A
  deployment you can't view answers `404`; one you can view but not change, `403`.
  `GET` answers `can`, the actions you may do: `view`, `deploy`, `edit`,
  `manage_tokens`, `manage_members`, `delete`.
- **Variables.** `variables` lists the stack's `.env` variables a deploy may set
  (uppercase letters, digits and `_`, at most 20). The compose file uses them, such as
  `image: ${API_IMAGE}`, so CI pins the exact image it built. A deploy's values may hold
  only letters, digits and `._:/@+=-` (no `$`, quotes, spaces or line breaks), at most 512
  bytes; another variable answers `400`. A run replaces the variables' lines in `.env`
  (adding the ones it lacks) and keeps the rest of the file; a failed run puts the file
  back as it was.
- **Deploy tokens** (`nudge_dt_…`, stored hashed, shown once) work only on their
  deployment's three `deploys` routes, for their scopes: `deploy` (start runs) and
  `runs:read`. Any other route answers `403`, another deployment `401`; failed checks
  count against the login limit per IP. A token acts for its creator and stops working
  (`403`) when the creator may no longer deploy (or view, for `runs:read`). Creating one
  needs a session, not an API token. `expires_at` (RFC 3339, in the future) is optional.
- **Runs.** `POST .../deploys` with `{"variables": {"API_IMAGE": "registry/app@sha256:…"}}`
  (body optional) pulls the stack's images and brings it up, like a stack update, as the
  caller (a token's creator, for the update history). Runs of a deployment go one at a
  time; one request waits behind the running one, and a newer request replaces it: the
  replaced run ends `superseded` and its variables go into the newer run (the newer
  value wins). Without `?wait=1` the answer is `202` with the run. With it, the run once
  it ends: `200` `succeeded`, `502` `failed`, `409` `superseded`, or `202` if it still runs
  after 10 minutes. A run:
  `{"id", "actor", "actor_kind": "user"|"token", "status", "variables", "previous",
  "queued_at", "started_at", "finished_at", "image_before", "image_after", "error"}`.
  `status` is `queued`, `running`, `succeeded`, `failed`, `superseded` or `rolled_back`;
  `image_before`/`image_after` list the changed services as `service=ID`. `previous` (the
  values a run replaced) is shown to maintainers and owners only, `{}` otherwise.
  `error` is nudge's own words, never the daemon's. `step` is the last step the run
  reached (`pulling`, `deploying`, `checking`, `rolling_back`) and `warnings` what it
  skipped or worked around (say, a check an older agent can't run).
- **Health.** `health` (on create and `PATCH`; a `PATCH` replaces all of it) says when a
  run succeeded: `{"wait_seconds": 0-1800, "stable_seconds": 0-600, "http": {"service",
  "port", "path": "/", "status": 200, "timeout_seconds": 5}, "rollback": false, "remove_oneoffs": false}`, `http`
  optional, the defaults shown. `wait_seconds` has compose wait for the services to run
  and pass their healthchecks (`up --wait`). `stable_seconds` then requires every
  container to keep running without a restart (a container that exited with 0 is a
  finished one-off job). `http` then GETs `http://127.0.0.1:port/path` inside a running
  container of `service`, without following redirects, and expects `status`. A failure
  ends the run `failed` with an `error` naming the step and the service, and puts the
  `.env` back; with `rollback` the replaced values are also deployed again (recorded in
  the update history) and the run ends `rolled_back` (`502` like a failure). Agents built
  before a check skip it with a warning. With `remove_oneoffs`, a run that ended
  `succeeded` then removes the stack's containers that exited with 0 and have restart
  policy `no` (migrations); the progress names them, and a failed removal is a warning.
  docs/deployments.md is the how-to.
- **Progress stream.** `POST .../deploys?stream=1` answers `200` with JSON lines once the
  run is queued (errors before that are ordinary JSON errors): `{"run": 7, "message":
  "queued"}`, then progress `{"step", "service", "message", "warning"}`, an empty `{}`
  every 25 s while nothing happens, and finally `{"status", "run", "step", "error",
  "warnings"}`. CI passes only if the last line's `status` is `succeeded`. Leaving early
  doesn't stop the run. Steps can be skipped in the stream when the run gets ahead of it.

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

### Backup
`POST /api/v1/backup` with `{"passphrase": "..."}` (at least 12 characters) answers `200`
with the backup as `application/octet-stream` and
`Content-Disposition: attachment; filename="nudge-20261028-120000.nudgebak"`: the
database snapshot, `secret.key` and the stacks' compose files, encrypted with the
passphrase (docs/config/server.md, "Backup and restore", has the contents and the
restore). The backup is written to a temporary file in the data directory first, so the
data volume needs room for one. Admins only and session only: the file holds the key to
the stored credentials. Errors: `400` a missing or short passphrase, `409` another backup
is being made. Audited with the file name as the target; the passphrase is never
recorded.

Docker errors (every environment route):
- `404` unknown environment.
- `503` the environment is offline: its Docker host can't be reached.
- `502` the daemon answered with an error. Its message is logged, never returned, since
  it can contain secrets.
- `504` the host did not answer within 10 s.
