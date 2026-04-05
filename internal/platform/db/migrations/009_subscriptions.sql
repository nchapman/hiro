-- Subscriptions: event-driven triggers for agent instances.
-- V1 supports cron (time-based) triggers; the trigger column is extensible
-- to webhooks, file watches, etc.
CREATE TABLE subscriptions (
    id          TEXT PRIMARY KEY,
    instance_id TEXT    NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,
    trigger     TEXT    NOT NULL,
    message     TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    next_fire   TEXT,
    last_fired  TEXT,
    fire_count  INTEGER NOT NULL DEFAULT 0,
    error_count INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT,
    created_at  TEXT    NOT NULL DEFAULT (datetime('now')),

    UNIQUE(instance_id, name)
);
CREATE INDEX idx_subscriptions_instance  ON subscriptions(instance_id);
CREATE INDEX idx_subscriptions_next_fire ON subscriptions(next_fire) WHERE status = 'active';

-- Link triggered sessions to their subscription.
ALTER TABLE sessions ADD COLUMN subscription_id TEXT REFERENCES subscriptions(id) ON DELETE SET NULL;
