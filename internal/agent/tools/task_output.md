# Purpose
Retrieve stdout/stderr and status information from a running or completed background task.

## Usage & Constraints
- **ID:** Requires a `task_id` identifying the specific task.
- **Wait:** Set `block: true` (default) to wait for task completion.
- **Status:** Use `block: false` for a non-blocking check of current status.
- **Control:** Use `TaskStop` to terminate the task if needed.
