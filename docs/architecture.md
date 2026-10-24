# Architecture

## Goals
- `docker run` with the socket mounted and a volume gives a working UI; the first visit
  creates the admin. No config file needed.
- Remote Docker hosts from the start, through an agent that needs no inbound port.
- One public API that the web UI and scripts share.

## Components

```
browser ──HTTPS──▶ nudge serve ──unix socket──▶ local dockerd
                       ▲
                       │ WebSocket (agent dials out), yamux streams
                       │
                  nudge agent ──unix socket──▶ remote dockerd
```

- **Server** (`nudge serve`): API under `/api/v1`, embedded web UI, SQLite store in
  `/data/nudge.db`, the registry of connected agents.
- **Agent** (`nudge agent`): same binary and image, on each remote host with that host's
  socket mounted. It dials the server; it has no API of its own and stores only its
  credential in `/data`.
- **Environment**: one Docker host the server manages. Kinds: `local` (a socket on the
  server's host, created on first start if `/var/run/docker.sock` is reachable) and
  `agent`. Every Docker route is scoped to one: `/api/v1/environments/{env}/...`.

## Docker access
`internal/docker` builds one `*client.Client` (github.com/moby/moby/client) per
environment. The only difference between kinds is the dialer:
- local: dial the unix socket;
- agent: open a new yamux stream on that agent's session.

So every Docker feature (lists, pull with progress, logs, attach/exec hijacking, events)
works the same for remote hosts without a per-feature agent protocol. Handlers use a
small interface over the client, so tests run against fakes.

## Agent tunnel (`internal/tunnel`, `internal/agent`)
1. Admin creates an agent environment in the UI. The server stores a **join token**
   (random 32 bytes, stored as a SHA-256 hash, single use, expires after 24 h) and shows the
   `docker run` command for the agent.
2. The agent sends the join token to `POST /api/v1/agents/enroll` (plain HTTPS, before
   any WebSocket). The server answers with an agent ID and a long-lived **agent secret**
   (hashed on the server); the agent saves both to `/data/agent.json` (0600) and forgets
   the join token. Then it connects to `wss://<server>/api/v1/agents/connect`.
3. Every connect authenticates with ID + secret (HTTP Basic auth on the upgrade request).
   A second connection for the same agent replaces the first. Revoking the environment
   (or enrolling it again) deletes the old secret and drops the session. The sessions
   live in `environments.Service` (`ServeAgent`), one per environment; the agent
   environment's Docker client dials a new yamux stream on the current session per
   connection, so a reconnect needs no new client.
4. After the handshake the WebSocket is wrapped in a `net.Conn` and a yamux session where
   the **server is the yamux client**: it opens streams, and the agent accepts each one and
   pipes it to its local Docker socket. A small JSON hello at connect carries the agent's
   version and host name; liveness uses yamux keepalives.
   Wire format (`internal/tunnel`): the agent sends one text message
   `{"protocol": 1, "version": "...", "hostname": "..."}`, the server answers
   `{"protocol": 1, "version": "..."}` or `{"protocol": 1, "error": "..."}` and closes
   (policy violation) on a protocol mismatch or invalid fields; after that every message
   is binary yamux data. Keepalive pings every 15 s, the session closes when one isn't
   answered within 10 s. Streams opened by the agent are refused.
5. The agent reconnects with exponential backoff and jitter (1 s → 60 s). The server marks
   the environment offline when the session closes and records `last_seen`.

Trust: the server has full Docker access to every agent's host (that is the point); an
agent can reach nothing on the server except its own tunnel endpoint. The server must
use TLS for agents on untrusted networks (`NUDGE_TLS_*` or a reverse proxy); the agent
refuses `ws://` unless `NUDGE_INSECURE=1`.

## Auth
- Users with argon2id password hashes. First start with no users: the API only allows
  `GET /health`, `GET /setup` and `POST /setup` (create the admin), unless
  `NUDGE_ADMIN_PASSWORD` creates it at startup.
- Browser: session cookie (`HttpOnly`, `Secure` behind TLS, `SameSite=Strict`), sessions
  in SQLite with expiry; writes additionally require a matching `Origin`.
- Scripts: API tokens (`Authorization: Bearer nudge_...`), stored hashed, shown once.
- Login attempts are rate-limited per IP and username, with bounded memory.

## Stacks (phase 5, open question)
Compose files live in `/data/stacks/<env>/<name>/`. For local environments the server runs
the compose library/CLI against the socket. For agent environments relative bind mounts
and `env_file` refer to the remote host, so compose must run on the agent: the agent then
needs one small control endpoint ("deploy this compose project"), the only exception to
"the agent is a dumb pipe". Decide when phase 5 starts.

## Storage
SQLite (`modernc.org/sqlite`, pure Go, so the image stays static) with numbered SQL
migrations embedded in the binary and applied at start. One writer connection; WAL mode.
