Regex search across file contents, backed by ripgrep.

- Full regex support (e.g., `log\.Error`, `func\s+\w+`)
- Narrow results with `glob` (e.g., `*.js`) or `type` (e.g., `go`, `py`) filters
- Three output modes: `files_with_matches` (default — paths only), `content` (matching lines with optional context), `count` (per-file match tallies)
- Results are paginated: `head_limit` defaults to 250 entries, `offset` skips the first N. Set `head_limit: 0` for no cap
- Ripgrep syntax — escape literal braces with backslashes (e.g., `interface\{\}` to find `interface{}`)
- For patterns that cross line boundaries, enable `multiline`
- Use `literal_text` to disable regex and match the exact string

Best practices:
- Use Grep for all content searching — don't run `grep` or `rg` through Bash
- Prefer `type` over `glob` for standard language filters — it's faster