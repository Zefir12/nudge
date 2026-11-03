-- Stacks whose compose file comes from a git repository. commit_hash is the commit of the
-- files in the stack's directory; seen_commit the newest one a pull got, deployed or not,
-- so polling doesn't retry a commit whose deploy failed.
CREATE TABLE stack_sources (
	environment_id INTEGER NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
	stack          TEXT NOT NULL,
	repo_url       TEXT NOT NULL,
	ref            TEXT NOT NULL,
	compose_path   TEXT NOT NULL,
	env_path       TEXT NOT NULL,
	auth_kind      TEXT NOT NULL CHECK (auth_kind IN ('none', 'token', 'ssh')),
	username       TEXT NOT NULL,
	secret_enc     BLOB,
	known_hosts    TEXT NOT NULL,
	poll_seconds   INTEGER NOT NULL CHECK (poll_seconds >= 0),
	commit_hash    TEXT NOT NULL,
	seen_commit    TEXT NOT NULL,
	last_error     TEXT NOT NULL,
	checked_at     INTEGER NOT NULL,
	pulled_at      INTEGER NOT NULL,
	PRIMARY KEY (environment_id, stack)
) STRICT, WITHOUT ROWID;
