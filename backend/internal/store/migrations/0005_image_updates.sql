-- The last image update check of each environment (internal/updates): one row per image
-- reference and image ID its containers ran. checked_at is Unix milliseconds.
CREATE TABLE image_updates (
	environment_id INTEGER NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
	image          TEXT    NOT NULL,
	image_id       TEXT    NOT NULL,
	status         TEXT    NOT NULL CHECK (status IN ('available', 'current', 'unknown')),
	local_digest   TEXT    NOT NULL,
	remote_digest  TEXT    NOT NULL,
	error          TEXT    NOT NULL,
	checked_at     INTEGER NOT NULL,
	PRIMARY KEY (environment_id, image, image_id)
) STRICT;
