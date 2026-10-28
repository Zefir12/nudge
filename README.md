<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/brand/nudge-dark-lockup.svg">
  <img src="docs/brand/nudge-light-lockup.svg" alt="nudge" height="64">
</picture>

# nudge

A small self-hosted Docker manager: one container with the Docker socket gives you a web
UI and a REST API for your containers, images, volumes, networks and stacks, and
remote Docker hosts join through an agent (its own image, `nudge-agent`) that dials out
to the server.

Status: early. Phase 0 (skeleton) is done; see [PLAN.md](PLAN.md) for the roadmap.

## Run

```sh
docker run -d --name nudge --restart unless-stopped \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v nudge-data:/data \
  registrydedic.zefirlabs.net/nudge:latest
```

Open http://localhost:8080. Or use [deploy/compose.yml](deploy/compose.yml).
No `--privileged` is needed: the socket is all it uses. The socket gives root on the host,
so keep nudge behind TLS: its own (`NUDGE_TLS_*`) or a reverse proxy's (see
[docs/config/server.md](docs/config/server.md#reverse-proxy)).

## Configuration

Flags with `NUDGE_*` environment variable fallbacks; the defaults work in the image.

| Env | Default | |
|---|---|---|
| `NUDGE_ADDR` | `:8080` | listen address |
| `NUDGE_DATA` | `/data` | data directory |
| `NUDGE_STACKS_DIR` | `<data>/stacks` | compose stacks; mount it at the same path on the host |
| `NUDGE_COMPOSE` | `/usr/local/bin/docker-compose` | docker-compose binary for stacks |
| `NUDGE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `NUDGE_ADMIN_PASSWORD` | unset | create user `admin` at first start (env only) |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker API of the local environment |
| `NUDGE_TLS_CERT`, `NUDGE_TLS_KEY` | unset | serve HTTPS with these PEM files (reloaded on change or SIGHUP) |
| `NUDGE_PUBLIC_URL` | unset | URL browsers and agents reach the server at; used in the agent command |
| `NUDGE_TRUSTED_PROXIES` | unset | reverse proxies (CIDRs or IPs) whose `X-Forwarded-For`/`-Proto` are trusted |
| `NUDGE_AUDIT_RETENTION_DAYS` | `90` | days the audit log keeps entries |
| `NUDGE_UPDATE_CHECK_INTERVAL` | `6h` | how often images are checked for updates, `0` for never |
| `NUDGE_USAGE_INTERVAL` | `15m` | how often volume sizes are measured, `0` for never |
| `NUDGE_BACKUP_INTERVAL` | `0` (off) | how often an encrypted backup is saved to `<data>/backups` |
| `NUDGE_BACKUP_KEEP` | `7` | how many scheduled backups are kept |
| `NUDGE_BACKUP_PASSPHRASE_FILE` | unset | file with the backups' passphrase (needed for scheduled backups and `nudge restore`) |
| `NUDGE_OIDC_ISSUER`, `NUDGE_OIDC_CLIENT_ID`, `NUDGE_OIDC_CLIENT_SECRET` | unset | single sign-on through an OpenID Connect provider (secret env only; needs an `https://` public URL) |
| `NUDGE_OIDC_NAME` | `SSO` | label of the single sign-on button |
| `NUDGE_OIDC_ADMIN_ROLE` | `admin` | `roles` claim value that makes a user an admin |
| `NUDGE_OIDC_USER_ROLE` | `user` | `roles` claim value that makes a user a (non-admin) user |
| `NUDGE_OIDC_REQUIRE_MFA` | `false` | refuse single sign-on without a second factor |
| `NUDGE_OIDC_SESSION_TTL` | `12h` | how long a single sign-on session lasts |
| `NUDGE_PASSWORD_LOGIN` | `true` | `false` leaves password login only to the local `admin` (with SSO) |
| `NUDGE_TENANT_CGROUP_PARENT` | | cgroup parent of each user's containers, `{id}` for the user's ID (e.g. `nudge-u{id}.slice`) |
| `NUDGE_TENANT_RUNTIME` | | OCI runtime of users' containers (e.g. `runsc`) |
| `NUDGE_TENANT_STORAGE_SIZE` | | disk cap of a user's container's writable layer (e.g. `10G`) |
| `NUDGE_USER_REGISTRIES` | Docker Hub and the big public registries | registry hosts users' pulls may reach, `*` for any |
| `NUDGE_TENANT_DENIED_LABELS` | `traefik.,caddy,…` | label prefixes users may not set on their containers (reverse proxies, updaters), or `none` |

Remote hosts run the agent image, `registrydedic.zefirlabs.net/nudge-agent`, with `NUDGE_SERVER_URL` and
`NUDGE_JOIN_TOKEN` (the UI's "Add environment" prints the `docker run` command and a
compose file, as in [deploy/agent.compose.yml](deploy/agent.compose.yml)).

CI deploys a deployment with `nudge deploy --server URL --deployment NAME --var
NAME=value` (token in `NUDGE_DEPLOY_TOKEN`, server also from `NUDGE_SERVER_URL`); it
prints the progress and exits 0 only when the run succeeded. See
[docs/deployments.md](docs/deployments.md).

Admins download an encrypted backup from the UI's Backup page (or `POST /api/v1/backup`);
`nudge restore -file B -passphrase-file P` puts one back with the server stopped. See
[docs/config/server.md](docs/config/server.md#backup-and-restore).

Details: [docs/config/server.md](docs/config/server.md), agents:
[docs/config/agent.md](docs/config/agent.md). API: [docs/api.md](docs/api.md).

## Development

Needs only Docker and make: the Makefile runs Go, golangci-lint and Node in containers
(`LOCAL=1` uses host toolchains).

```sh
make test lint   # the gate for every commit
make web-build   # build the frontend into backend/internal/web/dist
make build       # build the image
make web-dev     # Vite+ dev server on :5173, proxying /api to :8080 (host Node needed)
```

Layout: `backend/` (Go 1.27 module: `cmd/nudge`, `cmd/nudge-agent`, `internal/...`), `frontend/` (Vue 3,
TypeScript, Vite+), `deploy/`, `docs/`. Architecture: [docs/architecture.md](docs/architecture.md).
