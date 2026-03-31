Reads a file from the filesystem.

- The file_path parameter can be absolute or relative to the working directory
- By default, it reads the entire file. Use `offset` and `limit` for large files
- Results are returned with line numbers (cat -n format), starting at 1
- Output is limited to 64KB
- This tool can only read files, not directories. Use Bash with `ls` for directories