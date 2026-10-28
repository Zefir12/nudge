# Server configuration (`nudge serve`)

Every option is a flag with an environment variable fallback; the defaults work in the
image without setting anything. A flag wins over its variable.

| Flag | Env | Default | Description |
|---|---|---|---|
| `-addr` | `NUDGE_ADDR` | `:8080` | listen address of the API and web UI |
| `-data` | `NUDGE_DATA` | `/data` | data directory (database, stacks, `compose-config/` with the short-lived registry logins of a stack deploy); mount a volume here |
| `-stacks-dir` | `NUDGE_STACKS_DIR` | `<data>/stacks` | compose stacks, an absolute path; mount it at the same path on the host (see Stacks) |
| `-compose` | `NUDGE_COMPOSE` | `/usr/local/bin/docker-compose` | docker-compose binary that deploys stacks (shipped in the image) |
| `-log-level` | `NUDGE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`; logs are JSON on stderr |
| `-docker-host` | `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker API of the local environment, `unix://` or `tcp://` |
| `-tls-cert` | `NUDGE_TLS_CERT` | unset | PEM certificate (chain); with `-tls-key` the server speaks HTTPS only |
| `-tls-key` | `NUDGE_TLS_KEY` | unset | PEM private key of `-tls-cert` |
| `-public-url` | `NUDGE_PUBLIC_URL` | unset | URL browsers and agents reach the server at, `https://host[:port]` |
| `-trusted-proxies` | `NUDGE_TRUSTED_PROXIES` | unset | comma-separated CIDRs or IPs of reverse proxies whose `X-Forwarded-For` and `X-Forwarded-Proto` are trusted (see Reverse proxy) |
| `-audit-retention-days` | `NUDGE_AUDIT_RETENTION_DAYS` | `90` | days the audit log keeps entries (at least 1); older ones are deleted hourly |
| `-update-check-interval` | `NUDGE_UPDATE_CHECK_INTERVAL` | `6h` | how often every environment's images are checked for updates (a Go duration, at least `5m`; `0` turns the scheduled check off, the UI's check still works); the first check runs two minutes after start |
| `-usage-interval` | `NUDGE_USAGE_INTERVAL` | `15m` | how often every environment's volume sizes are measured (a Go duration, at least `1m`; `0` turns the scheduled measurement off, the UI's "Measure sizes" still works); the first measurement runs two minutes after start. Docker walks every volume to size it, so on hosts with large volumes a longer interval is kinder to the disks |
| — | `NUDGE_ADMIN_PASSWORD` | unset | create the user `admin` with this password at start if no user exists |
| `-oidc-issuer` | `NUDGE_OIDC_ISSUER` | unset | OpenID Connect issuer URL (`https://`); with the client ID and secret it turns on single sign-on (see Single sign-on) |
| `-oidc-client-id` | `NUDGE_OIDC_CLIENT_ID` | unset | nudge's client ID at the provider |
| — | `NUDGE_OIDC_CLIENT_SECRET` | unset | nudge's client secret (environment only) |
| `-oidc-name` | `NUDGE_OIDC_NAME` | `SSO` | label of the login page's button ("Log in with …") |
| `-oidc-admin-role` | `NUDGE_OIDC_ADMIN_ROLE` | `admin` | value of the ID token's `roles` claim that makes a user an admin |
| `-oidc-user-role` | `NUDGE_OIDC_USER_ROLE` | `user` | value of the `roles` claim that makes a user a (non-admin) user; `-oidc-user-role=""` signs in admins only; must differ from the admin role |
| `-oidc-require-mfa` | `NUDGE_OIDC_REQUIRE_MFA` | `false` | refuse sign-ins whose `amr` claim lacks `mfa` |
| `-oidc-session-ttl` | `NUDGE_OIDC_SESSION_TTL` | `12h` | how long a single sign-on session lasts (at least `5m`) |
| `-password-login` | `NUDGE_PASSWORD_LOGIN` | `true` | `false` hides the password form and refuses password login to everyone but the local `admin` (needs single sign-on) |
| `-tenant-cgroup-parent` | `NUDGE_TENANT_CGROUP_PARENT` | | cgroup parent of every user's containers, `{id}` standing for the user's ID: a systemd slice name (`nudge-u{id}.slice`) or a cgroupfs path (`/nudge/u{id}`); see [Hosting other users](#hosting-other-users) |
| `-tenant-runtime` | `NUDGE_TENANT_RUNTIME` | | OCI runtime of every user's container, e.g. `runsc` (gVisor); users' containers aren't created or started on a daemon that doesn't list it |
| `-tenant-storage-size` | `NUDGE_TENANT_STORAGE_SIZE` | | disk cap of a user's container's writable layer (`10G`, `512M`; binary units, at least `64M`), where the storage driver supports it |
| `-user-registries` | `NUDGE_USER_REGISTRIES` | see [Registries users can pull from](#registries-users-can-pull-from) | comma-separated registry hosts (with the port, if any) users' pulls may reach, `*` for any |

## Data directory
`nudge serve` creates the data directory (mode 0700) if it is missing and exits with an
error if it cannot write there. The database is `<data>/nudge.db` (SQLite in WAL mode, so
`nudge.db-wal` and `nudge.db-shm` sit next to it); schema migrations run at start.
`<data>/secret.key` (32 random bytes, mode 0600, created at the first start) encrypts the
registry passwords and the notification webhooks' URLs and header values in the database.
Back up the whole directory while the server is stopped; without its `secret.key` a
restored database's registry passwords and webhooks must be entered again. A database migrated by a newer nudge
is refused by an older one.

## Stacks
Stacks live in `<stacks dir>/<environment ID>/<name>/` (`compose.yaml`, `.env`), and the
server runs `docker-compose` on them against the local environment's Docker
(`DOCKER_HOST`). compose resolves relative paths (`./data:/data`, `env_file`, `configs`)
in that directory, but the daemon mounts them from the host, so mount the stacks
directory **at the same path on the host and in the container**:
`-v /opt/nudge/stacks:/opt/nudge/stacks -e NUDGE_STACKS_DIR=/opt/nudge/stacks`.
With the default (inside the `/data` volume) stacks that use only named volumes work,
but relative bind mounts would get an empty directory on the host, so deploying such a
stack fails with `409`: before `up`, nudge reads the project with `docker-compose config`
and inspects its own container, and refuses a bind source (or `file:` secret or config
a service uses) under the stacks directory unless that path is bind-mounted into nudge
from the same path on the host. Running outside a container, nothing is checked; if the
check itself fails (compose or the daemon errs), nudge logs a warning and deploys. compose gets only
`DOCKER_HOST`, `HOME` and `PATH`, none of nudge's own environment. Stacks of agent
environments are deployed by the agent on its host (docs/config/agent.md, Stacks); the
server keeps the source files here either way.

## First user
With no user in the database the web UI asks for an admin username and password
(`POST /api/v1/setup`); until then only health, setup and login answer. To skip that
step, set `NUDGE_ADMIN_PASSWORD`: at start, if no user exists, nudge creates `admin` with
it (at least 8 characters, else the server refuses to start). Once a user exists the
variable is ignored, so changing it later does not change the password. It has no flag,
so the password never shows in the process list.

## TLS
Agents send their secret and all Docker traffic through the server, and the session
cookie grants root on every host, so anything beyond a trusted LAN needs HTTPS: either
nudge serves it itself or a reverse proxy terminates it.

**nudge serves HTTPS:** set both `NUDGE_TLS_CERT` and `NUDGE_TLS_KEY` (setting only one
is an error) to PEM files, for example mounted read-only from the host. The listen
address (`NUDGE_ADDR`, `:8080` by default) then speaks only HTTPS, TLS 1.2 or newer.
A certificate that can't be loaded stops the server at start. Renewed files are picked
up without a restart: nudge checks them every 30 s and reloads on `SIGHUP`
(`docker kill -s HUP nudge`). If a reload fails (a half-written file, a key that doesn't
match) the previous certificate stays in use and the error is logged. Automatic
certificates (ACME) are not built in; use a proxy for that.

```sh
docker run -d --name nudge -p 443:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock -v nudge-data:/data \
  -v /etc/nudge/tls:/tls:ro \
  -e NUDGE_TLS_CERT=/tls/fullchain.pem -e NUDGE_TLS_KEY=/tls/privkey.pem \
  -e NUDGE_PUBLIC_URL=https://nudge.example.net \
  registrydedic.zefirlabs.net/nudge:latest
```

## Public URL
`NUDGE_PUBLIC_URL` is the address the server is reached at, `http(s)://host[:port]`
(a trailing slash is dropped; a path, query or credentials are refused: nudge serves from
the root). It goes into the `docker run` command "Add environment" shows, so agents dial
the right address even when you opened the UI through another one (an IP, an SSH
tunnel). Unset, nudge uses the host of each request, with `https://` when the request
came over TLS or with `X-Forwarded-Proto: https` from a trusted proxy.

An `https://` public URL, like serving TLS, also marks the session cookie `Secure` on
every response, so the UI must then be opened over HTTPS: a browser on plain HTTP (the
proxy bypassed, straight to port 8080) drops the cookie and login fails.

## Reverse proxy
Run nudge on plain HTTP on a private network and let the proxy terminate TLS. The proxy
must:
- pass the `Host` header through unchanged (cross-origin writes are checked against it),
- set `X-Forwarded-For` and `X-Forwarded-Proto` (both only count from a trusted proxy,
  see below),
- forward WebSocket upgrades on `/api/v1/agents/connect` (and the log, exec and event
  streams), with no read timeout shorter than a few minutes: the agent tunnel is one
  long-lived request kept alive by pings every 15 s.

Caddy does all of this by default:

```
nudge.example.net {
	reverse_proxy nudge:8080
}
```

nginx:

```nginx
location / {
    proxy_pass http://nudge:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;  # map $http_upgrade, see nginx docs
    proxy_read_timeout 1h;
    proxy_buffering off;
}
```

Set `NUDGE_PUBLIC_URL=https://nudge.example.net` as well.

### Trusted proxies
`X-Forwarded-For` and `X-Forwarded-Proto` are only believed when the TCP peer is in
`NUDGE_TRUSTED_PROXIES`, a comma-separated list of CIDRs or single IPs (IPv4 or IPv6),
e.g. `NUDGE_TRUSTED_PROXIES=172.16.0.0/12` for a proxy on a Docker network, or the
proxy container's fixed IP. From any other peer both headers are ignored, as anyone can
send them. Unset (the default), nudge trusts no proxy:

- the client IP (login rate limit, logs) is the TCP peer, so behind a proxy all clients
  share the proxy's IP and its failed-login budget (see docs/api.md). Failed deploy
  webhook checks (`POST /api/v1/hooks/{id}` with a wrong or missing secret) count
  against the same budget, so a CI job with a stale secret can use it up: after 20
  failures in 15 minutes, logins and webhook triggers through the proxy get `429` until
  the window ends. Fix or rotate the secret in CI promptly, or trust the proxy;
- `X-Forwarded-Proto` is ignored, so set `NUDGE_PUBLIC_URL=https://…` for `Secure`
  cookies and `https://` agent commands.

From a trusted peer, the client is the right-most `X-Forwarded-For` entry that is not a
trusted proxy (entries further left were sent by the client and can be forged), so list
every proxy in a chain (CDN, load balancer, Traefik) that adds an entry. If every entry
is a trusted proxy, the left-most is the client; a malformed entry ends the search at the
last trusted hop. `X-Forwarded-Proto: https` (its first value in a list) then counts as
HTTPS. IPv6 clients are still keyed by their /64. The proxy must append the peer it saw
to `X-Forwarded-For` (Caddy and Traefik do by default, nginx with the line in the example
above). Don't trust a range that untrusted clients can connect from (`0.0.0.0/0` makes
every forged header count; nudge logs a warning at start for `0.0.0.0/0` and `::/0`). IPv4-mapped prefixes (`::ffff:a.b.c.d/n`) need at least /96. The PROXY protocol is not supported.

## Sessions
Logins last 7 days (a cookie `nudge_session`; the database keeps only its SHA-256).
Expired sessions are deleted on use and every 10 minutes. The cookie is `Secure` when nudge
serves TLS or `NUDGE_PUBLIC_URL` is `https://`, else only when the browser uses HTTPS
through a trusted proxy that sets `X-Forwarded-Proto: https`. Writes from other origins are refused. Failed logins are
rate-limited per client IP and per username (see docs/api.md).

## Single sign-on
nudge signs users in through one OpenID Connect provider (authorization code flow with
PKCE, as a confidential client with `client_secret_basic`; ID tokens must be RS256). Set
`NUDGE_OIDC_ISSUER`, `NUDGE_OIDC_CLIENT_ID` and `NUDGE_OIDC_CLIENT_SECRET` together (nudge
refuses to start with only some of them) and an `https://` `NUDGE_PUBLIC_URL`. The
redirect URI to register is `<public URL>/api/v1/auth/oidc/callback`; the scopes are
`openid profile email`.

- **Roles.** The ID token's `roles` claim (an array) must hold `NUDGE_OIDC_ADMIN_ROLE`
  (the user is an admin) or `NUDGE_OIDC_USER_ROLE` (a user; with both, an admin). It is
  read on every login and sets the user's role in nudge, which an admin can't change
  there. Without either role the login is refused with a 403 page and audited, as is a
  user an admin disabled in nudge. The provider's role wins even when it demotes the last
  admin (logged as a warning): keep the break-glass `admin` for that case.
- **Users** are matched by issuer and subject (`sub`) only. A local user with the same
  name or email is never linked: the SSO user gets a name of its own (its preferred
  username, else its email, with `-2`… on a clash; never `admin`). Display name and email
  are refreshed on each login.
- **Setup** is off with SSO: `POST /api/v1/setup` is refused, and the first admin is
  whoever the provider gives the admin role. `NUDGE_ADMIN_PASSWORD` still creates a local
  `admin` on a fresh install: the break-glass user.
- **Password login** stays on by default. With `NUDGE_PASSWORD_LOGIN=false` the login page
  shows only the SSO button and the API refuses password logins except the local `admin`'s;
  `/login?password` shows the form for it.
- **Sessions** are nudge's own, capped by `NUDGE_OIDC_SESSION_TTL`: a user disabled at the
  provider keeps their session until then. Logout ends the nudge session and sends the
  browser to the provider's `end_session_endpoint`.
- **MFA.** `NUDGE_OIDC_REQUIRE_MFA=true` refuses ID tokens whose `amr` lacks `mfa`.
- Failed SSO logins land on the login page with a generic error, are logged with the
  detail and count against the per-IP login limit (shared with password login).
- **Revoking access.** Roles and MFA are checked at login only: a user whose admin role is
  taken away keeps their session (as an admin) until `NUDGE_OIDC_SESSION_TTL` ends it or
  they log in again, and API tokens they created keep working until then. The next SSO
  login without either role is refused and ends all of that user's sessions and deletes
  their API tokens (audited); a login refused only for a missing second factor revokes
  nothing. Deploy tokens and deploy webhooks they created are not touched. Disabling the
  user in nudge (Users page) ends their sessions and stops their tokens at once.
- **Break-glass.** SSO users count as users, so `NUDGE_ADMIN_PASSWORD` creates the local
  `admin` only on a fresh install: set it before the first SSO login if you want a way in
  without the provider (or after turning SSO off).

### Authestra
1. In Authestra, register a **confidential** client for nudge with the redirect URI
   `https://nudge.example.net/api/v1/auth/oidc/callback` and the scopes
   `openid profile email`. Note its client ID and secret.
2. Define the roles `admin` and `user` on that client, and grant `admin` to the people who
   manage nudge. Authestra refuses users without a role of the client at its login page;
   `user` signs in nowhere yet (nudge's roles are to come).
3. Start nudge with:
   ```sh
   NUDGE_PUBLIC_URL=https://nudge.example.net
   NUDGE_OIDC_ISSUER=https://auth.example.net
   NUDGE_OIDC_CLIENT_ID=nudge
   NUDGE_OIDC_CLIENT_SECRET=...        # from step 1
   NUDGE_OIDC_NAME=Authestra
   # NUDGE_OIDC_REQUIRE_MFA=true       # only users who passed a second factor
   # NUDGE_PASSWORD_LOGIN=false        # SSO only, plus the local admin
   ```

## Docker access
The server manages the Docker host whose socket is mounted at `/var/run/docker.sock`
(`-docker-host` / `DOCKER_HOST` overrides it, as `unix:///path` or `tcp://host:port`;
plain TCP has no TLS, so keep it on a trusted network). Socket access is root-equivalent
on that host; the container needs no `--privileged` and no extra capabilities.

At start nudge pings that host (3 s timeout). If it answers and no `local` environment
exists, nudge creates one named `local`. If it doesn't answer, nudge logs a warning and
starts anyway; restart it once the socket is available to get the environment. Each
Docker call made by an API request has a 10 s timeout.

## Hosting other users
Users (non-admins) share the host's kernel with everyone else. nudge already builds their
containers without privileges, host namespaces, binds or added capabilities, and checks
their allowance; an unprivileged container is hard to escape, but a kernel bug is root on
the host. The settings below make an escape and resource abuse much harder. They are
opt-in because each needs host setup nudge can't do itself, and they apply to users'
containers only, in every environment: each host (the server's and every agent's) needs
the same setup. Admins' containers are left as they are.

nudge refuses (409) to start or restart a user's container made with other settings than
the current ones, for example one made before a setting was added or changed: its
runtime, cgroup parent and disk cap (where the storage driver can set one) must match.
Recreating it (the edit form, or an update) builds it with the current settings.

### Registries users can pull from
A user's pull is anonymous (never with the stored registry credentials), but the image
reference names the registry, and the daemon connects to whatever host it names:
`10.0.0.5:5000/x` would probe the internal network from the Docker host. So users' pulls
(create, recreate, image update, deploy webhooks) may only reach the registries in
`NUDGE_USER_REGISTRIES`; any other is refused with `403` before the daemon is asked.
Admins' pulls aren't limited.

The default is Docker Hub (`docker.io`), `ghcr.io`, `quay.io`, `gcr.io`,
`registry.k8s.io`, `mcr.microsoft.com`, `public.ecr.aws`, `registry.gitlab.com` and
`lscr.io`. Setting the variable replaces the list, e.g.
`NUDGE_USER_REGISTRIES=docker.io,registry.example.net:5000`; a port is part of the host,
and Docker Hub's aliases (`index.docker.io`) count as `docker.io`. `*` allows any
registry: only for hosts with nothing to probe. The registry is read as Docker reads it:
the reference's first component when it has a dot or a port, is `localhost` or has an
upper-case letter, otherwise Docker Hub. An allowance's image patterns narrow a user's
images further.

### A cgroup slice per user
With `NUDGE_TENANT_CGROUP_PARENT=nudge-u{id}.slice` every container of user 7 goes into
`nudge-u7.slice`, so the kernel caps the user's total even if nudge's own allowance check
were wrong. The user's ID is on the Users page. With Docker's systemd cgroup driver (the
default on systemd hosts) the parent is a slice name ending in `.slice`; with the
cgroupfs driver it is a path such as `/nudge/u{id}`.

Create each user's slice before they start a container: systemd makes a missing slice
on demand, without limits. On the host, as root:

```ini
# /etc/systemd/system/nudge-u7.slice
[Unit]
Description=nudge user 7

[Slice]
MemoryMax=4G
MemorySwapMax=0
CPUQuota=200%
TasksMax=2048
```

then `systemctl daemon-reload && systemctl start nudge-u7.slice`. Keep the limits at or
above the user's allowance, so the allowance is what users normally meet and the slice is
the backstop.

### A sandboxed runtime
With `NUDGE_TENANT_RUNTIME=runsc` users' containers run under gVisor, which answers their
system calls in a user-space kernel instead of the host's: most kernel bugs are out of
reach. Install it and register it with the daemon (`runsc install`, which adds it to
`/etc/docker/daemon.json`, then restart Docker); `docker info` lists it under Runtimes.
Kata Containers (`kata-runtime`, a small VM per container) works the same way.

nudge refuses to create, start or restart a user's container (409) on a daemon that
doesn't list the runtime. The trade-offs: gVisor costs some speed on system-call-heavy work (I/O,
networking) and doesn't run everything (some `/proc` and `ioctl` uses); Kata needs
hardware virtualisation.

### Disk
`NUDGE_TENANT_STORAGE_SIZE=10G` caps the writable layer of every user's container (what it
writes outside its volumes; volumes and pulled images count toward the user's disk
allowance instead). Docker supports this on overlay2 over xfs mounted with `pquota`, and
on btrfs, zfs and devicemapper. nudge sets it where `docker info` reports such a driver
(for overlay2, an xfs backing filesystem) and logs a warning and leaves it out elsewhere.
On xfs without `pquota` the daemon refuses the create and the user sees why: mount the
filesystem under `/var/lib/docker` with `pquota` (in `/etc/fstab`; for the root
filesystem, `rootflags=pquota` on the kernel command line).

### The daemon
- **`userns-remap`** (`"userns-remap": "default"` in daemon.json) maps root in every
  container to an unprivileged range on the host, so an escape lands as nobody. It
  applies to every container on the daemon, admins' too, and existing images and volumes
  are re-owned or pulled again.
- **Rootless Docker** runs the whole daemon as an ordinary user: an escape gets that
  user, not root. nudge then talks to the rootless socket
  (`DOCKER_HOST=unix:///run/user/<uid>/docker.sock`); ports below 1024, some network
  drivers and cgroup limits need extra setup (cgroup v2 with delegation).
- **One VM per user** is the strongest: give each untrusted user their own small VM
  running `nudge-agent`, and grant them only that environment (Users, environment
  access). A kernel bug then reaches only their own VM. It costs a VM per user, and
  nudge doesn't provision them.
