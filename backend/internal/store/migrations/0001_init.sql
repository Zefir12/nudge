-- Initial schema. Times are Unix seconds (UTC). Secrets are stored only as hashes.

CREATE TABLE users (
	id            INTEGER PRIMARY KEY,
	username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT    NOT NULL,
	created_at    INTEGER NOT NULL,
	updated_at    INTEGER NOT NULL
) STRICT;

-- id_hash is the SHA-256 of the cookie value; the value itself is never stored.
CREATE TABLE sessions (
	id_hash      TEXT    PRIMARY KEY,
	user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	created_at   INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL,
	expires_at   INTEGER NOT NULL
) STRICT;
CREATE INDEX sessions_user_id ON sessions (user_id);
CREATE INDEX sessions_expires_at ON sessions (expires_at);

-- token_hash is the SHA-256 of the token; prefix is its first characters, shown in lists.
CREATE TABLE api_tokens (
	id           INTEGER PRIMARY KEY,
	user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	name         TEXT    NOT NULL,
	prefix       TEXT    NOT NULL,
	token_hash   TEXT    NOT NULL UNIQUE,
	created_at   INTEGER NOT NULL,
	last_used_at INTEGER,
	expires_at   INTEGER,
	UNIQUE (user_id, name)
) STRICT;

CREATE TABLE environments (
	id           INTEGER PRIMARY KEY,
	name         TEXT    NOT NULL UNIQUE,
	kind         TEXT    NOT NULL CHECK (kind IN ('local', 'agent')),
	created_at   INTEGER NOT NULL,
	last_seen_at INTEGER
) STRICT;
