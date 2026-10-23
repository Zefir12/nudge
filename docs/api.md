# API

Base path `/api/v1`. JSON in and out; errors are `{"error": "message"}` with a matching
status code. Unknown paths and wrong methods under `/api/` return a JSON 404 to a
logged-in client and a JSON 401 otherwise.
Streams (logs, exec, events, agent tunnel) use WebSocket.

Auth: the `nudge_session` cookie set by login or setup (`Authorization: Bearer <token>`
comes with API tokens). Routes marked *public* need neither; every other route answers
`401 {"error":"authentication required"}` without a valid session.

Cross-origin writes are refused with `403`: a `POST`/`PUT`/`PATCH`/`DELETE` must carry
`Sec-Fetch-Site: same-origin`, or an `Origin` whose host matches `Host`, or neither header
(non-browser clients).

## Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/api/v1/health` | public | `{"status":"ok","version":"v1.2.3"}` |
| GET | `/api/v1/setup` | public | `{"needed":true}` while no user exists |
| POST | `/api/v1/setup` | public | create the admin and log in; see below |
| POST | `/api/v1/auth/login` | public | log in; see below |
| POST | `/api/v1/auth/logout` | public | end the session and clear the cookie; `204` |
| GET | `/api/v1/auth/me` | session | the logged-in user |

Add a row (and a section below, for anything non-trivial) with every new endpoint.

### Setup and login
`POST /api/v1/setup` and `POST /api/v1/auth/login` take
`{"username": "admin", "password": "..."}` and answer with the user
`{"id": 1, "username": "admin", "created_at": "2026-10-24T12:00:00Z"}` (`201` for setup,
`200` for login) plus the session cookie: `HttpOnly`, `SameSite=Strict`, `Path=/`,
`Secure` when the request came over HTTPS (directly or with `X-Forwarded-Proto: https`),
valid for 7 days. `GET /api/v1/auth/me` returns the same user object.

- Setup works only while no user exists, else `409`. The username is 1 to 64 bytes
  without control characters or surrounding spaces; the password at least 8 characters
  (at most 1024 bytes). Invalid input is `400` with the reason.
- Usernames are case-insensitive. A wrong password and an unknown user both get
  `401 {"error":"invalid username or password"}`.
- Failed logins are limited to 20 per client IP and 10 per username in 15 minutes; then
  login answers `429` with `Retry-After` (seconds), even with the right password. Behind
  a reverse proxy all clients share the proxy's IP.
