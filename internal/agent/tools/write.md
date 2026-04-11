# Purpose
Create new files or perform full overwrites of existing files.

## Usage & Constraints
- **Overwrite:** Fully replaces contents of existing files.
- **Directories:** Parent directories are automatically created.
- **Safety:** Read existing files before overwriting to avoid data loss.

## Best Practices
- **Edit vs Write:** Use Edit for targeted changes; reserve Write for new files or complete rewrites.
- **Tool preference:** Avoid `echo >` or heredocs via Bash.
- **Restraint:** Do not create documentation or READMEs unless explicitly requested.
