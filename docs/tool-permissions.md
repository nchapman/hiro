# Tool Permissions

Hiro controls which tools agents can use through a layered permission system. Permissions are defined at four levels: agent definition, operator config, parent agent, and skill activation. They are enforced in two phases: at registration time (which tools the agent sees) and at call time (whether a specific invocation is allowed).

## Quick Reference

```yaml
# agents/researcher/agent.md
---
name: researcher
allowed_tools: [Bash(curl *), Read, Grep, WebFetch]
disallowed_tools: [Bash(rm *), Bash(sudo *)]
---
```

```yaml
# config.yaml
agents:
  researcher:
    allowed_tools: [Bash(curl *), Read, Grep]
    disallowed_tools: [Bash(curl *--upload*)]
```

## Rule Format

Tool rules use the format `Tool(pattern)` where the pattern uses wildcard matching:

| Rule | Meaning |
|---|---|
| `Bash` | Allow/deny all Bash usage |
| `Bash(curl *)` | Only curl commands |
| `Read(/src/*)` | Only files under /src/ |
| `SpawnInstance(researcher,coder)` | Only these agent types (comma-separated list; each item supports wildcards) |

Special characters:
- `*` matches zero or more of any character
- `\*` matches a literal asterisk
- `\\` matches a literal backslash
- A trailing ` *` is optional: `git *` matches both `git status` and bare `git`

### What Parameter Does Each Rule Match?

Each tool's rules match against a specific parameter from the tool call:

