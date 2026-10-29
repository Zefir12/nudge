-- metrics.db: usage samples, apart from nudge.db (not in backups, rewritten every tick).
-- Times are unix seconds. Network and block I/O are counters since each container
-- started, as the daemon reports them. A row's owner is the container's owner key
-- (auth's, the owner label's value), empty for a container no user owns.

-- One row per environment per tick: the totals of every running container.
CREATE TABLE environment_samples (
    environment_id INTEGER NOT NULL,
    time           INTEGER NOT NULL,
    cpu            REAL    NOT NULL,
    memory         INTEGER NOT NULL,
    network_rx     INTEGER NOT NULL,
    network_tx     INTEGER NOT NULL,
    block_read     INTEGER NOT NULL,
    block_write    INTEGER NOT NULL,
    containers     INTEGER NOT NULL,
    PRIMARY KEY (environment_id, time)
) STRICT, WITHOUT ROWID;

-- One row per running container per tick.
CREATE TABLE container_samples (
    environment_id INTEGER NOT NULL,
    container_id   TEXT    NOT NULL,
    time           INTEGER NOT NULL,
    name           TEXT    NOT NULL,
    owner          TEXT    NOT NULL DEFAULT '',
    cpu            REAL    NOT NULL,
    memory         INTEGER NOT NULL,
    network_rx     INTEGER NOT NULL,
    network_tx     INTEGER NOT NULL,
    block_read     INTEGER NOT NULL,
    block_write    INTEGER NOT NULL,
    PRIMARY KEY (environment_id, container_id, time)
) STRICT, WITHOUT ROWID;
CREATE INDEX container_samples_time ON container_samples (environment_id, time);
CREATE INDEX container_samples_owner ON container_samples (environment_id, owner, time) WHERE owner <> '';

-- Hourly rollups of the samples (hour is the start of the hour): the averages, the
-- maximums, the counters' last (largest) values and how many samples went in.
CREATE TABLE environment_hours (
    environment_id INTEGER NOT NULL,
    hour           INTEGER NOT NULL,
    cpu_avg        REAL    NOT NULL,
    cpu_max        REAL    NOT NULL,
    memory_avg     REAL    NOT NULL,
    memory_max     INTEGER NOT NULL,
    network_rx     INTEGER NOT NULL,
    network_tx     INTEGER NOT NULL,
    block_read     INTEGER NOT NULL,
    block_write    INTEGER NOT NULL,
    containers     INTEGER NOT NULL,
    samples        INTEGER NOT NULL,
    PRIMARY KEY (environment_id, hour)
) STRICT, WITHOUT ROWID;

CREATE TABLE container_hours (
    environment_id INTEGER NOT NULL,
    container_id   TEXT    NOT NULL,
    hour           INTEGER NOT NULL,
    name           TEXT    NOT NULL,
    owner          TEXT    NOT NULL DEFAULT '',
    cpu_avg        REAL    NOT NULL,
    cpu_max        REAL    NOT NULL,
    memory_avg     REAL    NOT NULL,
    memory_max     INTEGER NOT NULL,
    network_rx     INTEGER NOT NULL,
    network_tx     INTEGER NOT NULL,
    block_read     INTEGER NOT NULL,
    block_write    INTEGER NOT NULL,
    samples        INTEGER NOT NULL,
    PRIMARY KEY (environment_id, container_id, hour)
) STRICT, WITHOUT ROWID;
CREATE INDEX container_hours_owner ON container_hours (environment_id, owner, hour) WHERE owner <> '';
