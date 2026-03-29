-- Instances: durable agent identities (Definition -> Instance -> Session).
CREATE TABLE instances (
    id         TEXT PRIMARY KEY,
    agent_name TEXT    NOT NULL,
    mode       TEXT    NOT NULL CHECK (mode IN ('ephemeral', 'persistent', 'coordinator')),
    parent_id  TEXT    REFERENCES instances(id) ON DELETE CASCADE,
    status     TEXT    NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'stopped')),
    config     TEXT    NOT NULL DEFAULT '{}',
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    stopped_at TEXT
);
CREATE INDEX idx_instances_parent ON instances(parent_id);
CREATE INDEX idx_instances_agent  ON instances(agent_name);
CREATE INDEX idx_instances_status ON instances(status);

-- Link sessions to their parent instance.
ALTER TABLE sessions ADD COLUMN instance_id TEXT REFERENCES instances(id) ON DELETE CASCADE;
CREATE INDEX idx_sessions_instance ON sessions(instance_id);
