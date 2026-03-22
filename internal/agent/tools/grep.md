Fast content search tool that finds files containing specific text or regex patterns.

## Usage

- Provide a regex pattern to search within file contents.
- Set `literal_text=true` to escape special regex characters (search for exact text).
- Optional starting directory (defaults to the working directory).
- Optional `include` glob pattern to filter which files to search (e.g. `*.go`).
- Results sorted with most recently modified files first.

## Regex examples

- `function` — literal text search
- `log\..*Error` — text starting with "log." ending with "Error"
- `import\s+.*\s+from` — import statements

## Include patterns

- `*.go` — only search Go files
- `*.{ts,tsx}` — only search TypeScript files
- `*.py` — only search Python files

## Limitations

- Results limited to 100 matches
- Binary files are skipped
- Hidden files/directories (starting with `.`) are skipped
- `node_modules`, `vendor`, `dist`, `__pycache__`, `.git` are skipped

## Tips

- Use `literal_text=true` when searching for text with dots, parentheses, etc.
- Combine with glob: find files with glob, search contents with grep.
- Narrow results with `include` pattern for faster searches.
