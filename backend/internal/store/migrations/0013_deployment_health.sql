-- Deployment health checks: a deployment's settings (JSON, deployments.Health), the
-- step a run is at (pulling, deploying, checking, rolling_back) and the warnings it gave
-- (JSON array of safe messages, such as a check an old agent skipped).
ALTER TABLE deployments ADD COLUMN health TEXT NOT NULL DEFAULT '{}';
ALTER TABLE deployment_runs ADD COLUMN step TEXT NOT NULL DEFAULT '';
ALTER TABLE deployment_runs ADD COLUMN warnings TEXT NOT NULL DEFAULT '[]';
