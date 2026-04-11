# Purpose
Read file contents with line numbers for inspection and preparation for edits.

## Usage & Constraints
- **Location:** Accepts absolute or relative paths.
- **Filtering:** Use `offset` and `limit` for large files.
- **Output:** Returns content with 1-indexed line numbers. Capped at 64KB.
- **Errors:** Returns an error if the file doesn't exist. Cannot read directories — use Glob to list files instead.

## Best Practices
- **Standard Tool:** Use Read for all file reading — avoid `cat`, `head`, or `tail` via Bash.
- **Trust:** Trust provided paths and read them directly.
