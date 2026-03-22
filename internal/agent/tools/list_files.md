List the contents of a directory tree, showing files and subdirectories.

Automatically skips hidden directories, node_modules, vendor, dist, __pycache__, and .git. Results are limited to 500 entries.

## Parameters

- `path`: Directory to list. Defaults to the working directory.
- `pattern`: Optional glob pattern to filter by filename (e.g. `*.go`, `*.ts`). Matches against the file's base name only. Directories are always traversed regardless of the pattern.

## Tips

- Use this to explore project structure or find files by extension.
- For deep searches across subdirectories with complex patterns, prefer the glob tool.
- Directories are shown with a trailing `/`.
