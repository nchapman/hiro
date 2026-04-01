Find files by pattern. Results are sorted by modification time, newest first.

- Accepts standard glob syntax: `**/*.js`, `src/**/*.ts`, `*.{go,mod}`
- Returns paths relative to the search directory
- Caps at 100 results — narrow the path or pattern if you hit the limit
- Skips hidden files/dirs, node_modules, vendor, dist, and .git

Best practices:
- Use Glob for locating files by name or extension — don't shell out to `find` or `ls`
- When you need to search inside files rather than find them, use Grep
