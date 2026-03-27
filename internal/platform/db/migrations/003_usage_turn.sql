-- Add turn number to usage_events so per-step events can be grouped into turns.
-- Each turn (one Chat() call) may produce multiple LLM API calls (steps).
ALTER TABLE usage_events ADD COLUMN turn INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_usage_session_turn ON usage_events(session_id, turn);
