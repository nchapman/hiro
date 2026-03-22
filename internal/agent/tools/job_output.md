Retrieve the current output from a background job.

## Usage

- Provide the job ID returned from a background bash execution.
- Returns the current stdout/stderr output and whether the job has completed.
- Set `wait` to true to block until the job finishes before returning output.

## Tips

- Use this to monitor long-running processes.
- Can be called multiple times to view incremental output.
- Check the completion status to see if the process has finished.
