# Purpose
Execute shell commands to perform system operations, manage git, or run scripts.

## Usage & Constraints
- **Backgrounding:** Commands exceeding 60s automatically move to the background. Adjust with `timeout` (max 600,000ms).
- **Control:** Use `run_in_background` for long-running processes; manage via `TaskOutput` and `TaskStop`.
- **Output:** Capped at 32KB.
- **Formatting:** Quote paths with spaces. Chain dependent commands with `&&`.

## Best Practices
- **Prefer specialized tools:** Use Glob, Grep, Read, Edit, and Write over Bash equivalents (`find`, `ls`, `sed`, `cat`, etc.).
- **Git:** Create new commits rather than amending; don't force-push to main; respect hooks unless told otherwise.
- **Efficiency:** Use `TaskOutput` with `block` instead of `sleep` to wait for background work.
