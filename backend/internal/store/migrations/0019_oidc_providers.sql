-- Single sign-on providers an admin manages in the UI (the NUDGE_OIDC_* one stays in the
-- environment). client_secret is sealed with secretbox, bound to the id and issuer. One
-- provider per issuer: users are matched by issuer and subject.
CREATE TABLE oidc_providers (
	id                  TEXT PRIMARY KEY,
	name                TEXT NOT NULL UNIQUE COLLATE NOCASE,
	issuer              TEXT NOT NULL UNIQUE,
	client_id           TEXT NOT NULL,
	client_secret       BLOB NOT NULL,
	admin_role          TEXT NOT NULL,
	user_role           TEXT NOT NULL,
	require_mfa         INTEGER NOT NULL,
	session_ttl_seconds INTEGER NOT NULL,
	enabled             INTEGER NOT NULL,
	created_at          INTEGER NOT NULL,
	updated_at          INTEGER NOT NULL
);
