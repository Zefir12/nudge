-- Agent environments. agent_id and agent_secret_hash (SHA-256, hex) are set when an agent
-- enrolls with its join token and cleared when it is revoked.
ALTER TABLE environments ADD COLUMN agent_id TEXT;
ALTER TABLE environments ADD COLUMN agent_secret_hash TEXT;
ALTER TABLE environments ADD COLUMN agent_version TEXT;
CREATE UNIQUE INDEX environments_agent_id ON environments (agent_id);

-- One pending join token per agent environment; token_hash is the token's SHA-256 (hex).
CREATE TABLE join_tokens (
	environment_id INTEGER PRIMARY KEY REFERENCES environments (id) ON DELETE CASCADE,
	token_hash     TEXT    NOT NULL UNIQUE,
	expires_at     INTEGER NOT NULL
) STRICT;
