# Two images, one per role: `--target server` (the default, the last stage) is nudge,
# the API with the embedded web UI; `--target agent` is nudge-agent, the agent alone
# (no web UI, no server code). Keep the base images in sync with the Makefile and ci.yaml.

FROM node:24.22.1 AS web

WORKDIR /web
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# The static docker-compose v2 binary that deploys stacks (docs/architecture.md, Stacks).
FROM docker/compose-bin:v2.39.1 AS compose

FROM golang:1.27.3 AS gobase

WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
ARG VERSION=dev
ENV LDFLAGS="-s -w -X git.zefirlabs.net/zefir/nudge/backend/internal/version.Version=${VERSION}"
# The data directory, made here because distroless has no shell.
RUN mkdir -m 0700 /out /out/data

# Static binaries: modernc.org/sqlite is pure Go, so no cgo is needed.
FROM gobase AS agent-builder
RUN CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o /out/nudge-agent ./cmd/nudge-agent

FROM gobase AS server-builder
COPY --from=web /web/dist/ ./internal/web/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o /out/nudge ./cmd/nudge

# Both run as root: they talk to the Docker socket, which is root-equivalent anyway, and
# root needs no group juggling for the socket's GID. No --privileged needed. Both carry
# docker-compose, since stacks deploy next to the daemon they run on.
FROM gcr.io/distroless/static-debian13 AS agent

COPY --from=agent-builder /out/nudge-agent /usr/local/bin/nudge-agent
COPY --from=compose /docker-compose /usr/local/bin/docker-compose
COPY --from=agent-builder /out/data /data
VOLUME /data
ENV NUDGE_DATA=/data
ENTRYPOINT ["/usr/local/bin/nudge-agent"]
CMD ["agent"]

FROM gcr.io/distroless/static-debian13 AS server

COPY --from=server-builder /out/nudge /usr/local/bin/nudge
COPY --from=compose /docker-compose /usr/local/bin/docker-compose
COPY --from=server-builder /out/data /data
VOLUME /data
ENV NUDGE_DATA=/data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/nudge"]
CMD ["serve"]
