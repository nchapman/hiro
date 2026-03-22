Fast file pattern matching tool that finds files by name/pattern, returning paths sorted by modification time (newest first).

## Usage

- Provide a glob pattern to match against file paths.
- Optional starting directory (defaults to the working directory).
- Results sorted with most recently modified files first.

## Pattern syntax

- `*` matches any sequence of non-separator characters
- `**` matches any sequence including path separators
- `?` matches any single non-separator character
- `[...]` matches any character in brackets

## Examples

- `*.go` — Go files in current directory
- `**/*.go` — Go files in any subdirectory
- `src/**/*.{ts,tsx}` — TypeScript files under src/
- `*.{html,css,js}` — HTML, CSS, and JS files

## Limitations

- Results limited to 100 files
- Does not search file contents (use grep for that)
- Hidden files/directories (starting with `.`) are skipped
- `node_modules`, `vendor`, `dist`, `__pycache__`, `.git` are skipped
