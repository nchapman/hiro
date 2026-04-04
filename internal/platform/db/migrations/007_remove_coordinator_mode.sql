-- Collapse operator mode into persistent. The operator agent now
-- declares its management tools via allowed_tools and Unix groups via
-- the "groups" frontmatter field instead of relying on a dedicated mode.
UPDATE instances SET mode = 'persistent' WHERE mode = 'operator';
UPDATE sessions SET mode = 'persistent' WHERE mode = 'operator';
