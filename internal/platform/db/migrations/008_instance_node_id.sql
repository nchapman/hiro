-- Add node_id to track which cluster node an instance runs on.
-- This is non-derivable state: only the creator knows the target node.
ALTER TABLE instances ADD COLUMN node_id TEXT NOT NULL DEFAULT 'home';
