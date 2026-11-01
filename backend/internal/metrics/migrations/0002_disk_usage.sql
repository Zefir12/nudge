-- The last `docker system df` of each environment, one row per kind (volumes, images,
-- build_cache, containers); replaced by every measurement. Sizes in bytes, measured_at
-- in unix milliseconds.
CREATE TABLE disk_usage (
    environment_id INTEGER NOT NULL,
    kind           TEXT    NOT NULL,
    size           INTEGER NOT NULL,
    reclaimable    INTEGER NOT NULL,
    total_count    INTEGER NOT NULL,
    active_count   INTEGER NOT NULL,
    measured_at    INTEGER NOT NULL,
    PRIMARY KEY (environment_id, kind)
) STRICT, WITHOUT ROWID;
