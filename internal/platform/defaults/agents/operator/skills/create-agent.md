---
name: create-agent
description: Create new agents at runtime. Use when asked to build, define, or set up a new agent.
---

# Purpose
Create specialized agents at runtime. Each agent is a directory under `agents/` containing at least an `agent.md` file.

## Steps

1. Choose a short, descriptive name using letters, numbers, hyphens, or underscores (e.g., `code-reviewer`, `data_analyst`).
2. Create `agents/<name>/agent.md` with frontmatter and a markdown body.
3. Optionally add skills in `agents/<name>/skills/` (use the `create-skill` skill for format details).

## agent.md Format

```markdown
---
name: <agent-name>
description: <what this agent does and when to use it>
allowed_tools: [Bash, Read, Write, Edit, Glob, Grep, WebFetch, TaskOutput, TaskStop]
---

# Your Mission
Direct instructions to the agent about what it is and how it should behave.
```

### Frontmatter Fields

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Letters, numbers, hyphens, underscores (`^[a-zA-Z0-9_-]+$`). Should match the directory name. |
| `description` | yes | Shown in agent listings. Tells the parent when to use this agent and what context to provide. |
| `allowed_tools` | no | Built-in and management tools. No declaration = no tools. |
| `disallowed_tools` | no | Deny rules evaluated at call time (e.g., `Bash(rm -rf *)`). |
| `model` | no | Model override for this agent (e.g., `anthropic/claude-sonnet-4`). |
| `max_turns` | no | Max agentic loop turns. 0 = unlimited. |

### Writing Good Descriptions

The description is what parent agents read to decide when to spawn this agent. A good description:

- States the agent's role clearly
- Tells the caller when to use it vs. other agents
- Says what context to provide in the prompt

Examples:
- `Default all-around agent with full file and shell access. Use for one-off tasks or as a long-running collaborator.`
- `Read-only reviewer for evaluating completed work. Provide the content to review and the intent behind it.`
- `Subject matter expert for domain-specific problems. Specify the domain, provide context, and describe what you need.`

### Writing Good System Prompts

- Be direct. Write instructions, not descriptions. "You review completed work" not "This agent is designed to review completed work."
- Keep it short. The parent's prompt shapes per-task behavior — the system prompt sets the baseline.
- Don't repeat the description. The description is for the caller; the system prompt is for the agent.

## Agent Modes

Mode is set by the caller at runtime, not part of the agent definition:

- **ephemeral**: Single task, then cleaned up. Default for `SpawnInstance`.
- **persistent**: Keeps persona, memory, todos, and history. Created via `CreatePersistentInstance`.

## After Creating

- Use `SpawnInstance` to run it, or `CreatePersistentInstance` for a long-lived instance. Use `persona` to specialize.
- The definition is loaded fresh from disk — edits take effect on the next start.
- Check `agents/` first to avoid duplicating existing agents.
