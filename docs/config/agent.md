# Agent configuration (`nudge agent`, phase 2)

Not implemented yet; this is the target interface (docs/architecture.md has the design).
The agent runs on a remote Docker host and dials out to the server, so it needs no
inbound port.

```sh
docker run -d --name nudge-agent --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock -v nudge-agent:/data \
  -e NUDGE_SERVER_URL=https://nudge.example.net \
  -e NUDGE_JOIN_TOKEN=<from the UI> \
  registrydedic.zefirlabs.net/nudge:latest agent
```

| Flag | Env | Default | Description |
|---|---|---|---|
| `-server` | `NUDGE_SERVER_URL` | (required) | the server's public URL |
| `-join-token` | `NUDGE_JOIN_TOKEN` | | one-time token from "Add environment"; needed only on the first connect |
| `-data` | `NUDGE_DATA` | `/data` | stores `agent.json` (agent ID and secret) |
| `-insecure` | `NUDGE_INSECURE` | `false` | allow `http://` server URLs (testing only) |
| `-log-level` | `NUDGE_LOG_LEVEL` | `info` | as for the server |
