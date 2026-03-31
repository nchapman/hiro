-- Meta messages are visible to the model but hidden from the user's chat transcript.
-- Used for task completion notifications, system diagnostics, and internal signals.
ALTER TABLE messages ADD COLUMN meta INTEGER NOT NULL DEFAULT 0;
