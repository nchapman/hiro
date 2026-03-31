Edit a file by replacing exact text matches. Edits are applied sequentially.

`old_string` must match file content exactly, including whitespace. Empty `old_string` in the first edit creates a new file. Empty `new_string` deletes the matched text. When `replace_all` is false (default), `old_string` must appear exactly once.
