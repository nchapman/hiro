Retrieves output from a background job started by Bash.

- Response starts with `Status: running` or `Status: completed` followed by the output
- Set `wait` to true to block until the job completes
- Use `TaskStop` to terminate a background job