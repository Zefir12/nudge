# Agent configuration (`nudge agent`)

The agent runs on a remote Docker host and dials out to the server, so it needs no
inbound port (docs/architecture.md has the design). "Add environment" in the UI prints
the command below with the server URL and a join token filled in.

```sh
docker run -d --name nudge-agent --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock -v nudge-agent:/data \
  -e NUDGE_SERVER_URL=https://nudge.example.net \
  -e NUDGE_JOIN_TOKEN=<from the UI> \
  registrydedic.zefirlabs.net/nudge:latest agent
```

It also shows the same agent as a compose file:
[deploy/agent.compose.yml](../../deploy/agent.compose.yml) with the values filled in.

| Flag | Env | Default | Description |
|---|---|---|---|
| `-server` | `NUDGE_SERVER_URL` | (required) | the server's public URL |
| `-join-token` | `NUDGE_JOIN_TOKEN` | | one-time token from "Add environment"; needed only on the first connect |
| `-data` | `NUDGE_DATA` | `/data` | stores `agent.json` (agent ID and secret) |
| `-insecure` | `NUDGE_INSECURE` | `false` | allow `http://` server URLs (testing only) |
| `-log-level` | `NUDGE_LOG_LEVEL` | `info` | as for the server |
| `-docker-host` | `DOCKER_HOST` | `unix:///var/run/docker.sock` | the Docker API the agent serves, `unix://` or `tcp://` |
| `-stacks-dir` | `NUDGE_STACKS_DIR` | `<data>/stacks` | the stacks the server deploys here, an absolute path; mount it at the same path on the host (see Stacks) |
| `-compose` | `NUDGE_COMPOSE` | `/usr/local/bin/docker-compose` | docker-compose binary that deploys them (shipped in the image) |

The server URL is `https://host[:port][/path]`; a path prefix is kept for servers behind
a reverse proxy. `http://` is refused unless `-insecure` is set, because the agent secret
and all Docker traffic would travel in clear text.

## Stacks
The server deploys an agent environment's stacks through the agent: it sends the stack's
`compose.yaml` and `.env` over the tunnel, and the agent writes them to
`<stacks dir>/<name>/` (replaced on each deploy) and runs `docker-compose` there against
its `DOCKER_HOST`. As on the server, the daemon mounts relative paths
(`./data:/data`) from the host, so mount the stacks directory **at the same path on the
host and in the container** if stacks use relative bind mounts:
`-v /opt/nudge/stacks:/opt/nudge/stacks -e NUDGE_STACKS_DIR=/opt/nudge/stacks`. With the
default (inside the `/data` volume) stacks with named volumes only work as they are.
Agents older than the server's tunnel protocol 2 keep working for everything but stacks,
which answer "update the agent to use stacks".

## Credentials
On the first start the agent sends the join token to the server and saves the agent ID
and secret it gets back to `<data>/agent.json` (mode 0600). Later starts use that file,
so the join token (single use, 24 h) can stay in the environment: it is only spent again
when the server refuses the saved credentials. Keep the `/data` volume: without
`agent.json` the agent needs a new join token.

The agent exits (code 1) instead of retrying when retrying can't help:
- no `agent.json` and no join token;
- the join token is refused (used, expired or replaced by a newer one);
- the saved credentials are refused (the environment was revoked) and there is no unused
  join token. Create a new join token in the UI and restart the agent with it.

## Connection
The agent connects to `/api/v1/agents/connect` and pipes every stream the server opens to
its Docker socket. When the connection fails or drops it reconnects after an exponential
backoff with jitter: 1 s, doubling up to 60 s, each delay randomly shortened by up to
half. A connection that stayed up for a minute starts the backoff over. SIGTERM or
SIGINT closes the tunnel and stops the agent.
