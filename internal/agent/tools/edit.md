Find and replace exact text in a file.

- `old_string` must be a character-perfect match against the file, including all whitespace and indentation
- If you're working from Read output, strip the line number prefix — only use the actual file content
- The edit fails when `old_string` appears more than once. Add surrounding context to disambiguate, or set `replace_all`
- Setting `old_string` to empty with content in `new_string` creates a new file (errors if the file already exists)
- An empty `new_string` deletes the matched text
- `replace_all` is useful for variable renames and pattern replacements

Best practices:
- Always read the file before editing so you can match its content exactly
- Keep `old_string` short — 2-4 lines that uniquely identify the location is usually enough
- Reach for Edit over Write when modifying existing files
- Don't use `sed` or `awk` through Bash for file edits