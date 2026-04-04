Run an agent to complete a task. The agent executes the prompt, returns the result, and is cleaned up.

- Blocks until the agent finishes, unless `background` is set
- Set `background: true` to return immediately — you'll be notified when the agent completes with its result
- Results are capped at 32KB — summarize what matters for the user

When to use background:
- You have independent work to do while the agent runs
- You're launching multiple agents in parallel
- Do not poll or wait — continue working and respond when notified

Writing good prompts:
- The agent starts with zero context. Give it everything: file paths, constraints, expected output format.
- Be specific. "Fix the nil pointer in handleRequest at api/server.go:47" beats "look at the code and fix what's wrong."
- For research tasks, state the question clearly. For implementation tasks, describe the desired outcome.
