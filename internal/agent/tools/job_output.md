Retrieve the current output from a background job.

## Parameters

- `job_id`: The ID returned from a background bash execution.
- `wait`: If true, block until the job finishes before returning output.

## Output format

The response begins with `Status: running` or `Status: completed`, followed by any stdout/stderr output collected so far. For completed jobs with a non-zero exit, the exit code is appended. Output is truncated at 32KB using the same head/tail strategy as bash.

## Tips

- Use this to monitor long-running processes.
- Can be called multiple times to view incremental output.
