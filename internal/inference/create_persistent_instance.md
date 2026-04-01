Create a long-lived agent instance with its own memory, todos, and conversation history.

- **Persistent**: has memory and conversation history. Interact with it via SendMessage over multiple exchanges.
- **Coordinator**: persistent plus management tools and write access to agent definitions.

The instance is created and returns its ID immediately — it does not run a prompt. Use SendMessage to communicate with it.

When to create a persistent instance:
- You need an ongoing collaborator that accumulates context over time
- The agent will handle multiple related tasks across separate interactions
- You want the agent to maintain its own memory and task list

If you only need a single answer or task completed, use SpawnInstance instead.
