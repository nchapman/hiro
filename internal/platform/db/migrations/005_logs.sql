-- Application logs: structured slog output stored for querying.
CREATE TABLE logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    level       TEXT    NOT NULL,     -- DEBUG, INFO, WARN, ERROR
    message     TEXT    NOT NULL,
    component   TEXT,                 -- e.g. api, inference, agent, cluster
    instance_id TEXT,                 -- nullable, for instance-scoped logs
    attrs       TEXT,                 -- JSON object of remaining structured key-value pairs
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX idx_logs_created_at   ON logs(created_at);
CREATE INDEX idx_logs_level_id     ON logs(level, id DESC);
CREATE INDEX idx_logs_component_id ON logs(component, id DESC);
