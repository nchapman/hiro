Create a long-lived persistent agent instance with its own memory, todos, and conversation history.

The instance is created and returns its ID immediately — it does not run a prompt. Use SendMessage to communicate with it.

Use `name` and `description` to give the instance a meaningful identity (e.g. name: "Backend Lead", description: "Owns the API service rewrite"). These are stored in persona.md frontmatter and shown in listings and the dashboard. If omitted, the agent definition's name and description are used.

Use `persona` to specialize the instance for a specific role, project, or personality. This text is written to the persona.md body and injected into the agent's system prompt. For example: "You focus on Go and PostgreSQL. You prefer simple solutions and are skeptical of unnecessary abstractions."

When to create a persistent instance:
- You need an ongoing collaborator that accumulates context over time
- The agent will handle multiple related tasks across separate interactions
- You want the agent to maintain its own memory and task list

If you only need a single answer or task completed, use SpawnInstance instead.
