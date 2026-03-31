---
name: create-skill
description: Add skills to agents or the shared workspace. Use when asked to teach an agent something new or add a capability.
---

You can add skills to any agent by writing markdown files, or create shared skills available to all agents.

## Agent-specific skills

Write to `agents/<agent-name>/skills/`. Two formats are supported:

**Flat file:** `agents/<agent-name>/skills/<skill-name>.md`

**Directory:** `agents/<agent-name>/skills/<skill-name>/SKILL.md` — use this when the skill needs bundled resources like scripts, references, or assets in subdirectories alongside the SKILL.md.

## Shared workspace skills

Write to the workspace-level `skills/` directory. These are available to all agents. Agent-specific skills take precedence over shared skills with the same name.

## Skill file format

```markdown
---
name: skill-name
description: What this skill does and when to use it. Include trigger phrases.
license: MIT
compatibility: Requires python 3.8+
metadata:
  author: your-name
  version: "1.0"
---

Full instructions for the agent when using this skill.
```

### Required fields

| Field | Rules |
|-------|-------|
| `name` | Lowercase kebab-case (`a-z`, `0-9`, hyphens). Max 64 chars. Must match directory name for directory skills. |
| `description` | What the skill does AND when to use it. Max 1024 chars. This is what agents see to decide whether to load the skill. |

### Optional fields

| Field | Purpose |
|-------|---------|
| `license` | License identifier (e.g. `MIT`, `Apache-2.0`) |
| `compatibility` | System/dependency requirements. Max 500 chars. |
| `metadata` | Arbitrary key-value pairs (author, version, category, etc.) |

## How skills work at runtime

Skills use progressive disclosure. Only the name and description appear in the agent's system prompt. The agent calls `Skill("<skill-name>")` to load the full instructions and any bundled resources. This keeps prompts lean as skills grow.

## Skill directory format

For skills that bundle resources:

```
skills/my-skill/
  SKILL.md         # Required — the skill definition
  scripts/         # Optional — executable code
  references/      # Optional — documentation, examples
  assets/          # Optional — templates, data files
```

The agent can access all files in the skill directory via `Read` and `Glob`.

## Guidelines

- Use kebab-case for names (e.g. `summarize-code`, `write-tests`).
- Write the description as a trigger: what the skill does + when to use it.
- Keep the SKILL.md body focused on instructions. Move detailed docs to `references/`.
- Don't duplicate instructions already in the agent's `agent.md` or other skills.
- Check existing skills first with `Glob` or `Bash` to list `agents/<agent-name>/skills/`.
- Skills take effect on the agent's next message — no restart needed.
