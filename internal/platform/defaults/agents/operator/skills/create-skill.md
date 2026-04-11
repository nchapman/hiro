---
name: create-skill
description: Add skills to agents or the shared workspace. Use when asked to teach an agent something new or add a capability.
---

# Purpose
Add skills to agents or the shared workspace. Skills are markdown files loaded on demand via `Skill("<name>")`.

## Placement

- **Agent-specific:** `agents/<agent-name>/skills/<skill-name>.md` (or `agents/<agent-name>/skills/<skill-name>/SKILL.md` for directory skills)
- **Shared (all agents):** `skills/<skill-name>.md` (or `skills/<skill-name>/SKILL.md`)

Agent-specific skills take precedence over shared skills with the same name.

## Skill File Format

```markdown
---
name: skill-name
description: What this skill does and when to use it.
license: MIT
metadata:
  author: your-name
  version: "1.0"
---

Full instructions for the agent when using this skill.
```

### Fields

| Field | Required | Rules |
|-------|----------|-------|
| `name` | yes | Lowercase kebab-case (`^[a-z0-9]+(-[a-z0-9]+)*$`). Max 64 chars. Must match directory name for directory skills. |
| `description` | yes | What the skill does AND when to use it. Max 1024 chars. This is what agents see to decide whether to load the skill. |
| `license` | no | License identifier (e.g., `MIT`, `Apache-2.0`). |
| `compatibility` | no | System/dependency requirements. Max 500 chars. |
| `metadata` | no | Arbitrary key-value pairs (author, version, etc.). |
| `user_invocable` | no | If true, users can type `/<skill-name>` to invoke the skill directly. |
| `argument_hint` | no | Hint text shown for command arguments (e.g., `"<file-path>"`). |
| `arguments` | no | Named parameters for substitution in the skill body. |

## Directory Skills

For skills that bundle resources alongside the definition:

```
skills/my-skill/
  SKILL.md         # Required — the skill definition
  scripts/         # Optional — executable code
  references/      # Optional — documentation, examples
  assets/          # Optional — templates, data files
```

The agent can access all files in the skill directory via `Read` and `Glob`.

## Guidelines

- Write the description as a trigger: what the skill does + when to use it.
- Keep the body focused on instructions. Move detailed docs to `references/`.
- Check existing skills first to avoid duplicates.
- Skills take effect on the agent's next message — no restart needed.
