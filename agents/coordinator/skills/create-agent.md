---
name: create-agent
description: Create new agent definitions by writing markdown files to the agents/ directory.
---

You can create new agents at runtime. An agent is a directory under `agents/` with at minimum an `agent.md` file.

## Steps

1. Choose a short, descriptive kebab-case name for the agent (e.g. `code-reviewer`, `data-fetcher`).
2. Create the agent definition directory and required file:
   - `agents/<name>/agent.md` — **required**. Contains YAML frontmatter and a markdown body.
3. Optionally create supporting files:
   - `agents/<name>/soul.md` — persona, tone, and behavioral boundaries (no frontmatter needed, plain markdown)
   - `agents/<name>/tools.md` — tool usage guidelines (no frontmatter needed, plain markdown)
   - `agents/<name>/skills/<skill-name>.md` — specialized behavioral instructions (requires frontmatter with `name` and `description`)

## agent.md format

```markdown
---
name: <agent-name>
model: claude-sonnet-4-20250514
mode: persistent
description: One-line description of what this agent does.
---

The markdown body is the agent's system prompt — its core operating instructions.
Write this as direct instructions to the agent about what it is and how it should behave.
```

### Frontmatter fields

| Field | Required | Default | Values |
|-------|----------|---------|--------|
| `name` | yes | — | Must match the directory name |
| `model` | no | inherited from env | Any supported model ID |
| `mode` | no | `persistent` | `persistent` or `ephemeral` |
| `description` | no | — | Short description shown in `list_agents` |

### Mode guidance

- **persistent**: The agent keeps memory, todos, and conversation history across interactions. Use for agents that build up context over time or need to be long-running.
- **ephemeral**: The agent runs a single task and is cleaned up. Use for stateless, one-shot tasks. `spawn_agent` always forces ephemeral mode regardless of the config.

## skill file format

```markdown
---
name: skill-name
description: Brief description of the skill.
---

Instructions for this skill. These are injected into the agent's system prompt.
```

Both `name` and `description` are required in skill frontmatter.

## After creating an agent

- Use `start_agent` with the agent name to launch it as a persistent agent, or `spawn_agent` to run it ephemerally with a one-shot prompt.
- The agent definition is loaded fresh from disk each time, so edits to the markdown files take effect on the next start/spawn.
- You can verify the files look correct with `read_file` before starting the agent.

## Guidelines

- Write clear, focused system prompts. An agent should have a well-defined purpose.
- Keep agent prompts concise — avoid walls of text. Trust the agent to be capable.
- If an agent needs to create or manage its own sub-agents, it will automatically get manager tools.
- Don't duplicate capabilities that already exist — check `agents/` first.
