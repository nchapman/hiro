Execute a shell command using bash.

Use this tool to run commands, install packages, build projects, run tests, or perform any system operation.

## Guidelines

- Prefer simple, single commands over complex pipelines when possible.
- Use absolute paths when referencing files outside the working directory.
- For long-running commands, keep the output reasonable — pipe through `head` or `tail` if needed.
- Synchronous commands time out after 2 minutes.
- Output is truncated at 32KB.

## Background execution

- Set `run_in_background` to true to run a command in a background shell.
- Returns a job ID for managing the background process.
- Use the `job_output` tool to view current output from a background job.
- Use the `job_kill` tool to terminate a background job.
- NEVER use `&` at the end of commands — use `run_in_background` instead.
- Commands running longer than 60 seconds are automatically moved to background.

Good candidates for background:
- Long-running servers (`npm start`, `python -m http.server`, `node server.js`)
- Watch/monitoring tasks (`npm run watch`, `tail -f logfile`)
- Continuous processes that don't exit on their own

Not suitable for background:
- Build commands (`npm run build`, `go build`)
- Test suites (`npm test`, `pytest`)
- Git operations, file operations, short-lived scripts
