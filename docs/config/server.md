# Server configuration (`nudge serve`)

Every option is a flag with an environment variable fallback; the defaults work in the
image without setting anything. A flag wins over its variable.

| Flag | Env | Default | Description |
|---|---|---|---|
| `-addr` | `NUDGE_ADDR` | `:8080` | listen address of the API and web UI |
| `-data` | `NUDGE_DATA` | `/data` | data directory (database, stacks, `compose-config/` with the short-lived registry logins of a stack deploy); mount a volume here |
| `-stacks-dir` | `NUDGE_STACKS_DIR` | `<data>/stacks` | compose stacks, an absolute path; mount it at the same path on the host (see Stacks) |
| `-compose` | `NUDGE_COMPOSE` | `/usr/local/bin/docker-compose` | docker-compose binary that deploys stacks (shipped in the image) |
| `-log-level` | `NUDGE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`; logs are JSON on stderr |
| `-docker-host` | `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker API of the local environment, `unix://` or `tcp://` |
| `-tls-cert` | `NUDGE_TLS_CERT` | unset | PEM certificate (chain); with `-tls-key` the server speaks HTTPS only |
| `-tls-key` | `NUDGE_TLS_KEY` | unset | PEM private key of `-tls-cert` |
| `-public-url` | `NUDGE_PUBLIC_URL` | unset | URL browsers and agents reach the server at, `https://host[:port]` |
| `-audit-retention-days` | `NUDGE_AUDIT_RETENTION_DAYS` | `90` | days the audit log keeps entries (at least 1); older ones are deleted hourly |
| `-update-check-interval` | `NUDGE_UPDATE_CHECK_INTERVAL` | `6h` | how often every environment's images are checked for updates (a Go duration, at least `5m`; `0` turns the scheduled check off, the UI's check still works); the first check runs two minutes after start |
| — | `NUDGE_ADMIN_PASSWORD` | unset | create the user `admin` with this password at start if no user exists |

## Data directory
`nudge serve` creates the data directory (mode 0700) if it is missing and exits with an
error if it cannot write there. The database is `<data>/nudge.db` (SQLite in WAL mode, so
`nudge.db-wal` and `nudge.db-shm` sit next to it); schema migrations run at start.
`<data>/secret.key` (32 random bytes, mode 0600, created at the first start) encrypts the
registry passwords and the notification webhooks' URLs and header values in the database.
Back up the whole directory while the server is stopped; without its `secret.key` a
restored database's registry passwords and webhooks must be entered again. A database migrated by a newer nudge
is refused by an older one.

## Stacks
Stacks live in `<stacks dir>/<environment ID>/<name>/` (`compose.yaml`, `.env`), and the
server runs `docker-compose` on them against the local environment's Docker
(`DOCKER_HOST`). compose resolves relative paths (`./data:/data`, `env_file`, `configs`)
in that directory, but the daemon mounts them from the host, so mount the stacks
directory **at the same path on the host and in the container**:
`-v /opt/nudge/stacks:/opt/nudge/stacks -e NUDGE_STACKS_DIR=/opt/nudge/stacks`.
With the default (inside the `/data` volume) stacks that use only named volumes work,
but relative bind mounts get an empty directory on the host. compose gets only
`DOCKER_HOST`, `HOME` and `PATH`, none of nudge's own environment. Stacks of agent
environments are deployed by the agent on its host (docs/config/agent.md, Stacks); the
server keeps the source files here either way.

## First user
With no user in the database the web UI asks for an admin username and password
(`POST /api/v1/setup`); until then only health, setup and login answer. To skip that
step, set `NUDGE_ADMIN_PASSWORD`: at start, if no user exists, nudge creates `admin` with
it (at least 8 characters, else the server refuses to start). Once a user exists the
variable is ignored, so changing it later does not change the password. It has no flag,
so the password never shows in the process list.

