-- Add channel identification to sessions for per-channel session routing.
-- channel_type: "web", "telegram", "slack", "agent", "trigger"
-- channel_id: qualifier within type (e.g. chat ID, parent instance ID)
ALTER TABLE sessions ADD COLUMN channel_type TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN channel_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_sessions_instance_channel ON sessions(instance_id, channel_type, channel_id);
