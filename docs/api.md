# API

Base path `/api/v1`. JSON in and out; errors are `{"error": "message"}` with a matching
status code. Unknown paths and wrong methods under `/api/` return a JSON 404.
Streams (logs, exec, events, agent tunnel) use WebSocket.

Auth (from phase 1): session cookie from the web UI or `Authorization: Bearer <token>`.
Routes marked *public* need neither.

## Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/api/v1/health` | public | `{"status":"ok","version":"v1.2.3"}` |

Add a row (and a section below, for anything non-trivial) with every new endpoint.
