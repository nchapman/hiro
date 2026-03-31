Retrieves output from a running or completed background task.

- Takes a `task_id` parameter identifying the task
- Returns the task output along with status information
- Use `block` (default true) to wait for task completion
- Use `block: false` for non-blocking check of current status
- Use `TaskStop` to terminate a background task
