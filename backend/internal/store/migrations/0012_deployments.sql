-- Deployments (internal/deployments): a named stack CI deploys through, with its own
-- members and tokens. variables is a JSON array of the .env variable names a deploy may
-- set. Times are Unix milliseconds. A deployment goes with its environment.
CREATE TABLE deployments (
	id             INTEGER PRIMARY KEY,
	name           TEXT    NOT NULL UNIQUE,
	environment_id INTEGER NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
	stack          TEXT    NOT NULL,
	description    TEXT    NOT NULL DEFAULT '',
	variables      TEXT    NOT NULL DEFAULT '[]',
	created_at     INTEGER NOT NULL,
	updated_at     INTEGER NOT NULL
) STRICT;

-- Who may do what with a deployment; admins may do everything without a row.
CREATE TABLE deployment_members (
	deployment_id INTEGER NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
	user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	role          TEXT    NOT NULL CHECK (role IN ('viewer', 'deployer', 'maintainer', 'owner')),
	PRIMARY KEY (deployment_id, user_id)
) STRICT;

-- Deploy tokens: work only on their deployment's deploy routes, for their scopes (a
-- space-separated list). Only the SHA-256 of the token is stored.
CREATE TABLE deployment_tokens (
	id            INTEGER PRIMARY KEY,
	deployment_id INTEGER NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
	name          TEXT    NOT NULL,
	prefix        TEXT    NOT NULL,
	token_hash    TEXT    NOT NULL UNIQUE,
	scopes        TEXT    NOT NULL,
	created_by    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	created_at    INTEGER NOT NULL,
	expires_at    INTEGER,
	last_used_at  INTEGER,
	UNIQUE (deployment_id, name)
) STRICT;

-- One row per deploy request. actor_kind/actor_name say who asked (a user or a token,
-- by name). variables are the values the run set and previous the ones they replaced
-- (JSON objects). error is a safe message, never the daemon's. Bounded per deployment.
CREATE TABLE deployment_runs (
	id            INTEGER PRIMARY KEY,
	deployment_id INTEGER NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
	actor_kind    TEXT    NOT NULL CHECK (actor_kind IN ('user', 'token')),
	actor_name    TEXT    NOT NULL,
	status        TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'superseded', 'rolled_back')),
	variables     TEXT    NOT NULL DEFAULT '{}',
	previous      TEXT    NOT NULL DEFAULT '{}',
	queued_at     INTEGER NOT NULL,
	started_at    INTEGER,
	finished_at   INTEGER,
	image_before  TEXT    NOT NULL DEFAULT '',
	image_after   TEXT    NOT NULL DEFAULT '',
	error         TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX deployment_runs_deployment ON deployment_runs (deployment_id, id);
