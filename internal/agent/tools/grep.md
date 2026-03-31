Search file contents using regex patterns, powered by ripgrep.

- Supports full regex syntax (e.g., `log.*Error`, `function\s+\w+`)
- Filter files with the `glob` parameter (e.g., `*.js`, `**/*.tsx`)
- Set `literal_text` to true for exact string matching (auto-escapes regex)
- Max 100 matches, 30s timeout
- Skips hidden files/directories, node_modules, vendor, dist, .git