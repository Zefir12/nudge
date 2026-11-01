-- Container events per hour (hour is the start of the hour, unix seconds), by the
-- container's owner key (empty for a container no user owns): an environment's activity
-- is the sum over its owners, a user's the rows of their key.
CREATE TABLE activity_hours (
    environment_id INTEGER NOT NULL,
    hour           INTEGER NOT NULL,
    owner          TEXT    NOT NULL DEFAULT '',
    count          INTEGER NOT NULL,
    PRIMARY KEY (environment_id, hour, owner)
) STRICT, WITHOUT ROWID;
