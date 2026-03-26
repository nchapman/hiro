Apply multiple find-and-replace edits to a single file in one operation. Prefer this over repeated edit_file calls when making several changes to the same file.

## Parameters

- `file_path`: Absolute path to the file.
- `edits`: Array of edit operations, each with `old_string`, `new_string`, and optional `replace_all`.

## How it works

- Edits are applied sequentially in the order provided.
- Each edit operates on the result of the previous one.
- If all edits fail, no changes are written and an error is returned.
- If some edits fail, the successful ones are applied and a partial-success error is returned listing which failed.
- To create a new file, set the first edit's `old_string` to empty.

## Critical: exact matching

All rules from the edit_file tool apply to each edit — old_string must match exactly, including whitespace and indentation. Include 3-5 lines of surrounding context.

## Tips

- Plan the sequence carefully: earlier edits change the content that later edits must match.
- Read the file first to get exact text.
- Check the response for failed edits and retry them if needed.
- If edits are independent (don't overlap), order doesn't matter.
- If edits are dependent, verify that each old_string will still match after prior edits.
