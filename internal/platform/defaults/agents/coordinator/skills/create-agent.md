---
name: create-agent
description: Create new agents at runtime. Use when asked to build, define, or set up a new agent.
---

You can create new agents at runtime. An agent is a directory under `agents/` with at minimum an `agent.md` file.

## Steps

1. Choose a short, descriptive kebab-case name (e.g. `code-reviewer`, `data-analyst`).
2. Create `agents/<name>/agent.md` with frontmatter and a markdown body.
3. Optionally create skills in `agents/<name>/skills/` (use the `create-skill` skill for format details).

## agent.md format

```markdown
---
name: <agent-name>
description: <what this agent does and when to use it>
allowed_tools: [Bash, Read, Write, Edit, Glob, Grep, WebFetch, TaskOutput, TaskStop]
---

System prompt — direct instructions to the agent about what it is and how it should behave.
```

### Frontmatter fields

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Kebab-case. Must match the directory name. |
| `description` | yes | Shown in agent listings. Tells the parent agent when to use this agent and what context to provide. |
| `allowed_tools` | no | Built-in and management tools. No declaration = no tools. Management tools (`CreatePersistentInstance`, `ResumeInstance`, `StopInstance`, `DeleteInstance`, `SendMessage`, `ListInstances`) go here too. |
| `groups` | no | Unix groups for filesystem access. `[hiro-coordinators]` grants write to `agents/` and `skills/`. |

### Writing good descriptions

The description is the most important field — it's what parent agents read to decide when to spawn this agent and how to prompt it. A good description:

- States the agent's role clearly (what it does)
- Tells the caller when to use it vs other agents
- Says what context to provide in the prompt

Examples:
- `Default all-around agent with full file and shell access. Use for one-off tasks (ephemeral) or as a long-running collaborator (persistent). When in doubt, use this agent.`
- `Read-only reviewer for evaluating completed work. Cannot modify files. Provide the content to review and the goal or intent behind it.`
- `Subject matter expert for problems requiring domain-specific expertise in any field. Specify the domain, provide the context, and describe what you need (planning, review, or implementation).`

### Writing good system prompts

- Keep it short. The parent agent's prompt shapes the agent's behavior for each task — the system prompt just sets the baseline.
- Be direct. Write instructions, not descriptions. "You review completed work" not "This agent is designed to review completed work."
- Don't over-specify. A few high-value behavioral guidelines beat a wall of rules.
- Don't repeat what the description says. The description is for the caller; the system prompt is for the agent.

## Agent modes

Mode is a **runtime property** set by the caller, not the agent definition. The same agent can run in different modes:

- **ephemeral**: Runs a single task and is cleaned up. Default for `SpawnInstance`.
- **persistent**: Keeps persona, memory, todos, and conversation history across interactions. Created via `CreatePersistentInstance`.

## After creating an agent

- Use `SpawnInstance` to run it as a one-off, or `CreatePersistentInstance` to create a long-lived instance. Use the `persona` parameter to specialize the instance at creation time.
- The agent definition is loaded fresh from disk each time — edits take effect on the next start.
- Verify the files look correct with `Read` before starting.
- Don't duplicate agents that already exist — check `agents/` first.
