# Tool Permissions

Hiro controls which tools agents can use through a layered permission system. Permissions are defined at three levels: instance config (seeded from agent definition), parent agent, and skill activation. They are enforced in two phases: at registration time (which tools the agent sees) and at call time (whether a specific invocation is allowed).

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

Rules apply to the 10 tools registered in the rule checker: Bash, Read, Write, Edit, Glob, Grep, WebFetch, SpawnInstance, TaskOutput, TaskStop.

**Structural tools** (management tools, memory tools, TodoWrite, Skill, etc.) bypass call-time rule enforcement. They are always available based on the agent's mode and `allowed_tools` declaration.

## Permission Sources

Permissions come from three sources, evaluated in order:

### 1. Instance Config (`config/instances/<uuid>.yaml`)

Each instance owns its tool declarations, stored outside the instance directory so Landlock prevents agents from modifying their own tool config. These are **seeded from `agent.md`** at creation time and decoupled thereafter — changes to `agent.md` `allowed_tools` do not flow to existing instances.

```yaml
# config/instances/<uuid>.yaml
allowed_tools: [Bash(curl *), Read, Grep, WebFetch]
disallowed_tools: [Bash(rm *)]
```

The agent definition (`agent.md`) provides the initial template:

```yaml
# agents/researcher/agent.md
---
name: researcher
allowed_tools: [Bash(curl *), Read, Grep, WebFetch]
disallowed_tools: [Bash(rm *)]
---
```

- `allowed_tools` — tools the instance can use. **Closed by default**: omitting this field means the agent gets no remote tools.
- `disallowed_tools` — tools explicitly blocked, even if allowed above.

An instance with no `allowed_tools` field can still use structural tools (SpawnInstance, memory, etc.) based on its mode.

### 2. Parent Agent

When an agent spawns a child, the child inherits the parent's tool restrictions. A child can never gain tools the parent doesn't have.

```
Parent: allowed_tools: [Bash, Read, Write]
Child:  allowed_tools: [Bash, Read, Write, Grep]
Result: [Bash, Read, Write]  (Grep removed — parent doesn't have it)
```

The parent's parameterized rules and deny rules also propagate to all descendants.

### 3. Skill Activation

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
Effective tools = Instance declared ∩ Parent tools
```

If any source doesn't include a tool by name, the agent can't use it. Whole-tool deny rules (e.g., `disallowed_tools: [Bash]`) also remove tools from the effective set entirely — the agent never sees them.

Skill-granted tools are added to the session's tool set after the initial intersection, bypassing the intersection (but still subject to deny rules).

### Call-Time Enforcement

For tools that are visible, parameterized rules are enforced when the tool is actually called. The system uses a **layered model**:

- **Deny rules** from all sources are merged. Any matching deny rule blocks the call immediately.
- **Allow layers** are checked independently per source. Each layer must allow the call (cross-layer AND). Within a layer, any matching rule permits the call (within-layer OR).
- **Unmatched** — if a layer has no rules for the tool being called, that layer has no opinion and doesn't block. Only layers with rules for the specific tool participate in the decision.

Example with parent inheritance:

```
Parent:   allowed_tools: [Bash(curl *)]
Child:    allowed_tools: [Bash(curl *), Bash(git *)]
```

- `curl https://example.com` — allowed (matches both layers)
- `git status` — denied (child layer allows, parent layer has Bash rules but none match `git`)
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

## Filesystem Policy

Tool permissions govern *what tools an agent can call*. The filesystem policy governs *what paths workers can access at all*. The two layers compose: a worker with `Bash` enabled still has to read/write files through paths the policy allows, and a worker without a path in the policy cannot reach it even if it had the perfect `Bash(cat *)` rule.

The policy is declarative and lives in `config/config.yaml` under the `filesystem` key (parsed and compiled by `internal/platform/fspolicy`). If `filesystem` is absent, an embedded default is used. Three sections:

