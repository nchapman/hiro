Executes a shell command and returns its output.

- Avoid using this tool to run commands when a dedicated tool is available (use Read instead of cat, Edit instead of sed, Glob instead of find, Grep instead of grep)
- Commands timeout after 120 seconds by default. Use `run_in_background` for long-running processes
- Synchronous commands that exceed 60s are automatically moved to background
- Output is truncated at 32KB
- Use `BashOutput` to retrieve output from background jobs, and `KillShell` to terminate them