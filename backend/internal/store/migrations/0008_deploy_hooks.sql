-- Deploy webhooks (internal/deployhooks): a URL a CI job or a Forgejo webhook calls to
-- make one container (by name, which survives a recreate) or stack pull its image and
-- redeploy. The secret is sealed with AES-256-GCM under the key in <data>/secret.key, as
-- HMAC signature checks need its raw value. tag_filter is '' for none. Times are Unix
-- milliseconds. A user's webhooks go with the user.
CREATE TABLE deploy_hooks (
	id                TEXT    PRIMARY KEY,
	name              TEXT    NOT NULL,
	environment_id    INTEGER NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
	kind              TEXT    NOT NULL CHECK (kind IN ('container', 'stack')),
	target            TEXT    NOT NULL,
	secret_enc        BLOB    NOT NULL,
	tag_filter        TEXT    NOT NULL,
	created_by        INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	created_at        INTEGER NOT NULL,
	last_triggered_at INTEGER,
	UNIQUE (created_by, name)
) STRICT;

CREATE INDEX deploy_hooks_target ON deploy_hooks (environment_id, kind, target);

-- One row per trigger that ran (or was skipped by the tag filter); triggers merged into
-- a queued run have none. error is a safe message, never the daemon's. Bounded per
-- webhook and in total.
CREATE TABLE deploy_hook_runs (
	id           INTEGER PRIMARY KEY,
	hook_id      TEXT    NOT NULL REFERENCES deploy_hooks (id) ON DELETE CASCADE,
	trigger_kind TEXT    NOT NULL CHECK (trigger_kind IN ('bearer', 'signature')),
	status       TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'updated', 'unchanged', 'skipped', 'failed')),
	queued_at    INTEGER NOT NULL,
	started_at   INTEGER,
	finished_at  INTEGER,
	image_before TEXT    NOT NULL DEFAULT '',
	image_after  TEXT    NOT NULL DEFAULT '',
	error        TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX deploy_hook_runs_hook ON deploy_hook_runs (hook_id, id);
