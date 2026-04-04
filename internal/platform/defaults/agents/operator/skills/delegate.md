---
name: delegate
description: Delegate work to instances. Use when a task should be handled by a specialist or benefits from a focused context.
---

## Choosing the right approach

**Handle it yourself** when:
- The task is straightforward and you have the context
- It would take longer to explain to an instance than to do it
- The work requires iterating on a conversation with the user

**Use `SpawnInstance`** when:
- The task is self-contained with a clear deliverable
- The work benefits from a clean, focused context (no history baggage)
- You don't need the agent to persist after the task
- You want to break large work into independent chunks, each with a focused scope

**Use `SpawnInstance` (persistent) + `SendMessage`** when:
- You need to send multiple messages to the same agent over time
- You want a long-running collaborator (e.g., a monitor, a researcher)
- The agent benefits from building up its own context across interactions

## Writing good prompts for instances

The instance has no context beyond what you give it. Every prompt should be self-contained:

1. **State the goal clearly.** What specific output do you need?
2. **Provide all necessary context.** File paths, background, constraints. Don't say "the file we discussed" — say which file.
3. **Specify the format.** If you need structured output, say so. If you need code, say where it should go.
4. **Set boundaries.** What should the agent NOT do? What's out of scope?

Bad: "Review the code changes"
Good: "Review the changes in `internal/api/server.go` for security issues. Focus on input validation and authentication. List each finding with the line number, the issue, and a suggested fix."

## Batching independent work

Each `SpawnInstance` call blocks until the instance finishes. For large tasks, break the work into independent chunks and spawn one agent per chunk:

- Each chunk should be self-contained with its own clear scope
- Spawn sequentially, then synthesize all results at the end
- Keep chunks genuinely independent — if task B depends on the output of task A, run them in order

## Synthesizing results

When you get results back from instances:

- **Don't just relay raw output.** Synthesize, summarize, and present a coherent answer.
- **Resolve conflicts.** If two agents give contradictory answers, investigate and pick the right one (or flag the ambiguity).
- **Filter noise.** Drop irrelevant details. Surface what matters.
- **Attribute when useful.** If the user might want to dig deeper, mention which agent produced which finding.

## Handling failures

- If an instance fails, try to understand why before retrying. Read the error.
- If an agent definition doesn't exist, consider creating one with the `create-agent` skill, or handle the task yourself.
- If a persistent instance becomes unresponsive, stop it and start a fresh one.
- Don't retry the same failing operation in a loop. Adapt your approach.

## Managing running instances

- Use `ListInstances` to see your direct child agents before starting duplicates. (It doesn't show grandchildren — instances started by your children.)
- Stop instances you no longer need — they consume resources.
- Persistent instances survive restarts. Check for restored instances before creating new ones.