- **`base`** — paths granted to every worker, as `rw` or `ro`.
- **`on_tool`** — additional paths conditional on the agent having a named tool. Paths listed here at a higher privilege than `base` are promoted (e.g. `agents/` is RO in `base` but RW in `on_tool.CreatePersistentInstance`).
- **`per_instance`** — dynamic paths resolved per spawn: `$INSTANCE_DIR`, `$SESSION_DIR`, `$SOCKET_DIR`.

Variables are expanded with `$NAME` syntax. `$HOME` is the platform root (`/home/hiro`); other variables fall through to the worker process environment (e.g. `$MISE_DATA_DIR`). A variable that resolves to an empty string drops the whole path — useful for optional env-configured locations.

**Declaring filesystem needs for a new tool.** If a tool needs access beyond the base set (e.g. a new sandbox dir, a socket path, a package manager cache), add an `on_tool.<ToolName>` entry. The tool becomes a capability gate: only agents that declare the tool get the extra paths. No Go code changes.

**Self-escalation boundary.** Anything not listed in the policy is blocked by Landlock. The two load-bearing absences are `config/` and `db/` — `config/` holds the policy itself (so agents can't rewrite `config.yaml` to widen access), secrets, and per-instance tool declarations; `db/` holds the platform SQLite database. Both are blocked for every worker.

**Read/write split.** The in-process file-tool guard splits paths into *readable* (RW + RO) and *writable* (RW only). An agent without `CreatePersistentInstance` can `Read` files under `agents/` (base.ro) but cannot `Write` or `Edit` them. With the tool, `agents/` is promoted to RW and appears in both lists. This split matters most on non-Linux dev machines where Landlock is unavailable — the guard is the only restriction left.

**Bash gates dotfile access.** `~/.ssh`, `~/.gitconfig`, `~/.config`, `~/.cache`, and `~/.local` are not in `base` — they live only under `on_tool.Bash.rw`. A file-only agent (just `Read`/`Write`/`Edit`, no `Bash`) cannot reach any of them. This closes the "drop a poisoned `~/.gitconfig` hook" attack for restricted agents, because the only tools that actually read those files (git, ssh, gh, pip, npm) require Bash to invoke. Bash agents retain full access.

**Mounts.** `$HOME/mounts` is granted RW in the policy. Per-mount RW/RO is enforced at the mount layer — Docker's `:ro` bind flag, NFS `ro`, FUSE read-only exports — which returns `EROFS` on writes regardless of Landlock's opinion. fspolicy takes no position on per-mount modes; the filesystem is the source of truth.

**Reload.** The policy is held in memory by the control plane alongside the rest of `config.yaml`. Edits to the file on disk are picked up by the control plane's reload mechanism (fsnotify); the next worker spawn uses the updated policy. No restart required.

**Clustering.** `config/` is not in the cluster sync allowlist — `config.yaml` holds secrets and must not replicate across nodes. The filesystem policy therefore is currently **node-local**: each node's operator edits their own `config.yaml`. For single-node deployments this is fine; for multi-node clusters, a targeted sync for the `filesystem` section is a known follow-up.

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
license: MIT
compatibility: Requires kubectl
version: "1.0"
when_to_use: Detailed usage scenarios
argument_hint: "[namespace]"
arguments: [namespace, resource]
metadata:
  author: name
---
```

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | required | Kebab-case, max 64 chars |
| `description` | string | required | Max 1024 chars; trigger description for progressive disclosure |
| `allowed_tools` | string[] | `nil` | Tools granted when skill is activated (session-scoped, additive) |
| `user_invocable` | bool | unset | Whether users can invoke via `/skill-name` |
| `model` | string | inherit | Model override when skill runs as sub-agent |
| `license` | string | optional | License identifier (e.g., MIT, Apache-2.0) |
| `compatibility` | string | optional | System/dependency requirements (max 500 chars) |
| `version` | string | optional | Skill version identifier |
| `when_to_use` | string | optional | Detailed usage scenarios |
| `argument_hint` | string | optional | Hint text for command arguments |
| `arguments` | string[] | optional | Named parameters for argument substitution |
| `metadata` | map | optional | Arbitrary key-value pairs (author, version, etc.) |

