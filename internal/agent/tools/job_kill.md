Terminate a background job.

## Usage

- Provide the `job_id` returned from a background bash execution.
- The job and all its child processes are terminated immediately (SIGKILL to the process group).
- After termination, the job ID is removed — subsequent job_output calls will return an error.
- If the process does not exit within 5 seconds, an error is returned, but the job ID is still removed.
