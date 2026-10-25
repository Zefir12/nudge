-- How nudge browses a registry over HTTP itself (internal/registryclient): kind generic
-- speaks the Docker Registry v2 API only; forgejo also uses Forgejo's packages API to
-- list and delete. owners are the comma-separated Forgejo users/orgs whose packages are
-- listed (empty: the registry's username). plain_http allows a registry without TLS.
ALTER TABLE registries ADD COLUMN kind TEXT NOT NULL DEFAULT 'generic' CHECK (kind IN ('generic', 'forgejo'));
ALTER TABLE registries ADD COLUMN owners TEXT NOT NULL DEFAULT '';
ALTER TABLE registries ADD COLUMN plain_http INTEGER NOT NULL DEFAULT 0 CHECK (plain_http IN (0, 1));
