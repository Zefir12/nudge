-- A user's owner key is the value of the owner label on the containers, volumes and
-- networks they own. It is random rather than the user's id, which SQLite hands out again
-- when the newest user is deleted: a new user would inherit their objects.
ALTER TABLE users ADD COLUMN owner_key TEXT;
UPDATE users SET owner_key = lower(hex(randomblob(16)));
CREATE UNIQUE INDEX users_owner_key ON users (owner_key);
CREATE TRIGGER users_owner_key AFTER INSERT ON users WHEN NEW.owner_key IS NULL
BEGIN
	UPDATE users SET owner_key = lower(hex(randomblob(16))) WHERE id = NEW.id;
END;

-- The environments a user may use, set by an admin; admins use every environment.
CREATE TABLE user_environments (
	user_id        INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	environment_id INTEGER NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
	PRIMARY KEY (user_id, environment_id)
) STRICT, WITHOUT ROWID;
