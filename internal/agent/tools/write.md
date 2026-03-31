Create or overwrite a file. Parent directories are created automatically.

- If the file exists, its contents are fully replaced
- You should read existing files before overwriting to avoid losing content — this tool may reject writes to unread files

Best practices:
- For targeted changes to existing files, use Edit instead — it's more precise and only transmits the diff
- Reserve Write for creating new files or full rewrites
- Avoid writing files through Bash (`echo >`, heredocs)
- Don't create documentation or README files unless the user asks for them
