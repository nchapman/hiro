-- Per-session configuration (model override, reasoning effort, future settings).
-- Stored as a JSON object for extensibility without schema changes.
ALTER TABLE sessions ADD COLUMN config TEXT NOT NULL DEFAULT '{}';
