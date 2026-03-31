Send a message to a running child instance and get its response.

- Blocks until the instance replies
- Scoped to your descendants — you cannot message siblings or ancestors
- Result truncated at 32KB

Best practices:
- Provide full context in each message — the instance may have lost earlier context to compaction
- Use for ongoing collaboration with persistent instances. For one-shot tasks, use SpawnInstance in ephemeral mode instead