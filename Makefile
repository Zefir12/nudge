# --- CONFIG ---
REGISTRY ?= registrydedic.zefirlabs.net
IMAGE ?= $(REGISTRY)/nudge
TAG ?= latest

# Keep in sync with backend/go.mod, the Dockerfile and .forgejo/workflows/ci.yaml.
GO_IMAGE ?= golang:1.27.3
# golangci-lint must be built with a Go >= go.mod's language version.
LINT_IMAGE ?= golangci/golangci-lint:v2.15.0
# The frontend's toolchain (Vite+, from frontend/package.json).
NODE_IMAGE ?= node:24.22.1

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X git.zefirlabs.net/zefir/nudge/backend/internal/version.Version=$(VERSION)

# Tooling runs in Docker by default (only Docker and make needed on the host).
# `make test LOCAL=1` uses the host's go / golangci-lint / npm instead. Module and build
# caches live in named volumes so repeated runs are fast.
ifeq ($(LOCAL),1)
GO := cd backend && go
LINT := cd backend && golangci-lint
NPM := cd frontend && npm
NPX := cd frontend && npx
else
DOCKER_RUN := docker run --rm -v "$(CURDIR)":/src \
	-v nudge-gomod:/go/pkg/mod -v nudge-gocache:/root/.cache
GO := $(DOCKER_RUN) -w /src/backend $(GO_IMAGE) go
LINT := $(DOCKER_RUN) -w /src/backend $(LINT_IMAGE) golangci-lint
WEB_RUN := docker run --rm -v "$(CURDIR)/frontend":/web -w /web $(NODE_IMAGE)
NPM := $(WEB_RUN) npm
NPX := $(WEB_RUN) npx
endif

.PHONY: test lint format tidy run web-install web-test web-check web-format web-build web-dev \
	build push publish publish-hash

# --- DEV ---

# Go tests (race detector), then the frontend's.
test: frontend/node_modules
	$(GO) test -race ./...
	$(NPX) vp test run

# golangci-lint (vet, staticcheck, gosec, gofmt/goimports …; backend/.golangci.yml),
# then the frontend's format, lint and type checks.
lint: frontend/node_modules
	$(LINT) run ./...
	$(NPX) vp check
	$(NPX) vue-tsc --noEmit

# Applies gofmt + goimports, and the frontend's formatter and lint fixes.
format: frontend/node_modules
	$(LINT) fmt ./...
	$(NPX) vp check --fix

tidy:
	$(GO) mod tidy

# Builds the frontend into the backend and runs the server on the host's Docker
# (http://localhost:8080). Needs a Go toolchain on the host; otherwise use `make build`
# and `docker compose -f deploy/compose.yml up`.
run: web-build
	cd backend && NUDGE_DATA=$(CURDIR)/.data go run -ldflags "$(LDFLAGS)" ./cmd/nudge serve

# --- FRONTEND (frontend/: Vue 3 + Vite+) ---

frontend/node_modules: frontend/package.json frontend/package-lock.json
	$(NPM) ci --no-audit --no-fund
	@touch $@

web-install: frontend/node_modules

web-test: frontend/node_modules
	$(NPX) vp test run

web-check: frontend/node_modules
	$(NPX) vp check
	$(NPX) vue-tsc --noEmit

web-format: frontend/node_modules
	$(NPX) vp check --fix

# Builds frontend/dist and copies it to backend/internal/web/dist, which the binary embeds.
web-build: frontend/node_modules
	$(NPM) run build
	find backend/internal/web/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
	cp -r frontend/dist/. backend/internal/web/dist/

# Vite dev server with hot reload on :5173, proxying /api to a server on :8080.
# Uses the host's Vite+ (`vp`), since it serves on the host.
web-dev:
	cd frontend && vp install && vp dev

# --- IMAGE (publishing is done by zefir, not by Claude) ---

build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) .

push:
	docker push $(IMAGE):$(TAG)

publish:
	docker buildx build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) --push .

publish-hash:
	tag=$$(git rev-parse --short HEAD) && \
	docker buildx build --build-arg VERSION=$$tag -t $(IMAGE):$$tag --push .
