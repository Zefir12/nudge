-- The events a webhook receives (internal/notify), comma-separated. Webhooks made
-- before recovery events keep the events they got so far; container.healthy and
-- agent.online are opt-in.
ALTER TABLE webhooks ADD COLUMN events TEXT NOT NULL DEFAULT 'update.available,container.unhealthy,container.exited,agent.offline';
