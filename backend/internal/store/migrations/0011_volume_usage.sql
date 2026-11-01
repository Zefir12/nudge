-- The last disk usage measurement of each environment's volumes (internal/usage): one
-- row per volume. size is NULL when the driver can't report it; measured_at is Unix
-- milliseconds.
CREATE TABLE volume_usage (
	environment_id INTEGER NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
	name           TEXT    NOT NULL,
	size           INTEGER,
	ref_count      INTEGER NOT NULL,
	measured_at    INTEGER NOT NULL,
	PRIMARY KEY (environment_id, name)
) STRICT;
