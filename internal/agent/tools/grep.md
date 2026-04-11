# Purpose
Search file contents across multiple files using regular expressions.

## Usage & Constraints
- **Search Type:** Supports full regex and `literal_text` matching.
- **Filters:** Use `glob` patterns (e.g., `*.js`) or `type` (e.g., `go`, `py`) for faster results.
- **Modes:** `files_with_matches` (default), `content` (lines with context), and `count`.
- **Formatting:** Paginated results (250 entries, `offset` to skip). Set `head_limit: 0` for no cap. Use `multiline` for cross-line regex. Escape regex-special chars (e.g., `interface\{\}` for `interface{}`), or use `literal_text: true` for exact string matching.

## Best Practices
- **Internal Search:** Use Grep for all content searching — avoid `grep` or `rg` via Bash.
- **Efficiency:** Use `type` over `glob` for language filtering whenever possible.
