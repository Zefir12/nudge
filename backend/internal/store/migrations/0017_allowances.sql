-- What a user's containers may take in total in one environment, set by an admin. Without
-- a row the user may create or start nothing there. images is a JSON list of image
-- patterns; empty allows any image.
CREATE TABLE allowances (
	user_id        INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	environment_id INTEGER NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
	cpus_milli     INTEGER NOT NULL CHECK (cpus_milli >= 0),
	memory_bytes   INTEGER NOT NULL CHECK (memory_bytes >= 0),
	disk_bytes     INTEGER NOT NULL CHECK (disk_bytes >= 0),
	max_containers INTEGER NOT NULL CHECK (max_containers >= 0),
	port_min       INTEGER NOT NULL CHECK (port_min BETWEEN 0 AND 65535),
	port_max       INTEGER NOT NULL CHECK (port_max BETWEEN port_min AND 65535),
	images         TEXT NOT NULL DEFAULT '[]',
	updated_at     INTEGER NOT NULL,
	PRIMARY KEY (user_id, environment_id)
) STRICT, WITHOUT ROWID;

-- The images a user's pulls brought onto an environment's (shared) image store: they count
-- toward the user's disk allowance while they exist.
CREATE TABLE image_pulls (
	user_id        INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	environment_id INTEGER NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
	image_id       TEXT NOT NULL,
	pulled_at      INTEGER NOT NULL,
	PRIMARY KEY (user_id, environment_id, image_id)
) STRICT, WITHOUT ROWID;