## TLS
Agents send their secret and all Docker traffic through the server, and the session
cookie grants root on every host, so anything beyond a trusted LAN needs HTTPS: either
nudge serves it itself or a reverse proxy terminates it.

**nudge serves HTTPS:** set both `NUDGE_TLS_CERT` and `NUDGE_TLS_KEY` (setting only one
is an error) to PEM files, for example mounted read-only from the host. The listen
address (`NUDGE_ADDR`, `:8080` by default) then speaks only HTTPS, TLS 1.2 or newer.
A certificate that can't be loaded stops the server at start. Renewed files are picked
up without a restart: nudge checks them every 30 s and reloads on `SIGHUP`
(`docker kill -s HUP nudge`). If a reload fails (a half-written file, a key that doesn't
match) the previous certificate stays in use and the error is logged. Automatic
certificates (ACME) are not built in; use a proxy for that.

```sh
docker run -d --name nudge -p 443:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock -v nudge-data:/data \
  -v /etc/nudge/tls:/tls:ro \
  -e NUDGE_TLS_CERT=/tls/fullchain.pem -e NUDGE_TLS_KEY=/tls/privkey.pem \
  -e NUDGE_PUBLIC_URL=https://nudge.example.net \
  registrydedic.zefirlabs.net/nudge:latest
```

## Public URL
`NUDGE_PUBLIC_URL` is the address the server is reached at, `http(s)://host[:port]`
(a trailing slash is dropped; a path, query or credentials are refused: nudge serves from
the root). It goes into the `docker run` command "Add environment" shows, so agents dial
the right address even when you opened the UI through another one (an IP, an SSH
tunnel). Unset, nudge uses the host of each request, with `https://` when the request
came over TLS or with `X-Forwarded-Proto: https`.

An `https://` public URL, like serving TLS, also marks the session cookie `Secure` on
every response, so the UI must then be opened over HTTPS: a browser on plain HTTP (the
proxy bypassed, straight to port 8080) drops the cookie and login fails.

## Reverse proxy
Run nudge on plain HTTP on a private network and let the proxy terminate TLS. The proxy
must:
- pass the `Host` header through unchanged (cross-origin writes are checked against it),
- set `X-Forwarded-Proto: https` (for `Secure` cookies when no public URL is set),
- forward WebSocket upgrades on `/api/v1/agents/connect` (and the log, exec and event
  streams), with no read timeout shorter than a few minutes: the agent tunnel is one
  long-lived request kept alive by pings every 15 s.

Caddy does all of this by default:

```
nudge.example.net {
	reverse_proxy nudge:8080
}
```

nginx:

```nginx
location / {
    proxy_pass http://nudge:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;  # map $http_upgrade, see nginx docs
    proxy_read_timeout 1h;
    proxy_buffering off;
}
```

Set `NUDGE_PUBLIC_URL=https://nudge.example.net` as well. Behind a proxy all clients
share its IP for the login rate limit (see docs/api.md).

## Sessions
Logins last 7 days (a cookie `nudge_session`; the database keeps only its SHA-256).
Expired sessions are deleted on use and every 10 minutes. The cookie is `Secure` when nudge
serves TLS or `NUDGE_PUBLIC_URL` is `https://`, else only when the browser uses HTTPS
through a proxy that sets `X-Forwarded-Proto: https`. Writes from other origins are refused. Failed logins are
rate-limited per client IP and per username (see docs/api.md).

## Docker access
The server manages the Docker host whose socket is mounted at `/var/run/docker.sock`
(`-docker-host` / `DOCKER_HOST` overrides it, as `unix:///path` or `tcp://host:port`;
plain TCP has no TLS, so keep it on a trusted network). Socket access is root-equivalent
on that host; the container needs no `--privileged` and no extra capabilities.

At start nudge pings that host (3 s timeout). If it answers and no `local` environment
exists, nudge creates one named `local`. If it doesn't answer, nudge logs a warning and
starts anyway; restart it once the socket is available to get the environment. Each
Docker call made by an API request has a 10 s timeout.
