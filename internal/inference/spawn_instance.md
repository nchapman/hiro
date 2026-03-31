Launch a new agent instance from a definition in agents/.

- Ephemeral (default): executes the prompt, returns the result, then cleans up. Blocks until complete
- Persistent: creates a durable instance with its own memory, todos, and conversation history
- Coordinator: persistent with additional management tools and write access to agent definitions
- Results are not shown to the user automatically — you need to relay them
- Output is capped at 32KB

Best practices:
- Default to ephemeral for self-contained, one-shot work
- Choose persistent when you'll interact with the instance over multiple exchanges
- Give the instance everything it needs in the prompt — paths, constraints, expected format. It starts with zero context
- Be specific in your instructions. "Fix the bug in server.go" is better than "look at the code and fix what's wrong"
