-- The whole host's load next to the containers' totals: CPU as a percentage of every
-- CPU (0-100), memory in use (all but MemAvailable) and the host's memory, in bytes.
-- NULL where the host couldn't be read (an older agent, the first tick after a start).
ALTER TABLE environment_samples ADD COLUMN host_cpu REAL;
ALTER TABLE environment_samples ADD COLUMN host_memory INTEGER;
ALTER TABLE environment_samples ADD COLUMN host_memory_total INTEGER;

-- Hourly: the averages of the samples that had them, the largest memory total.
ALTER TABLE environment_hours ADD COLUMN host_cpu REAL;
ALTER TABLE environment_hours ADD COLUMN host_memory REAL;
ALTER TABLE environment_hours ADD COLUMN host_memory_total INTEGER;
-- How many of the hour's samples had the host's load, which weighs its averages.
ALTER TABLE environment_hours ADD COLUMN host_samples INTEGER NOT NULL DEFAULT 0;
