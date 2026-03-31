Read a file and return its contents with line numbers.

- Accepts absolute or relative paths
- Returns the entire file by default. For large files, use `offset` and `limit` to target the section you need
- Line numbers are 1-indexed in the output
- Output is capped at 64KB
- Reading a nonexistent file returns an error, not a crash — it's safe to try
- Directories can't be read — use Bash with `ls` instead

Best practices:
- Use Read for all file reading — avoid `cat`, `head`, or `tail` through Bash
- When a user gives you a path, trust it and read directly