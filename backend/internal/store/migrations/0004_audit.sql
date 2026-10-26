-- Every write through the API (see internal/audit). Users and environments are copied by
-- name, not referenced, so entries outlive what they describe. token is the name of the
-- API token the request used, empty for a session; status is the HTTP status answered.
CREATE TABLE audit_log (
	id          INTEGER PRIMARY KEY,
	time        INTEGER NOT NULL,
	username    TEXT    NOT NULL,
	token       TEXT    NOT NULL,
	environment TEXT    NOT NULL,
	action      TEXT    NOT NULL,
	target      TEXT    NOT NULL,
	status      INTEGER NOT NULL
) STRICT;

CREATE INDEX audit_log_time ON audit_log (time);
