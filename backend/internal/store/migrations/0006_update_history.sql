-- Updates made through nudge (internal/updates history.go): the image a container or a
-- stack's service ran before and after, so an update can be rolled back by hand. target
-- is the container or stack name (names survive the recreate, IDs don't); service is
-- the compose service, empty for a container. time is Unix milliseconds.
CREATE TABLE update_history (
	id              INTEGER PRIMARY KEY,
	environment_id  INTEGER NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
	kind            TEXT    NOT NULL CHECK (kind IN ('container', 'stack')),
	target          TEXT    NOT NULL,
	service         TEXT    NOT NULL,
	image           TEXT    NOT NULL,
	old_image_id    TEXT    NOT NULL,
	old_digest      TEXT    NOT NULL,
	new_image_id    TEXT    NOT NULL,
	new_digest      TEXT    NOT NULL,
	username        TEXT    NOT NULL,
	time            INTEGER NOT NULL
) STRICT;

CREATE INDEX update_history_target ON update_history (environment_id, kind, target, id);
