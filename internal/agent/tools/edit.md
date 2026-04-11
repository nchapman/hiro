# Purpose
Precisely find and replace text in existing files using exact matches.

## Usage & Constraints
- **Exact Match:** `old_string` must be a character-perfect match, including whitespace, indentation, and no line prefixes.
- **Uniqueness:** Fails if `old_string` appears multiple times; add context to disambiguate or use `replace_all`.
- **Creation/Deletion:** Set `old_string` to empty to create a new file; set `new_string` to empty to delete matched text.

## Best Practices
- **Read first:** Always read the file before editing to ensure a perfect match.
- **Precision:** Keep `old_string` short (2-4 lines) but unique.
- **Tool preference:** Use Edit over Write for modifying existing files. Avoid `sed` or `awk` through Bash.
