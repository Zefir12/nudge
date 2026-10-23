# Server configuration (`nudge serve`)

Every option is a flag with an environment variable fallback; the defaults work in the
image without setting anything. A flag wins over its variable.

| Flag | Env | Default | Description |
|---|---|---|---|
| `-addr` | `NUDGE_ADDR` | `:8080` | listen address of the API and web UI |
| `-data` | `NUDGE_DATA` | `/data` | data directory (database, stacks); mount a volume here |
| `-log-level` | `NUDGE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`; logs are JSON on stderr |

Planned (see PLAN.md): `NUDGE_ADMIN_PASSWORD` (create the admin at startup, phase 1),
`NUDGE_TLS_CERT` / `NUDGE_TLS_KEY` (serve HTTPS, phase 2), `NUDGE_PUBLIC_URL` (the URL
agents and links use, phase 2).

## Data directory
`nudge serve` creates the data directory (mode 0700) if it is missing and exits with an
error if it cannot write there. The database is `<data>/nudge.db` (SQLite in WAL mode, so
`nudge.db-wal` and `nudge.db-shm` sit next to it); schema migrations run at start. Back
up the whole directory while the server is stopped. A database migrated by a newer nudge
is refused by an older one.

## Docker access
The server manages the Docker host whose socket is mounted at `/var/run/docker.sock`
(`DOCKER_HOST` overrides it). Socket access is root-equivalent on that host; the
container needs no `--privileged` and no extra capabilities.
