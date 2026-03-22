---
name: create-skill
description: Add new skills to existing agents by writing markdown files to their skills/ directory.
---

You can add skills to any agent by writing markdown files to its `skills/` directory.

## Steps

1. Identify the target agent by name (e.g. `coordinator`, `code-reviewer`).
2. Write a `.md` file to `agents/<agent-name>/skills/<skill-name>.md`.
3. The skill takes effect on the agent's next turn — the system prompt is rebuilt from disk each turn.

## Skill file format

```markdown
---
name: skill-name
description: Brief description of what this skill enables.
---

Instructions for the agent when using this skill.
Write as direct instructions — these are injected into the system prompt under "## Skills".
```

Both `name` and `description` are required in the frontmatter.

## Guidelines

- Use kebab-case for skill file names (e.g. `summarize-code.md`, `write-tests.md`).
- Keep skills focused — one capability per skill.
- Write instructions as if speaking directly to the agent that will use the skill.
- Don't duplicate instructions already in the agent's `agent.md` or other skills.
- Check the agent's existing skills first with `list_files agents/<agent-name>/skills/` to avoid overlap.
- If the target agent is currently running, the new skill takes effect on its next message — no restart needed.
