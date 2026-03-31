Run a shell command and return stdout/stderr.

- Commands that run longer than 60s are automatically moved to background. Use `timeout` to set a different threshold — shorter or longer (max 600000ms)
- Output is capped at 32KB
- Set `run_in_background` for intentionally long-running processes, then manage with `TaskOutput` and `TaskStop`
- `description` labels what the command does — purely informational, no effect on execution
- Quote paths that contain spaces
- Chain dependent commands with `&&`. Avoid using newlines as command separators

Best practices:
- Prefer the purpose-built tools over Bash equivalents:
  - Glob over `find`/`ls` for file discovery
  - Grep over `grep`/`rg` for content search
  - Read over `cat`/`head`/`tail` for reading files
  - Edit over `sed`/`awk` for file modifications
  - Write over `echo >`/heredocs for creating files
- For git: don't force-push to main, create new commits rather than amending, and don't skip hooks unless explicitly told to
- Don't `sleep` to wait for background work — use `TaskOutput` with `block` instead
