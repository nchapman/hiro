Performs exact string replacements in files.

- The edit will FAIL if `old_string` is not unique in the file. Provide more surrounding context to make it unique, or use `replace_all` to change every instance
- Use `replace_all` for renaming variables or replacing repeated patterns across the file
- When `old_string` is empty and `new_string` has content, creates a new file (fails if file exists)
- When `new_string` is empty, deletes the matched text
- `old_string` must match file content exactly, including whitespace and indentation