-- Single sign-on users: matched by the provider's issuer and subject (sub) only, never
-- by name or email. Local users keep both empty; SSO users have an empty password_hash,
-- so password login can't reach them. display_name and email are refreshed on each login.
ALTER TABLE users ADD COLUMN oidc_issuer TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN oidc_subject TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX users_oidc ON users (oidc_issuer, oidc_subject) WHERE oidc_subject <> '';
