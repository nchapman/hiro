# Purpose
Find files and directories by pattern, sorted by modification time (newest first).

## Usage & Constraints
- **Syntax:** Supports standard glob patterns like `**/*.js`, `src/**/*.ts`, `*.{go,mod}`.
- **Output:** Returns relative paths. Capped at 100 results.
- **Exclusions:** Automatically skips node_modules, vendor, dist, __pycache__, .git, and hidden files/dirs.

## Best Practices
- **Discovery:** Use Glob for all file discovery — avoid `find` or `ls` via Bash.
- **Content:** If you need to search *inside* files, use Grep instead.
