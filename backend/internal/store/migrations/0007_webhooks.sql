-- Webhook targets of notifications (internal/notify). The URL can carry a secret (chat
-- webhooks put their token in the path), so it is sealed like the header value with
-- AES-256-GCM under the key in <data>/secret.key; endpoint is its scheme and host, shown
-- in the UI. header_name is '' and header_value_enc NULL when no secret header is sent.
-- last_sent_at (Unix seconds) and last_error describe the last delivery.
CREATE TABLE webhooks (
	id               INTEGER PRIMARY KEY,
	name             TEXT    NOT NULL UNIQUE,
	endpoint         TEXT    NOT NULL,
	url_enc          BLOB    NOT NULL,
	header_name      TEXT    NOT NULL,
	header_value_enc BLOB,
	enabled          INTEGER NOT NULL,
	last_sent_at     INTEGER,
	last_error       TEXT    NOT NULL DEFAULT '',
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL
) STRICT;

-- Notifications already sent, so one problem doesn't repeat: a key per problem, sent
-- again only after expires_at (Unix milliseconds); expired rows are pruned.
CREATE TABLE notify_sent (
	key        TEXT    PRIMARY KEY,
	expires_at INTEGER NOT NULL
) STRICT;
