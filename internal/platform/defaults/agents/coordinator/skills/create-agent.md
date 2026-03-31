---
name: create-agent
description: Create new agents at runtime. Use when asked to build, define, or set up a new agent.
---

You can create new agents at runtime. An agent is a directory under `agents/` with at minimum an `agent.md` file.

## Steps

1. Choose a short, descriptive kebab-case name for the agent (e.g. `code-reviewer`, `data-fetcher`).
2. Create the agent definition directory and required file:
   - `agents/<name>/agent.md` — **required**. Contains YAML frontmatter and a markdown body.
3. Optionally create skills:
   - `agents/<name>/skills/<skill-name>.md` — flat skill file (requires frontmatter with `name` and `description`)
   - `agents/<name>/skills/<skill-name>/SKILL.md` — directory skill with optional `scripts/`, `references/`, `assets/` subdirs

## agent.md format

```markdown
---
name: <agent-name>
description: One-line description of what this agent does.
tools: [bash, read_file, write_file, edit_file, multiedit_file, list_files, glob, grep, fetch, job_output, job_kill]
---

The markdown body is the agent's system prompt — its core operating instructions.
Write this as direct instructions to the agent about what it is and how it should behave.
```

### Frontmatter fields

| Field | Required | Default | Values |
|-------|----------|---------|--------|
| `name` | yes | — | Must match the directory name |
| `description` | no | — | Short description shown in `list_instances` |
| `tools` | no | *(none)* | List of built-in tools the agent can use |

### Mode guidance

- **persistent**: The agent keeps persona, memory, todos, and conversation history across interactions. Use for agents that build up context over time or need to be long-running.
- **coordinator**: A superset of persistent — also gets agent management tools (`resume_instance`, `stop_instance`, `send_message`, `list_instances`) and write access to `agents/` and `skills/` directories. Use for agents that need to manage other agents.
- **ephemeral**: The agent runs a single task and is cleaned up. Use for stateless, one-shot tasks. `spawn_instance` always forces ephemeral mode regardless of the config.

## Instance-level files

These files live in the instance directory, not the agent definition. The agent manages them at runtime:

- **`persona.md`** — who this instance is. Identity, tone, behavioral traits. Seeded by the operator or written by the agent via `persona_write`. Appears in the system prompt under `## Persona`.
- **`memory.md`** — what this instance knows. Facts, context, decisions. Written by the agent via `memory_write`. Appears in the system prompt under `## Memories`.

## After creating an agent

- Use `spawn_instance` with `mode: "persistent"` to launch it as a persistent instance, or with the default ephemeral mode to run it with a one-shot prompt.
- The agent definition is loaded fresh from disk each time, so edits to the markdown files take effect on the next start/spawn.
- You can verify the files look correct with `read_file` before starting the agent.

## Guidelines

- Write clear, focused system prompts. An agent should have a well-defined purpose.
- Keep agent prompts concise — avoid walls of text. Trust the agent to be capable.
- If an agent needs to create or manage its own sub-agents, it will automatically get manager tools.
- Don't duplicate capabilities that already exist — check `agents/` first.
