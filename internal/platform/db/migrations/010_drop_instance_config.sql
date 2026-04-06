-- Remove the orphaned config column from instances.
-- Per-instance config (model override, reasoning effort, channel bindings)
-- now lives in instances/<uuid>/config.yaml on the filesystem.
ALTER TABLE instances DROP COLUMN config;
