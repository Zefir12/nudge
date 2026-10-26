# One image for both roles: `nudge serve` (default) and, from PLAN.md phase 2,
# `nudge agent`. Keep the base images in sync with the Makefile and ci.yaml.

FROM node:24.22.1 AS web

WORKDIR /web
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# The static docker-compose v2 binary that deploys stacks (docs/architecture.md, Stacks).
FROM docker/compose-bin:v2.39.1 AS compose

FROM golang:1.27.3 AS builder

WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
COPY --from=web /web/dist/ ./internal/web/dist/
ARG VERSION=dev
# Static binary: modernc.org/sqlite is pure Go, so no cgo is needed.
RUN CGO_ENABLED=0 go build -trimpath \
	-ldflags "-s -w -X git.zefirlabs.net/zefir/nudge/backend/internal/version.Version=${VERSION}" \
	-o /out/nudge ./cmd/nudge
# The data directory, made here because distroless has no shell.
RUN mkdir -m 0700 /out/data

# Runs as root: it talks to the Docker socket, which is root-equivalent anyway, and
# root needs no group juggling for the socket's GID. No --privileged needed.
FROM gcr.io/distroless/static-debian13

COPY --from=builder /out/nudge /usr/local/bin/nudge
COPY --from=compose /docker-compose /usr/local/bin/docker-compose
COPY --from=builder /out/data /data
VOLUME /data
ENV NUDGE_DATA=/data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/nudge"]
CMD ["serve"]
