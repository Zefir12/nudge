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
`kind` is `local` (the server's own Docker socket) or `agent` (phase 2). `online` comes
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

Docker errors (every environment route):
- `404` unknown environment.
- `502` the host is unreachable, or its daemon answered with an error. The daemon's
  message is logged, never returned, since it can contain secrets.
- `504` the host did not answer within 10 s.
