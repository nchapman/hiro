Execute a shell command using bash.

Use this tool to run commands, install packages, build projects, run tests, or perform any system operation.

## Guidelines

- Prefer simple, single commands over complex pipelines when possible.
- Use absolute paths when referencing files outside the working directory.
- For long-running commands, keep the output reasonable — pipe through `head` or `tail` if needed.
- Commands time out after 2 minutes.
- Output is truncated at 32KB.
