# nudge

A small self-hosted Docker manager: one container with the Docker socket gives you a web
UI and a REST API for your containers, images, volumes, networks and stacks, and
remote Docker hosts join through an agent (the same image) that dials out to the server.

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
| `NUDGE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `NUDGE_ADMIN_PASSWORD` | unset | create user `admin` at first start (env only) |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker API of the local environment |
| `NUDGE_TLS_CERT`, `NUDGE_TLS_KEY` | unset | serve HTTPS with these PEM files (reloaded on change or SIGHUP) |
| `NUDGE_PUBLIC_URL` | unset | URL browsers and agents reach the server at; used in the agent command |
| `NUDGE_AUDIT_RETENTION_DAYS` | `90` | days the audit log keeps entries |

Remote hosts run the same image as `nudge agent` with `NUDGE_SERVER_URL` and
`NUDGE_JOIN_TOKEN` (the UI's "Add environment" prints the `docker run` command).

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

Layout: `backend/` (Go 1.27 module: `cmd/nudge`, `internal/...`), `frontend/` (Vue 3,
TypeScript, Vite+), `deploy/`, `docs/`. Architecture: [docs/architecture.md](docs/architecture.md).
