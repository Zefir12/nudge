# Server configuration (`nudge serve`)

Every option is a flag with an environment variable fallback; the defaults work in the
image without setting anything. A flag wins over its variable.

| Flag | Env | Default | Description |
|---|---|---|---|
| `-addr` | `NUDGE_ADDR` | `:8080` | listen address of the API and web UI |
| `-data` | `NUDGE_DATA` | `/data` | data directory (database, stacks); mount a volume here |
| `-log-level` | `NUDGE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`; logs are JSON on stderr |
| `-docker-host` | `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker API of the local environment, `unix://` or `tcp://` |
| — | `NUDGE_ADMIN_PASSWORD` | unset | create the user `admin` with this password at start if no user exists |

Planned (see PLAN.md): `NUDGE_TLS_CERT` / `NUDGE_TLS_KEY` (serve HTTPS, phase 2), `NUDGE_PUBLIC_URL` (the URL
agents and links use, phase 2).

## Data directory
`nudge serve` creates the data directory (mode 0700) if it is missing and exits with an
error if it cannot write there. The database is `<data>/nudge.db` (SQLite in WAL mode, so
`nudge.db-wal` and `nudge.db-shm` sit next to it); schema migrations run at start. Back
up the whole directory while the server is stopped. A database migrated by a newer nudge
is refused by an older one.

## First user
With no user in the database the web UI asks for an admin username and password
(`POST /api/v1/setup`); until then only health, setup and login answer. To skip that
step, set `NUDGE_ADMIN_PASSWORD`: at start, if no user exists, nudge creates `admin` with
it (at least 8 characters, else the server refuses to start). Once a user exists the
variable is ignored, so changing it later does not change the password. It has no flag,
so the password never shows in the process list.

## Sessions
Logins last 7 days (a cookie `nudge_session`; the database keeps only its SHA-256).
Expired sessions are deleted on use and every 10 minutes. The cookie is `Secure` only
when the browser uses HTTPS, directly or through a proxy that sets
`X-Forwarded-Proto: https`. Writes from other origins are refused. Failed logins are
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
