-- Registry credentials used for image pulls. password_enc is the password sealed with
-- AES-256-GCM under the key in <data>/secret.key (see internal/secretbox); address is the
-- registry host as images name it (docker.io for Docker Hub).
CREATE TABLE registries (
	id           INTEGER PRIMARY KEY,
	name         TEXT    NOT NULL UNIQUE,
	address      TEXT    NOT NULL UNIQUE,
	username     TEXT    NOT NULL,
	password_enc BLOB    NOT NULL,
	created_at   INTEGER NOT NULL,
	updated_at   INTEGER NOT NULL
) STRICT;