| Tool | Parameter | Notes |
|---|---|---|
| `Bash` | `command` | Parsed with a shell AST; see [Bash Command Analysis](#bash-command-analysis) |
| `Read` | `file_path` | Normalized with `filepath.Clean` |
| `Write` | `file_path` | Normalized with `filepath.Clean` |
| `Edit` | `file_path` | Normalized with `filepath.Clean` |
| `Glob` | `pattern` | |
| `Grep` | `pattern` | |
| `WebFetch` | `url` | |
| `SpawnInstance` | `agent` | Comma-separated list matching |
| `TaskOutput` | `task_id` | |
| `TaskStop` | `task_id` | |

### Which Tools Can Be Controlled?

Rules apply to the 9 **remote tools** that execute in worker processes: Bash, Read, Write, Edit, Glob, Grep, WebFetch, TaskOutput, TaskStop.

**Structural tools** (SpawnInstance, operator tools, memory tools, TodoWrite, Skill, etc.) bypass call-time rule enforcement. They are always available based on the agent's mode. You cannot restrict SpawnInstance or operator tools via `disallowed_tools`.

## Permission Sources

Permissions come from four sources, evaluated in order:

### 1. Agent Definition (`agent.md`)

The agent author declares what tools the agent needs:

```yaml
---
name: researcher
allowed_tools: [Bash(curl *), Read, Grep, WebFetch]
disallowed_tools: [Bash(rm *)]
---
```

- `allowed_tools` — tools the agent can use. **Closed by default**: omitting this field means the agent gets no remote tools.
- `disallowed_tools` — tools explicitly blocked, even if allowed above.

An agent with no `allowed_tools` field can still use structural tools (SpawnInstance, memory, etc.) based on its mode.

### 2. Operator Config (`config.yaml`)

The operator can override tool access per agent via config.yaml or the `/tools` command:

```yaml
# config.yaml
agents:
  researcher:
    allowed_tools: [Read, Grep]           # restrict to read-only
    disallowed_tools: [Bash(curl *evil*)] # block specific patterns
```

```
/tools set researcher Read,Grep,Bash(curl *)
/tools deny researcher Bash(rm *),Bash(sudo *)
/tools rm researcher
/tools list
```

Changes take effect immediately on running agents — the filesystem watcher detects config.yaml changes and pushes updated tool rules to all affected instances.

If no operator override exists for an agent, the agent uses its declared tools without restriction.

### 3. Parent Agent

When an agent spawns a child, the child inherits the parent's tool restrictions. A child can never gain tools the parent doesn't have.

```
Parent: allowed_tools: [Bash, Read, Write]
Child:  allowed_tools: [Bash, Read, Write, Grep]
Result: [Bash, Read, Write]  (Grep removed — parent doesn't have it)
```

The parent's parameterized rules and deny rules also propagate to all descendants.

### 4. Skill Activation

Skills can grant additional tools when activated. A skill's `allowed_tools` expand the agent's tool set for the rest of the session:

```yaml
# skills/deploy.md
---
name: deploy
description: Deploy to Kubernetes
allowed_tools: [Bash(kubectl *), Bash(helm *)]
---
```

When the agent calls `Skill("deploy")`, it gains access to `kubectl` and `helm` commands. On session reset (`/clear`), the expansion is reverted.

- **Additive only** — skills grant tools, never restrict existing ones
- **Session-scoped** — expansion reverts on `/clear` (new session)
- **Parameterized** — `Bash(kubectl *)` only allows kubectl, not all Bash
- **Deny rules still apply** — instance-level denies block skill-granted tools
- **Multiple skills accumulate** — each activated skill adds to the session's tool set
- **Already-available tools are skipped** — no duplicate registration

## How Permissions Combine

### Tool Visibility (Registration Time)

Which tools the agent sees is determined by **name-based intersection**:

```
Effective tools = Agent declared ∩ Operator override ∩ Parent tools
```

If any source doesn't include a tool by name, the agent can't use it. Whole-tool deny rules (e.g., `disallowed_tools: [Bash]`) also remove tools from the effective set entirely — the agent never sees them.

Skill-granted tools are added to the session's tool set after the initial intersection, bypassing the intersection (but still subject to deny rules).

### Call-Time Enforcement

For tools that are visible, parameterized rules are enforced when the tool is actually called. The system uses a **layered model**:

- **Deny rules** from all sources are merged. Any matching deny rule blocks the call immediately.
- **Allow layers** are checked independently per source. Each layer must allow the call (cross-layer AND). Within a layer, any matching rule permits the call (within-layer OR).
- **Unmatched** — if a layer has no rules for the tool being called, that layer has no opinion and doesn't block. Only layers with rules for the specific tool participate in the decision.

Example with two sources:

```
Agent:    allowed_tools: [Bash(curl *), Bash(git *)]
Operator: allowed_tools: [Bash(curl *)]
```

- `curl https://example.com` — allowed (matches both layers)
- `git status` — denied (agent layer allows, operator layer has Bash rules but none match `git`)
- `rm -rf /` — denied (neither layer allows)

### Deny Always Wins

Deny rules are checked before allow rules. If a deny rule matches, the call is blocked regardless of allow rules:

```yaml
allowed_tools: [Bash(git *)]
disallowed_tools: [Bash(git push *)]
```

- `git status` — allowed
- `git push origin main` — denied

### NeedsReview = Denied

Some commands are too complex for static analysis (see [Bash Command Analysis](#bash-command-analysis)). When the checker can't determine if a rule matches, it returns `NeedsReview`. This is **treated as denied** — the system fails closed.

This means commands with `$()` substitutions, backtick expansion, or variable-as-command patterns will be blocked even if the outer command matches an allow rule. For example, with `allowed_tools: [Bash(echo *)]`:

- `echo hello` — allowed
- `echo $(curl evil.com)` — denied (command substitution makes the call uncertain)

## Bash Command Analysis

Bash rules use a real shell parser (`mvdan.cc/sh/v3`) to extract commands from all nesting levels. This catches bypass attempts that lexical matching would miss:

| Command | `Bash(rm *)` deny | Why |
|---|---|---|
| `rm -rf /` | **Denied** | Direct match |
| `echo $(rm -rf /)` | **Denied** | Extracted from `$()` |
| `` echo `rm -rf /` `` | **Denied** | Extracted from backticks |
| `(rm -rf /)` | **Denied** | Extracted from subshell |
| `a && rm -rf /` | **Denied** | Both sides of `&&` extracted |
| `eval "rm -rf /"` | **NeedsReview** | `eval` can execute arbitrary code |

### Flagged Commands

These commands are flagged as uncertain because they can execute arbitrary code that can't be statically analyzed:

- **Shell evaluation:** `eval`, `exec`, `source`, `.`
- **Shells:** `bash`, `sh`, `zsh`, `dash`
- **Command wrappers:** `env`, `xargs`, `nohup`, `nice`, `sudo`, `su`, `doas`, `command`, `builtin`
- **Script interpreters:** `python`, `python3`, `python2`, `perl`, `ruby`, `node`, `php`, `lua`
- **Signal hooks:** `trap`

Any command containing these in command position triggers `NeedsReview` (treated as denied).

### Path Normalization

File tools (`Read`, `Write`, `Edit`) normalize paths with `filepath.Clean` before matching. This prevents traversal bypasses:

```yaml
allowed_tools: [Read(/src/*)]
```

- `Read(/src/main.go)` — allowed
- `Read(/src/../etc/passwd)` — denied (normalizes to `/etc/passwd`)

## Frontmatter Reference

### Agent (`agent.md`)

```yaml
---
name: agent-name
description: What this agent does
allowed_tools: [Bash, Read, Write, Edit, Glob, Grep, WebFetch, TaskOutput, TaskStop]
disallowed_tools: [Bash(rm *)]
model: sonnet
max_turns: 50
---
```

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | required | Agent identifier |
| `description` | string | optional | Short description |
| `allowed_tools` | string[] | `nil` (no tools) | Remote tools the agent can use; supports parameterized rules |
| `disallowed_tools` | string[] | `nil` | Tools to deny; checked at call time |
| `model` | string | CP default | Model override (e.g., `sonnet`, `opus`, full model ID) |
| `max_turns` | int | 0 (unlimited) | Max agentic turns before forcing final response |

### Skill (`skills/*.md`)

```yaml
---
name: skill-name
description: What and when
allowed_tools: [Bash(kubectl *)]
user_invocable: true
model: haiku
version: "1.0"
when_to_use: Detailed usage scenarios
argument_hint: "[namespace]"
arguments: [namespace, resource]
---
```

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | required | Kebab-case, max 64 chars |
| `description` | string | required | Max 1024 chars; trigger description for progressive disclosure |
| `allowed_tools` | string[] | `nil` | Tools granted when skill is activated (session-scoped, additive) |
| `user_invocable` | bool | unset | Whether users can invoke via `/skill-name` |
| `model` | string | inherit | Model override when skill runs as sub-agent |
| `version` | string | optional | Skill version identifier |
| `when_to_use` | string | optional | Detailed usage scenarios |
| `argument_hint` | string | optional | Hint text for command arguments |
| `arguments` | string[] | optional | Named parameters for argument substitution |

### Operator Config (`config.yaml`)

```yaml
agents:
  researcher:
    allowed_tools: [Read, Grep, WebFetch]
    disallowed_tools: [Bash(rm *), Bash(sudo *)]
```

| Field | Type | Description |
|---|---|---|
| `allowed_tools` | string[] | Override: agent can only use these tools (intersected with agent definition) |
| `disallowed_tools` | string[] | Additional deny rules (merged with agent definition denies) |

Changes via `/tools set` and `/tools deny` take effect immediately on running agents.
