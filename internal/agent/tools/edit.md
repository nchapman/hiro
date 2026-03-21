Edit files by replacing exact text matches. Also creates new files.

## Operations

- **Edit**: provide old_string + new_string to replace text
- **Create**: provide file_path + new_string with empty old_string to create a new file
- **Delete content**: provide old_string with empty new_string to remove text

## Critical: exact matching

The old_string must match the file content EXACTLY — every space, tab, newline, and indentation character. Include 3-5 lines of surrounding context to ensure a unique match.

Common failures:
- Wrong indentation (4 spaces vs 2 spaces)
- Missing blank lines
- Wrong comment spacing ("// comment" vs "//comment")

## Guidelines

- Always read the file first before editing.
- When replace_all is false (default), old_string must appear exactly once.
- If old_string appears multiple times, add more surrounding context or set replace_all to true.
- For complete file rewrites, use the write_file tool instead.
