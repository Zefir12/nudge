# Plan

nudge: a self-hosted Docker manager. One privileged-socket container serves an API and a
Vue UI; remote hosts join through the agent (`nudge-agent`). Design: docs/architecture.md.

Each task below becomes one issue (one branch, one PR). Tasks within a phase are in
dependency order. Every task includes tests, docs (docs/api.md, docs/config/, README) and
`make test lint` green.

## Phase 0: skeleton ✅
- Repo layout (backend/, frontend/), Makefile (Docker by default, `LOCAL=1`), Dockerfile
  (single static image), CI and image workflows, Claude Code setup.
- `nudge serve` with `/api/v1/health` and the embedded SPA; Vue shell with a dashboard.

## Phase 1: core server, local environment
1. **Store**: `internal/store` on `modernc.org/sqlite`, embedded numbered migrations,
   WAL, one writer; opened from `NUDGE_DATA`. Tables: users, sessions, api_tokens,
   environments.
2. **Auth backend**: `internal/auth` with argon2id; first-run setup (`GET/POST /setup`),
   `NUDGE_ADMIN_PASSWORD`; login/logout, cookie sessions with expiry (injected clock),
   Origin check on writes, bounded per-IP/user login rate limit; auth middleware.
3. **API tokens**: create (shown once), list, revoke; Bearer auth in the middleware.
4. **Frontend auth**: setup and login views, router guard, logout, 401 → login redirect
   in `apiFetch`.
5. **Environments + Docker client**: `internal/docker` (moby client per environment,
   dialer per kind, an interface for handlers); `local` environment auto-created when
   the socket is reachable; `GET /environments`, environment info (`/info`, `/version`).
6. **Read-only resources**: list + inspect containers, images, volumes, networks per
   environment.
7. **Frontend resources**: environment switcher, dashboard with host info and counts,
   list and detail views for the four resource kinds.

## Phase 2: remote agents
1. **Tunnel**: `internal/tunnel`, WebSocket (`coder/websocket`) as `net.Conn`, yamux
   session, hello message with versions; tests over `net.Pipe`/`httptest`.
2. **Enrollment**: agent environments, join tokens (hashed, single use, 24 h expiry),
   agent ID + secret exchange, revoke; `POST /environments` for kind `agent`.
3. **Server side**: `GET /api/v1/agents/connect` (WebSocket), agent registry (one
   session per agent, replace on reconnect), online/offline + `last_seen`, Docker client
   dialing through the session.
4. **`nudge agent`**: config (docs/config/agent.md), `/data/agent.json`, dial with
   backoff + jitter, pipe each stream to the local socket, graceful shutdown.
5. **TLS + public URL**: `NUDGE_TLS_CERT/KEY`, `NUDGE_PUBLIC_URL`; agent refuses
   `http://` without `NUDGE_INSECURE`.
6. **Frontend**: "Add environment" with the generated `docker run` command, status
   badges, revoke.
7. **End-to-end test**: server and agent in one test process over `httptest`, with a fake
   Docker API behind the agent's "socket".

## Phase 3: actions
1. Container start/stop/restart/kill/remove, with confirmation in the UI.
2. Image pull with streamed progress, remove, prune; registry credentials (stored encrypted).
3. Volume and network create/remove/prune.
4. Container create (image, name, ports, env, volumes, restart policy) and recreate.
5. Audit log of every write (user, environment, action, target), viewable in the UI.

## Phase 4: live
1. Container logs over WebSocket (follow, tail, timestamps), log viewer.
2. Stats (CPU, memory, network) per container and environment.
3. Exec terminal (xterm.js: needs zefir's approval as a dependency).
4. Docker events stream → live-updating lists.

## Phase 5: stacks
1. Decide compose execution for agent environments (docs/architecture.md, Stacks).
2. Stacks: create/edit compose YAML + env, deploy, update (pull + up), down; files in
   `/data/stacks`.
3. Stack view: services, containers, logs.

## Phase 6: nudges (updates)
1. Image update check (registry digest vs. local) per container, scheduled.
2. One-click update (pull + recreate) for containers and stacks.
3. Notifications (webhook) for available updates and failed containers.

## Later / ideas
- Users and roles (read-only, per-environment access), OIDC login.
- Backup/restore of `/data`.
